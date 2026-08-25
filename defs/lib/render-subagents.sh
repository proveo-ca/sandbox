#!/usr/bin/env bash
# SPEC: _spec/defs/agent-definition-sharing.puml
# Local preview of what a harness will compose at container start. There is only
# one implementation — render_subagents in packages/lib/entrypoint-lib.sh, the
# same function the entrypoints call — so a preview cannot drift from the image.
#
# Usage: render-subagents.sh <harness> [dest-dir]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
harness="${1:?usage: render-subagents.sh <harness> [dest-dir]}"
dest="${2:-$(mktemp -d)}"

# shellcheck source=/dev/null
source "$ROOT/packages/lib/entrypoint-lib.sh"
export PROVEO_SUBAGENTS_DIR="$ROOT/defs/subagents"

render_subagents "$harness" "$dest" 1
echo "preview: $dest"
