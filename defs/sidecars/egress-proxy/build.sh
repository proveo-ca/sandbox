#!/usr/bin/env bash
# Build the Go egress inspection proxy image. Build context is the repo root
# (the Go module); the Dockerfile lives here.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
# shellcheck source=../../lib/docker-build.sh
source "$SCRIPT_DIR/../../lib/docker-build.sh"

IMAGE="${PROVEO_EGRESS_PROXY_IMAGE:-proveo/egress-proxy:latest}"
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

echo "🔨 building $IMAGE (context: $REPO_ROOT)"
proveo_docker_build ${PUSH:+--push} \
  ${NO_CACHE:+$NO_CACHE} \
  -f "$SCRIPT_DIR/Dockerfile" \
  -t "$IMAGE" \
  "$REPO_ROOT"
