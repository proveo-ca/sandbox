#!/usr/bin/env bash
# SPEC: _spec/_devops/image-lineage-and-publish.puml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

TAG="latest"
PUSH=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --push)
      PUSH=1
      shift
      ;;
    --tag)
      [[ $# -ge 2 ]] || {
        echo "--tag requires a value" >&2
        exit 1
      }
      TAG="$2"
      shift 2
      ;;
    *)
      echo "unknown ensure option: $1" >&2
      exit 1
      ;;
  esac
done
IMAGE="$(proveo_image_ref PROVEO_BASE_NODE_LSP_IMAGE proveo/base-node-lsp "$TAG")"

if [[ -n "$PUSH" ]]; then
  proveo_require_published "$IMAGE" "$TAG" || exit 1
  exit 0
fi

lsp_floor() {
  docker run --rm --entrypoint sh "$IMAGE" -c '
    command -v node >/dev/null \
      && command -v jq >/dev/null \
      && command -v typescript-language-server >/dev/null \
      && command -v pyright-langserver >/dev/null
  ' >/dev/null 2>&1
}

if docker image inspect "$IMAGE" >/dev/null 2>&1; then
  if lsp_floor; then
    exit 0
  fi
  echo "⚠️  $IMAGE present but missing the LSP floor — rebuilding" >&2
  exec "$SCRIPT_DIR/build.sh" --image "$IMAGE"
fi

echo "📥 base-node-lsp image missing — pulling $IMAGE" >&2
if docker pull "$IMAGE" >/dev/null 2>&1 && lsp_floor; then
  exit 0
fi
echo "🔨 pull failed or image lacks the LSP floor — building $IMAGE from source" >&2
exec "$SCRIPT_DIR/build.sh" --image "$IMAGE"
