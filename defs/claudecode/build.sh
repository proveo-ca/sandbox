#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

VARIANT="all"
TAG="latest"
BROWSER=0
NO_CACHE=""
PUSH=""

usage() {
  cat <<'EOF'
Usage:
  ./build.sh [--variant mcp|solidity|all] [--browser] [--tag <tag>] [--no-cache] [--push]

Builds the claudecode harness images. Defaults to all variants.
solidity = mcp + the Solidity/security toolchain (Foundry, solc, solhint, semgrep).
--browser = the mcp image FROM proveo/base-node-browser (Playwright + Chromium),
tagged proveo/claudecode-browser.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --variant)
      [[ $# -ge 2 ]] || { echo "--variant requires a value" >&2; exit 1; }
      VARIANT="$2"
      shift 2
      ;;
    --browser)
      BROWSER=1
      shift
      ;;
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

CLAUDE_CODE_VERSION="$(proveo_agent_version CLAUDE_CODE_VERSION npm @anthropic-ai/claude-code)"

build_variant() {
  local variant="$1"
  local image="$2"
  local base="${3:-$(proveo_image_ref PROVEO_BASE_NODE_LSP_IMAGE proveo/base-node-lsp "$TAG")}"
  echo "Building $image:$TAG from $variant (base $base)..."
  proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} --build-arg BASE_IMAGE="$base" \
    --build-arg CLAUDE_CODE_VERSION="$CLAUDE_CODE_VERSION" \
    -t "$image:$TAG" -f "$SCRIPT_DIR/$variant/Dockerfile" "$SCRIPT_DIR/../.."
}

if [[ "$BROWSER" == 1 ]]; then
  "$SCRIPT_DIR/../base-node-browser/ensure.sh" --tag "$TAG" ${PUSH:+--push}
  build_variant mcp proveo/claudecode-browser \
    "$(proveo_image_ref PROVEO_BASE_NODE_BROWSER_IMAGE proveo/base-node-browser "$TAG")"
  exit 0
fi

build_solidity() {
  local parent="proveo/claudecode:$TAG"
  if [[ -n "$PUSH" ]]; then
    proveo_require_published "$parent" "$TAG" || return 1
  elif ! docker image inspect "$parent" >/dev/null 2>&1; then
    build_variant mcp proveo/claudecode
  fi
  echo "Building proveo/claudecode-solidity:$TAG from solidity..."
  proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} \
    --build-arg BASE_IMAGE="proveo/claudecode:$TAG" \
    -t "proveo/claudecode-solidity:$TAG" -f "$SCRIPT_DIR/solidity/Dockerfile" "$SCRIPT_DIR/../.."
}

"$SCRIPT_DIR/../base-node-lsp/ensure.sh" --tag "$TAG" ${PUSH:+--push}

case "$VARIANT" in
  mcp)
    build_variant mcp proveo/claudecode
    ;;
  solidity)
    build_solidity
    ;;
  all)
    build_variant mcp proveo/claudecode
    build_solidity
    ;;
  *)
    echo "Unknown variant: $VARIANT" >&2
    usage
    exit 1
    ;;
esac
