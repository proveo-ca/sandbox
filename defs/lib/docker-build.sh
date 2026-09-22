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
_proveo_pypi_version() {
  local pkg="$1"
  python3 -c '
import json, sys, urllib.request
url = "https://pypi.org/pypi/{}/json".format(sys.argv[1])
with urllib.request.urlopen(url, timeout=20) as r:
    print(json.load(r)["info"]["version"])
' "$pkg"
}

proveo_agent_version() {
  local override_var="$1" eco="$2" pkg="$3" v="" fetch_err=""
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
        v="$(curl -fsSL --max-time 20 "https://registry.npmjs.org/${pkg}/latest" 2>/dev/null | _proveo_json_field version)"
      fi
      ;;
    pypi)
      local pypi_url="https://pypi.org/pypi/${pkg}/json"
      v="$(curl -fsSL --max-time 20 "$pypi_url" 2>/dev/null | _proveo_json_field info.version)"
      if [[ -z "$v" ]] && command -v python3 >/dev/null 2>&1; then
        v="$(_proveo_pypi_version "$pkg" 2>/dev/null || true)"
      fi
      if [[ -z "$v" ]]; then
        fetch_err="$(curl -fsSL --max-time 8 -o /dev/null "$pypi_url" 2>&1 | tail -n 1 || true)"
      fi
      ;;
    cursor)
      v="$(curl -fsSL --max-time 20 "$pkg" 2>/dev/null \
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
      if [[ -n "$fetch_err" ]]; then
        echo "   last curl: ${fetch_err}"
      fi
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

# SPEC: _spec/_devops/buildx-driver-selection.puml
# PROVEO_DOCKER_PULL: auto (default) | 0/false/never/off | 1/true/always/on/yes
proveo_docker_pull_flags() {
  case "${PROVEO_DOCKER_PULL:-auto}" in
    auto | "")
      return 0
      ;;
    0 | false | no | never | off)
      printf '%s\n' '--pull=false'
      ;;
    1 | true | yes | on | always)
      printf '%s\n' '--pull=true'
      ;;
    *)
      echo "❌ PROVEO_DOCKER_PULL=${PROVEO_DOCKER_PULL}: want auto|0|1 (or never/always)" >&2
      return 1
      ;;
  esac
}

proveo_docker_argv_has_pull() {
  local a
  for a in "$@"; do
    case "$a" in
      --pull | --pull=*) return 0 ;;
    esac
  done
  return 1
}

proveo_docker_is_registry_dns_error() {
  grep -Eiq \
    'temporary failure in name resolution|lookup registry-1\.docker\.io|no such host|server misbehaving'
}

proveo_docker_registry_dns_help() {
  cat <<'EOF'
❌ Docker Hub name lookup failed (registry-1.docker.io).
   That is the daemon's DNS, not the Dockerfile FROM line.

   If the FROM image is already local, skip the registry HEAD:
     PROVEO_DOCKER_PULL=0 mise run build

   If it is not local, Hub must resolve first:
     docker pull docker/sandbox-templates:shell-docker-0.5.0
     getent hosts registry-1.docker.io

   Frozen container-builder resolv.conf (proveo-multiarch):
     docker buildx rm proveo-multiarch
EOF
}

proveo_docker_arg_image() {
  local prev=""
  for a in "$@"; do
    if [[ "$prev" == "--tag" || "$prev" == "-t" ]]; then
      printf '%s' "${a%%:*}"
      return 0
    fi
    prev="$a"
  done
  return 1
}

# SPEC: _spec/internal/maintain/build-schedule.puml
# local cache exporter, mode=max, shared by --load and --push
proveo_docker_cache_flags() {
  case "${PROVEO_BUILDKIT_CACHE:-1}" in
    0 | false | no | off) return 0 ;;
  esac
  local image
  image="$(proveo_docker_arg_image "$@")" || return 0
  local root="${PROVEO_BUILDKIT_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/proveo/buildkit}"
  local dest="${root}/${image}"
  if ! mkdir -p "$dest"; then
    echo "⚠️  buildkit cache dir ${dest} is not writable; building without a named cache" >&2
    return 0
  fi
  printf '%s\n' \
    "--cache-from=type=local,src=${dest}" \
    "--cache-to=type=local,dest=${dest},mode=max"
}

proveo_build_browser_variant() {
  local parent="$1" dest="$2" user_name="$3" layer_dir="$4"
  shift 4
  proveo_docker_build "$@" \
    --build-arg "BASE_IMAGE=${parent}" \
    --build-arg "USER_NAME=${user_name}" \
    -f "${layer_dir}/Dockerfile" \
    -t "${dest}" \
    "${layer_dir}"
}

proveo_docker_buildx_invoke() {
  local builder="$1" platforms="$2"
  shift 2
  echo "🔨 buildx --builder ${builder} --platform ${platforms} $*"
  docker buildx build --builder "$builder" --platform "$platforms" "$@"
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

  local -a pull_flags=()
  if ! proveo_docker_argv_has_pull "${docker_args[@]}"; then
    local pull_line
    pull_line="$(proveo_docker_pull_flags)" || return 1
    if [[ -n "$pull_line" ]]; then
      pull_flags=("$pull_line")
    fi
  fi

  local -a cache_flags=()
  local cache_text
  cache_text="$(proveo_docker_cache_flags "${docker_args[@]}")" || return 1
  if [[ -n "$cache_text" ]]; then
    mapfile -t cache_flags <<<"$cache_text"
  fi

  local log st
  log="$(mktemp)"
  set +e
  proveo_docker_buildx_invoke "$builder" "$platforms" "${out_flags[@]}" "${pull_flags[@]}" "${cache_flags[@]}" "${docker_args[@]}" 2>&1 | tee "$log"
  st=${PIPESTATUS[0]}
  set -e

  if ((st != 0)) \
    && [[ "$mode" == "load" ]] \
    && [[ "${PROVEO_DOCKER_PULL:-auto}" == "auto" ]] \
    && [[ ${#pull_flags[@]} -eq 0 ]] \
    && ! proveo_docker_argv_has_pull "${docker_args[@]}" \
    && proveo_docker_is_registry_dns_error <"$log"; then
    echo "⚠️  registry DNS failed; retrying with --pull=false so a local FROM image can satisfy the build" >&2
    pull_flags=(--pull=false)
    set +e
    proveo_docker_buildx_invoke "$builder" "$platforms" "${out_flags[@]}" "${pull_flags[@]}" "${cache_flags[@]}" "${docker_args[@]}" 2>&1 | tee "$log"
    st=${PIPESTATUS[0]}
    set -e
  fi
  if ((st != 0)) && proveo_docker_is_registry_dns_error <"$log"; then
    proveo_docker_registry_dns_help >&2
  fi
  rm -f "$log"
  return "$st"
}
