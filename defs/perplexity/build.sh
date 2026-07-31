#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

TAG="latest"
NO_CACHE=""
PUSH=""

usage() {
  cat <<'EOF'
Usage:
  ./build.sh [--tag <tag>] [--no-cache] [--push]

Builds the perplexity (pplx CLI) harness image.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag)
      [[ $# -ge 2 ]] || { echo "--tag requires a value" >&2; exit 1; }
      TAG="$2"
      shift 2
      ;;
    --no-cache)
      NO_CACHE="--no-cache"
      shift
      ;;
    --push)
      PUSH=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown build option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

IMAGE_NAME="${PROVEO_PERPLEXITY_IMAGE:-proveo/perplexity:$TAG}"
BASE_IMAGE="proveo/base:latest"
"$SCRIPT_DIR/../base/ensure.sh"

proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} \
  --build-arg BASE_IMAGE="$BASE_IMAGE" \
  -t "$IMAGE_NAME" -f "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR/../.."
