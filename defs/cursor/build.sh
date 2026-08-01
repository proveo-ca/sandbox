#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

TAG="latest"
BROWSER=0
NO_CACHE=""
PUSH=""

usage() {
  cat <<'EOF'
Usage:
  ./build.sh [--tag <tag>] [--browser] [--no-cache] [--push]

Builds the cursor harness image. --browser builds the cursor-browser variant
FROM proveo/base-node-browser (Playwright + Chromium) so cursor-agent can drive a
browser (e.g. via a Playwright MCP) instead of the lean base.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag)
      [[ $# -ge 2 ]] || { echo "--tag requires a value" >&2; exit 1; }
      TAG="$2"
      shift 2
      ;;
    --browser)
      BROWSER=1
      shift
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

if [[ "$BROWSER" == 1 ]]; then
  IMAGE_NAME="${PROVEO_CURSOR_BROWSER_IMAGE:-proveo/cursor-browser:$TAG}"
  BASE_IMAGE="$(proveo_image_ref PROVEO_BASE_NODE_BROWSER_IMAGE proveo/base-node-browser "$TAG")"
  "$SCRIPT_DIR/../base-node-browser/ensure.sh" --tag "$TAG" ${PUSH:+--push}
else
  IMAGE_NAME="${PROVEO_CURSOR_IMAGE:-proveo/cursor:$TAG}"
  BASE_IMAGE="$(proveo_image_ref PROVEO_BASE_IMAGE proveo/base "$TAG")"
  "$SCRIPT_DIR/../base/ensure.sh" --tag "$TAG" ${PUSH:+--push}
fi

proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} \
  --build-arg BASE_IMAGE="$BASE_IMAGE" \
  -t "$IMAGE_NAME" -f "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR/../.."
