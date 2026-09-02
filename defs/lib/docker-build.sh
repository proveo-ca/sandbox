#!/usr/bin/env bash
# SPEC: _spec/_devops/buildx-driver-selection.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/_devops/agent-version-pin.puml
# Shared docker buildx helper for defs/*/build.sh.

# _proveo_json_field prints one top-level-ish field out of JSON on stdin, by
# jq path ("info.version"), with python3 as the fallback so a host without jq
# still resolves. Prints nothing when neither is present or the path is absent.
_proveo_json_field() {
  local path="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r ".${path} // empty" 2>/dev/null
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 -c '
import json, sys
try:
    v = json.load(sys.stdin)
    for k in sys.argv[1].split("."):
        v = v[k]
    if v is not None:
        print(v)
except Exception:
    pass
' "$path" 2>/dev/null
    return 0
  fi
  cat >/dev/null
}

# proveo_agent_version prints the version of the agent package a harness bakes,
# so build.sh can pin the install to an exact release instead of `@latest`.
#
#   proveo_agent_version <OVERRIDE_VAR> <ecosystem> <package>
#
# ecosystem is one of:
#   npm    — the registry's dist-tag `latest` for <package>
#   pypi   — PyPI's current release of <package>
#   cursor — the build cursor.com/install (URL in <package>) unpacks, read out of
#            the script itself: the installer hardcodes its release and takes no
#            version argument, so this is the only place the version is stated.
#
# Why a pin at all: BuildKit caches a RUN layer by its text and its parents. An
# `npm install -g x@latest` line never changes when upstream publishes, so a warm
# cache keeps last month's agent and nothing in the build says so. Resolving the
# release here and passing it as a --build-arg the Dockerfile USES moves the cache
# key exactly when upstream moves, and lets the image label what it carries.
#
# An exported OVERRIDE_VAR wins outright (OPENCODE_VERSION=1.18.20 proveo build
# opencode): that is how a maintainer rebuilds an older agent, or builds offline.
#
# The bare version goes to stdout; a 📌 note saying which release the build is
# about to bake, and how it was chosen, goes to stderr — beside the buildx line,
# so a maintainer reading a build log sees the agent version without inspecting
# the image. The note is printed HERE rather than by the caller because the
# caller assigns the result to a variable of the same name as OVERRIDE_VAR, after
# which nothing outside this function can tell an override from a resolution.
proveo_agent_version() {
  local override_var="$1" eco="$2" pkg="$3" v=""
  if [[ -n "${!override_var:-}" ]]; then
    echo "📌 ${pkg}@${!override_var} (from ${override_var})" >&2
    printf '%s' "${!override_var}"
    return 0
  fi
  case "$eco" in
    npm)
      if command -v npm >/dev/null 2>&1; then
        v="$(npm view "$pkg" version 2>/dev/null || true)"
      fi
      if [[ -z "$v" ]]; then
        v="$(curl -fsSL "https://registry.npmjs.org/${pkg}/latest" 2>/dev/null | _proveo_json_field version)"
      fi
      ;;
    pypi)
      v="$(curl -fsSL "https://pypi.org/pypi/${pkg}/json" 2>/dev/null | _proveo_json_field info.version)"
      ;;
    cursor)
      v="$(curl -fsSL "$pkg" 2>/dev/null \
        | sed -n 's|.*/versions/\([0-9][0-9.]*-[0-9a-f]\{1,\}\)/.*|\1|p' | head -1)"
      ;;
    *)
      echo "proveo_agent_version: unknown ecosystem '${eco}' (want npm|pypi|cursor)" >&2
      return 1
      ;;
  esac
  v="${v//[[:space:]]/}"
  if [[ -z "$v" ]]; then
    {
      echo "❌ could not resolve the current ${pkg} release (${eco})."
      echo "   The agent install is pinned by version, so a rebuild is reproducible and a"
      echo "   cached layer cannot hide an upstream release. Offline or behind a proxy, name"
      echo "   the version yourself:   ${override_var}=<x.y.z> proveo build <target>"
    } >&2
    return 1
  fi
  echo "📌 ${pkg}@${v} (resolved upstream; override with ${override_var}=<version>)" >&2
  printf '%s' "$v"
}

# proveo_docker_host_platform prints the linux/<arch> matching this machine.
proveo_docker_host_platform() {
  case "$(uname -m)" in
    x86_64 | amd64) echo "linux/amd64" ;;
    aarch64 | arm64) echo "linux/arm64" ;;
    *)
      echo "linux/amd64" # QEMU / unusual hosts: prefer the published default
      ;;
  esac
}

proveo_image_ref() {
  local override="${!1:-}"
  if [[ -n "$override" ]]; then
    printf '%s' "$override"
    return 0
  fi
  printf '%s:%s' "$2" "${3:-latest}"
}

proveo_require_published() {
  local ref="$1" tag="${2:-latest}"
  if docker buildx imagetools inspect "$ref" >/dev/null 2>&1; then
    return 0
  fi

  local name="${ref##*/}"
  name="${name%%:*}"
  local tagflag=""
  [[ "$tag" != "latest" ]] && tagflag=" --tag $tag"

  {
    echo "❌ $ref is not published."
    echo "   A --push build takes its base from the registry, and this script will"
    echo "   not publish $ref as a side effect of deploying its child."
    echo "   Deploying one target whose parents are unpublished is not supported yet."
    echo "   →  proveo deploy all${tagflag}   — publishes every base before its children"
    echo "   →  proveo deploy ${name}${tagflag}   — then re-run this target"
  } >&2
  return 1
}

