#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

source "$SCRIPT_DIR/helpers.sh"

# Cleanup trap for temp dirs on unexpected exit
cleanup() {
  # Nothing persistent to clean; individual tests handle their own temp dirs
  :
}
trap cleanup EXIT

echo "============================================"
echo "  Claude Code Container - Test Suite"
echo "============================================"
echo ""

# --- Phase 1: Build ---
echo "--- Phase 1: Build ---"
source "$SCRIPT_DIR/test_build.sh"
echo ""

# --- Phase 2: Tools ---
echo "--- Phase 2: Tool Verification ---"
source "$SCRIPT_DIR/test_tools.sh"
echo ""

# --- Phase 3: Security ---
echo "--- Phase 3: Security Hardening ---"
source "$SCRIPT_DIR/test_security.sh"
echo ""

# --- Phase 4: Configuration ---
echo "--- Phase 4: Configuration ---"
source "$SCRIPT_DIR/test_config.sh"
echo ""

# --- Phase 5: Workspace ---
echo "--- Phase 5: Workspace Structure ---"
source "$SCRIPT_DIR/test_workspace.sh"
echo ""

# --- Phase 6: Volumes ---
echo "--- Phase 6: Volume Mounts ---"
source "$SCRIPT_DIR/test_volumes.sh"
echo ""

# --- Phase 7: Wrapper Contracts ---
echo "--- Phase 7: Wrapper Contracts ---"
source "$SCRIPT_DIR/test_wrappers.sh"
echo ""

# --- Phase 8: Functional ---
echo "--- Phase 8: Functional Tests ---"
source "$SCRIPT_DIR/test_functional.sh"
echo ""

# --- Phase 9: Egress Modes ---
echo "--- Phase 9: Egress Mode Tests ---"
source "$SCRIPT_DIR/test_egress.sh"
echo ""

# --- Phase 10: Claude in Chrome bridge ---
echo "--- Phase 10: Claude in Chrome Bridge ---"
source "$SCRIPT_DIR/test_chrome_bridge.sh"
echo ""

# --- Summary ---
print_summary
exit $?
