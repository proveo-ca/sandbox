#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

source "$SCRIPT_DIR/helpers.sh"

cleanup() {
  :
}
trap cleanup EXIT

echo "============================================"
echo "  Claude Code Container - Test Suite"
echo "============================================"
echo ""

echo "--- Phase 1: Build ---"
source "$SCRIPT_DIR/test_build.sh"
echo ""

echo "--- Phase 2: Tool Verification ---"
source "$SCRIPT_DIR/test_tools.sh"
echo ""

echo "--- Phase 3: Security Hardening ---"
source "$SCRIPT_DIR/test_security.sh"
echo ""

echo "--- Phase 4: Configuration ---"
source "$SCRIPT_DIR/test_config.sh"
echo ""

echo "--- Phase 5: Workspace Structure ---"
source "$SCRIPT_DIR/test_workspace.sh"
echo ""

echo "--- Phase 6: Volume Mounts ---"
source "$SCRIPT_DIR/test_volumes.sh"
echo ""

echo "--- Phase 7: Wrapper Contracts ---"
source "$SCRIPT_DIR/test_wrappers.sh"
echo ""

echo "--- Phase 8: Functional Tests ---"
source "$SCRIPT_DIR/test_functional.sh"
echo ""

echo "--- Phase 9: Egress Mode Tests ---"
source "$SCRIPT_DIR/test_egress.sh"
echo ""

echo "--- Phase 10: Claude in Chrome Bridge ---"
source "$SCRIPT_DIR/test_chrome_bridge.sh"
echo ""

print_summary
exit $?