proveo_ref_tag() {
  local last="${1##*/}"
  case "$last" in
    *:*) printf '%s' "${last##*:}" ;;
    *) printf 'latest' ;;
  esac
}

proveo_docker_container_builder() {
  local builder="${PROVEO_BUILDX_BUILDER:-proveo-multiarch}"
  if ! proveo_docker_builder_running "$builder"; then
    echo "🔧 creating buildx builder $builder (docker-container)" >&2
    docker buildx create --name "$builder" --driver docker-container --bootstrap >/dev/null 2>&1 || true
  fi
  printf '%s' "$builder"
}

proveo_docker_builder_running() {
  local out
  out="$(docker buildx inspect "$1" 2>/dev/null)" || true
  awk 'tolower($1) == "status:" && tolower($2) == "running" { found = 1 }
       END { exit !found }' <<<"$out"
}

proveo_docker_builder_is_docker_driver() {
  local out
  out="$(docker buildx inspect "$1" 2>/dev/null)" || true
  awk 'tolower($1) == "driver:" && tolower($2) == "docker" { found = 1 }
       END { exit !found }' <<<"$out"
}

proveo_docker_ensure_buildx() {
  local mode="${1:-load}" platforms="${2:-}"
  if ! docker buildx version >/dev/null 2>&1; then
    echo "❌ docker buildx is required for proveo image builds" >&2
    return 1
  fi

  if [[ "$mode" == "push" ]]; then
    proveo_docker_container_builder
    return 0
  fi

  local host_platform
  host_platform="$(proveo_docker_host_platform)"
  if [[ -n "$platforms" && "$platforms" != "$host_platform" ]]; then
    echo "ℹ️  ${platforms} != host ${host_platform}: using the cross-capable container driver." >&2
    echo "    A locally built parent image is NOT visible to it — base images resolve from the registry." >&2
    proveo_docker_container_builder
    return 0
  fi

  local builder="${PROVEO_BUILDX_LOCAL_BUILDER:-}"
  if [[ -n "$builder" ]]; then
    printf '%s' "$builder"
    return 0
  fi
  local ctx
  ctx="$(docker context show 2>/dev/null)" || true
  if [[ -n "$ctx" ]] \
     && proveo_docker_builder_is_docker_driver "$ctx" \
     && proveo_docker_builder_running "$ctx"; then
    printf '%s' "$ctx"
    return 0
  fi

  builder="$(proveo_docker_container_builder)"
  echo "⚠️  no running docker-driver builder; using $builder — a locally built base image" >&2
  echo "    will NOT be visible to its dependents (override with PROVEO_BUILDX_LOCAL_BUILDER)" >&2
  printf '%s' "$builder"
}

# proveo_docker_arg_tag reads the --tag value out of a build argv, defaulting to
# latest the same way the docker CLI does when none is given.
proveo_docker_arg_tag() {
  local prev=""
  for a in "$@"; do
    if [[ "$prev" == "--tag" || "$prev" == "-t" ]]; then
      printf '%s' "${a##*:}"
      return 0
    fi
    prev="$a"
  done
  printf 'latest'
}

proveo_docker_build() {
  local push=0
  local -a docker_args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --push)
        push=1
        shift
        ;;
      *)
        docker_args+=("$1")
        shift
        ;;
    esac
  done
  case "${PROVEO_DOCKER_PUSH:-}" in
    1 | true | yes | on) push=1 ;;
  esac

  local platforms="${PROVEO_PLATFORMS:-linux/amd64,linux/arm64}"

  local mode="load"
  local -a out_flags
  if [[ "$push" -eq 1 ]]; then
    mode="push"
    out_flags=(--push)
  else
    # A --load build must never write :latest. That tag means "published", and when a
    # local build also answered to it, anything that re-resolves the reference — sbx
    # pulls it at sandbox creation — could serve the registry image over the build
    # under test, silently. Local builds are :local; `proveo deploy` promotes.
    local want_tag
    want_tag="$(proveo_docker_arg_tag "${docker_args[@]}")"
    if [[ "$want_tag" == "latest" ]]; then
      {
        echo "❌ refusing to --load an image tagged :latest."
        echo "   :latest means published. Build locally as :local, then promote:"
        echo "   →  proveo build <target>            — writes :local"
        echo "   →  proveo deploy <target>           — promotes :local to :latest and pushes"
      } >&2
      return 1
    fi
    if [[ "$platforms" == *,* ]]; then
      platforms="$(proveo_docker_host_platform)"
      echo "ℹ️  local image load is single-platform; building ${platforms} (PROVEO_DOCKER_PUSH=1 / --push publishes amd64+arm64)" >&2
    fi
    out_flags=(--load)
  fi

  local builder
  builder="$(proveo_docker_ensure_buildx "$mode" "$platforms")" || return 1

  echo "🔨 buildx --builder ${builder} --platform ${platforms} ${out_flags[*]}"
  docker buildx build --builder "$builder" --platform "$platforms" "${out_flags[@]}" "${docker_args[@]}"
}
