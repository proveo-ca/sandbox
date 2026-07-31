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
  ./build.sh [--variant mcp|solo|sol|all] [--browser] [--tag <tag>] [--no-cache] [--push]

Builds the claudecode harness images. Defaults to all variants.
sol = mcp + the Solidity/security toolchain (Foundry, solc, solhint, semgrep).
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

build_variant() {
  local variant="$1"
  local image="$2"
  local base="${3:-proveo/base-node-lsp:latest}"
  echo "Building $image:$TAG from $variant (base $base)..."
  proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} --build-arg BASE_IMAGE="$base" \
    -t "$image:$TAG" -f "$SCRIPT_DIR/$variant/Dockerfile" "$SCRIPT_DIR/../.."
}

# Browser variant: the mcp image FROM base-node-browser (Playwright + Chromium),
# short-circuiting the variant matrix below.
if [[ "$BROWSER" == 1 ]]; then
  "$SCRIPT_DIR/../base-node-browser/ensure.sh"
  build_variant mcp proveo/claudecode-browser proveo/base-node-browser:latest
  exit 0
fi

# sol layers the Solidity/security toolchain (Foundry, solc, solhint, semgrep)
# on the mcp image: ensure the same-tag parent exists, then build FROM it.
build_sol() {
  if ! docker image inspect "proveo/claudecode:$TAG" >/dev/null 2>&1; then
    build_variant mcp proveo/claudecode
  fi
  echo "Building proveo/claudecode-sol:$TAG from sol..."
  proveo_docker_build ${PUSH:+--push} ${NO_CACHE:+$NO_CACHE} \
    --build-arg BASE_IMAGE="proveo/claudecode:$TAG" \
    -t "proveo/claudecode-sol:$TAG" -f "$SCRIPT_DIR/sol/Dockerfile" "$SCRIPT_DIR/../.."
}

# mcp/solo build FROM proveo/base-node-lsp (adds the shared workspace LSP servers)
"$SCRIPT_DIR/../base-node-lsp/ensure.sh"

case "$VARIANT" in
  mcp)
    build_variant mcp proveo/claudecode
    ;;
  solo)
    build_variant solo proveo/claudecode-solo
    ;;
  sol)
    build_sol
    ;;
  all)
    build_variant mcp proveo/claudecode
    build_variant solo proveo/claudecode-solo
    build_sol
    ;;
  *)
    echo "Unknown variant: $VARIANT" >&2
    usage
    exit 1
    ;;
esac
