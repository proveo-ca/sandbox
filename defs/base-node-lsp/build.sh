#!/usr/bin/env bash
# Build proveo/base-node-lsp (proveo/base-node + the shared workspace language
# servers). Ensures proveo/base-node exists first. The Dockerfile pulls nothing
# from the repo, so the build context is this dir (not the repo root).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

IMAGE="${PROVEO_BASE_NODE_LSP_IMAGE:-proveo/base-node-lsp:latest}"
TAG=""
NO_CACHE=""
PUSH=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) TAG="$2"; shift 2 ;;
    --image) IMAGE="$2"; shift 2 ;;
    --no-cache) NO_CACHE="--no-cache"; shift ;;
    --push) PUSH=1; shift ;;
    -h|--help) echo "Usage: build.sh [--image NAME] [--tag TAG] [--no-cache] [--push]"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done
[[ -n "$TAG" ]] && IMAGE="${IMAGE%%:*}:$TAG"

"$SCRIPT_DIR/../base-node/ensure.sh"

echo "🔨 building $IMAGE (context: $SCRIPT_DIR)"
proveo_docker_build ${PUSH:+--push} \
  ${NO_CACHE:+$NO_CACHE} \
  -f "$SCRIPT_DIR/Dockerfile" \
  -t "$IMAGE" \
  "$SCRIPT_DIR"
