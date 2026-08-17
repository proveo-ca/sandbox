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
IMAGE="$(proveo_image_ref PROVEO_BASE_IMAGE proveo/base "$TAG")"

if [[ -n "$PUSH" ]]; then
  proveo_require_published "$IMAGE" "$TAG" || exit 1
  exit 0
fi

# The minimal-floor contract: git + gh + the proveo-entrypoint binary. (No
# browsers/Node/Python — those moved out of the base.)
base_has_floor() {
  docker run --rm --entrypoint sh "$IMAGE" -c '
    command -v git >/dev/null \
      && command -v gh >/dev/null \
      && command -v jq >/dev/null \
      && test -x /usr/local/bin/proveo-entrypoint
  ' >/dev/null 2>&1
}

if docker image inspect "$IMAGE" >/dev/null 2>&1; then
  if base_has_floor; then
    exit 0
  fi
  echo "⚠️  $IMAGE is present but missing the proveo/base floor — rebuilding" >&2
  exec "$SCRIPT_DIR/build.sh" --image "$IMAGE"
fi

echo "📥 base image missing — pulling $IMAGE" >&2
if docker pull "$IMAGE" >/dev/null 2>&1 && base_has_floor; then
  exit 0
fi
echo "🔨 pull failed or image lacks the floor — building $IMAGE from source" >&2
exec "$SCRIPT_DIR/build.sh" --image "$IMAGE"
