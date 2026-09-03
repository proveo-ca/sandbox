#!/usr/bin/env bash
# SPEC: _spec/_devops/image-lineage-and-publish.puml, _spec/defs/browser-layer.puml
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
IMAGE="$(proveo_image_ref PROVEO_BASE_NODE_BROWSER_IMAGE proveo/base-node-browser "$TAG")"

if [[ -n "$PUSH" ]]; then
  proveo_require_published "$IMAGE" "$TAG" || exit 1
  exit 0
fi

# Floor: base-node-lsp plus the Playwright CLI, a Chromium under the shared
# browser store, and agent-browser wired to it.
browser_floor() {
  docker run --rm --entrypoint sh "$IMAGE" -c '
    command -v node >/dev/null \
      && command -v playwright >/dev/null \
      && command -v typescript-language-server >/dev/null \
      && ls "${PLAYWRIGHT_BROWSERS_PATH:-/opt/ms-playwright}"/chromium-* >/dev/null 2>&1 \
      && command -v agent-browser >/dev/null \
      && test -x "${AGENT_BROWSER_EXECUTABLE_PATH:-/opt/proveo/chromium/chrome}" \
      && test -f "${AGENT_BROWSER_SKILLS_DIR:-/opt/agent-browser/skill-data}/core/SKILL.md" \
      && test -f /opt/proveo/skills/agent-browser/SKILL.md
  ' >/dev/null 2>&1
}

if docker image inspect "$IMAGE" >/dev/null 2>&1; then
  if browser_floor; then
    exit 0
  fi
  echo "⚠️  $IMAGE present but missing the Playwright/Chromium/agent-browser floor — rebuilding" >&2
  exec "$SCRIPT_DIR/build.sh" --image "$IMAGE"
fi

echo "📥 base-node-browser image missing — pulling $IMAGE" >&2
if docker pull "$IMAGE" >/dev/null 2>&1 && browser_floor; then
  exit 0
fi
echo "🔨 pull failed or image lacks the browser floor — building $IMAGE from source" >&2
exec "$SCRIPT_DIR/build.sh" --image "$IMAGE"
