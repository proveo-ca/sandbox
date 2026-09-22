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

Builds the cursor harness image. --browser layers Playwright and Chromium
onto proveo/cursor, tagged proveo/cursor-browser.
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
  PARENT="$(proveo_image_ref PROVEO_CURSOR_IMAGE proveo/cursor "$TAG")"
  if [[ -n "$PUSH" ]]; then
    proveo_require_published "$PARENT" "$TAG" || exit 1
  elif ! docker image inspect "$PARENT" >/dev/null 2>&1; then
    "$SCRIPT_DIR/build.sh" --tag "$TAG" ${NO_CACHE:+--no-cache}
  fi
  LAYER_DIR="$(cd "$SCRIPT_DIR/../base-node-browser" && pwd)"
  echo "Building $IMAGE_NAME on $PARENT..."
  proveo_build_browser_variant "$PARENT" "$IMAGE_NAME" cursor "$LAYER_DIR" ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE}
  exit 0
fi

IMAGE_NAME="${PROVEO_CURSOR_IMAGE:-proveo/cursor:$TAG}"
BASE_IMAGE="$(proveo_image_ref PROVEO_BASE_IMAGE proveo/base "$TAG")"
"$SCRIPT_DIR/../base/ensure.sh" --tag "$TAG" ${PUSH:+--push}

CURSOR_INSTALL_URL="${CURSOR_INSTALL_URL:-https://cursor.com/install}"
CURSOR_AGENT_VERSION="$(proveo_agent_version CURSOR_AGENT_VERSION cursor "$CURSOR_INSTALL_URL")"

proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} \
  --build-arg BASE_IMAGE="$BASE_IMAGE" \
  --build-arg CURSOR_INSTALL_URL="$CURSOR_INSTALL_URL" \
  --build-arg CURSOR_AGENT_VERSION="$CURSOR_AGENT_VERSION" \
  -t "$IMAGE_NAME" -f "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR/../.."
