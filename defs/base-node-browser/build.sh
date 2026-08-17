#!/usr/bin/env bash
# SPEC: _spec/_devops/image-lineage-and-publish.puml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

IMAGE="${PROVEO_BASE_NODE_BROWSER_IMAGE:-proveo/base-node-browser:latest}"
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

PARENT_TAG="$(proveo_ref_tag "$IMAGE")"
BASE_IMAGE="$(proveo_image_ref PROVEO_BASE_NODE_LSP_IMAGE proveo/base-node-lsp "$PARENT_TAG")"
"$SCRIPT_DIR/../base-node-lsp/ensure.sh" --tag "$PARENT_TAG" ${PUSH:+--push}

echo "🔨 building $IMAGE from $BASE_IMAGE (context: $SCRIPT_DIR)"
proveo_docker_build ${PUSH:+--push} \
  ${NO_CACHE:+$NO_CACHE} \
  --build-arg BASE_IMAGE="$BASE_IMAGE" \
  -f "$SCRIPT_DIR/Dockerfile" \
  -t "$IMAGE" \
  "$SCRIPT_DIR"
