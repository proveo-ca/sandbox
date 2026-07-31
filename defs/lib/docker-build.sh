#!/usr/bin/env bash
# Shared docker buildx helper for defs/*/build.sh.
#
# Default platforms: linux/amd64,linux/arm64 (override with PROVEO_PLATFORMS).
# Local builds use --load (single platform — host arch when multi is requested).
# Registry publishes use --push / PROVEO_DOCKER_PUSH=1 for a real multi-arch manifest.
#
# Usage (from a def build.sh):
#   source "$SCRIPT_DIR/../lib/docker-build.sh"          # defs/<name>/
#   source "$SCRIPT_DIR/../../lib/docker-build.sh"       # defs/sidecars/<name>/
#   proveo_docker_build [--push] [docker build args...]

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

# proveo_docker_ensure_buildx selects a docker-container builder that can
# cross-build (the default "docker" driver cannot).
proveo_docker_ensure_buildx() {
  if ! docker buildx version >/dev/null 2>&1; then
    echo "❌ docker buildx is required for proveo image builds" >&2
    return 1
  fi
  local builder="${PROVEO_BUILDX_BUILDER:-proveo-multiarch}"
  if ! docker buildx inspect "$builder" >/dev/null 2>&1; then
    echo "🔧 creating buildx builder $builder (docker-container)" >&2
    docker buildx create --name "$builder" --driver docker-container --bootstrap >/dev/null
  fi
  docker buildx use "$builder" >/dev/null
}

# proveo_docker_build [--push] [args...] runs `docker buildx build` with the
# proveo platform defaults. Remaining args are forwarded as to `docker build`
# (-t, -f, --build-arg, --no-cache, context, …).
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
  proveo_docker_ensure_buildx

  local -a out_flags
  if [[ "$push" -eq 1 ]]; then
    out_flags=(--push)
    echo "🔨 buildx --platform ${platforms} --push"
  else
    if [[ "$platforms" == *,* ]]; then
      platforms="$(proveo_docker_host_platform)"
      echo "ℹ️  local image load is single-platform; building ${platforms} (PROVEO_DOCKER_PUSH=1 / --push publishes amd64+arm64)" >&2
    fi
    out_flags=(--load)
    echo "🔨 buildx --platform ${platforms} --load"
  fi

  docker buildx build --platform "$platforms" "${out_flags[@]}" "${docker_args[@]}"
}
