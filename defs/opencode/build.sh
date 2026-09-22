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

Builds the opencode harness image. --browser layers Playwright and Chromium
onto proveo/opencode, tagged proveo/opencode-browser.
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
  IMAGE_NAME="${PROVEO_OPENCODE_BROWSER_IMAGE:-proveo/opencode-browser:$TAG}"
  PARENT="$(proveo_image_ref PROVEO_OPENCODE_IMAGE proveo/opencode "$TAG")"
  if [[ -n "$PUSH" ]]; then
    proveo_require_published "$PARENT" "$TAG" || exit 1
  elif ! docker image inspect "$PARENT" >/dev/null 2>&1; then
    "$SCRIPT_DIR/build.sh" --tag "$TAG" ${NO_CACHE:+--no-cache}
  fi
  LAYER_DIR="$(cd "$SCRIPT_DIR/../base-node-browser" && pwd)"
  echo "Building $IMAGE_NAME on $PARENT..."
  proveo_build_browser_variant "$PARENT" "$IMAGE_NAME" opencode "$LAYER_DIR" ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE}
  exit 0
fi

IMAGE_NAME="${PROVEO_OPENCODE_IMAGE:-proveo/opencode:$TAG}"
BASE_IMAGE="$(proveo_image_ref PROVEO_BASE_NODE_LSP_IMAGE proveo/base-node-lsp "$TAG")"
"$SCRIPT_DIR/../base-node-lsp/ensure.sh" --tag "$TAG" ${PUSH:+--push}

OPENCODE_VERSION="$(proveo_agent_version OPENCODE_VERSION npm opencode-ai)"

proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} \
  --build-arg BASE_IMAGE="$BASE_IMAGE" \
  --build-arg OPENCODE_VERSION="$OPENCODE_VERSION" \
  -t "$IMAGE_NAME" -f "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR/../.."
