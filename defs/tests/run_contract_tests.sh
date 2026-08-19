#!/usr/bin/env bash
# SPEC: _spec/tests/00-testing-overview.puml, _spec/tests/20-contract.puml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if command -v go >/dev/null 2>&1; then
  _bin_dir="$(mktemp -d)"
  ( cd "$REPO_ROOT" && go build -o "$_bin_dir/proveo-egress" ./cmd/proveo-egress )
  export PROVEO_EGRESS_BIN="$_bin_dir/proveo-egress"
  trap 'rm -rf "$_bin_dir"' EXIT
else
  echo "⚠️  go toolchain not found; egress.sh will fall back to 'go run' per call" >&2
fi

"$SCRIPT_DIR/test_harness_contracts.sh"
"$SCRIPT_DIR/../claudecode/tests/test_egress.sh"
