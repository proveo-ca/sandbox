#!/usr/bin/env bash
# SPEC: _spec/defs/agent-definition-sharing.puml
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
harness="${1:?usage: render-subagents.sh <harness> [dest-dir]}"
dest="${2:-$(mktemp -d)}"

# shellcheck source=/dev/null
source "$ROOT/packages/lib/entrypoint-lib.sh"
export PROVEO_SUBAGENTS_DIR="$ROOT/defs/subagents"

render_subagents "$harness" "$dest" 1
echo "preview: $dest"
