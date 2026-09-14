#!/usr/bin/env bash
# SPEC: _spec/_devops/image-lineage-and-publish.puml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

TAG="latest"
BROWSER=0
NO_CACHE=""
PUSH=""
CODEX_VERSION="${CODEX_VERSION:-latest}"

usage() {
  cat <<'USAGE'
Usage:
  ./build.sh [--tag <tag>] [--browser] [--codex-version <npm-version>]
             [--no-cache] [--push]

Builds the codex harness image (proveo/codex) FROM proveo/base-node-lsp — the
shared base that carries node plus the workspace language servers the entrypoint
wires into Codex as MCP servers.

--browser builds the same Dockerfile FROM proveo/base-node-browser (Playwright +
Chromium), tagged proveo/codex-browser, so codex can drive a browser (e.g. via a
Playwright MCP). That base is itself FROM base-node-lsp, so the browser variant
keeps the language servers rather than trading them away.

--codex-version pins @openai/codex (default: latest). Also settable as
CODEX_VERSION in the environment.
USAGE
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
    --codex-version)
      [[ $# -ge 2 ]] || { echo "--codex-version requires a value" >&2; exit 1; }
      CODEX_VERSION="$2"
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

if [[ "$BROWSER" == 1 ]]; then
  IMAGE_NAME="${PROVEO_CODEX_BROWSER_IMAGE:-proveo/codex-browser:$TAG}"
  BASE_IMAGE="$(proveo_image_ref PROVEO_BASE_NODE_BROWSER_IMAGE proveo/base-node-browser "$TAG")"
  "$SCRIPT_DIR/../base-node-browser/ensure.sh" --tag "$TAG" ${PUSH:+--push}
else
  IMAGE_NAME="${PROVEO_CODEX_IMAGE:-proveo/codex:$TAG}"
  BASE_IMAGE="$(proveo_image_ref PROVEO_BASE_NODE_LSP_IMAGE proveo/base-node-lsp "$TAG")"
  "$SCRIPT_DIR/../base-node-lsp/ensure.sh" --tag "$TAG" ${PUSH:+--push}
fi

echo "Building $IMAGE_NAME from base $BASE_IMAGE (@openai/codex@$CODEX_VERSION)..."
proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} \
  --build-arg BASE_IMAGE="$BASE_IMAGE" \
  --build-arg CODEX_VERSION="$CODEX_VERSION" \
  -t "$IMAGE_NAME" -f "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR/../.."
