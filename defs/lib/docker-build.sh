#!/usr/bin/env bash
# SPEC: _spec/_devops/buildx-driver-selection.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/_devops/agent-version-pin.puml

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

# SPEC: _spec/_devops/agent-version-pin.puml
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

proveo_image_created() {
  local ts
  ts="$(docker image inspect "$1" --format '{{.Created}}' 2>/dev/null)" || return 1
  [[ -n "$ts" ]] || return 1
  ts="${ts%.*}"; ts="${ts%Z}"
  date -u -d "${ts}Z" +%s 2>/dev/null \
    || date -u -j -f '%Y-%m-%dT%H:%M:%S' "$ts" +%s 2>/dev/null \
    || return 1
}

# SPEC: _spec/_devops/image-lineage-and-publish.puml
proveo_resolve_image() {
  local ref="$1" repo local_ref local_at pub_at
  [[ "$(proveo_ref_tag "$ref")" == "latest" ]] || { printf '%s' "$ref"; return 0; }
  repo="${ref%:*}"
  local_ref="${repo}:local"

  if ! local_at="$(proveo_image_created "$local_ref")"; then
    printf '%s' "$ref"; return 0
  fi
  if ! pub_at="$(proveo_image_created "$ref")" || (( local_at > pub_at )); then
    printf '%s' "$local_ref"; return 0
  fi
  printf '%s' "$ref"
}

proveo_test_image() {
  local ref chosen
  ref="$1"
  chosen="$(proveo_resolve_image "$ref")"
  if [[ "$chosen" != "$ref" ]]; then
    echo "🧪 image: $chosen (local build — newer than the published tag)" >&2
  else
    echo "🧪 image: $chosen (published)" >&2
  fi
  printf '%s' "$chosen"
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
