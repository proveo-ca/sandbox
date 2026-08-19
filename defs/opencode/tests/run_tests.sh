#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

source "$SCRIPT_DIR/helpers.sh"

echo "============================================"
echo "  opencode Image - Test Suite"
echo "  Image: $IMAGE"
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

echo "--- Phase 4: Configuration & Entrypoint ---"
source "$SCRIPT_DIR/test_config.sh"
echo ""

echo "--- Phase 5: Baked-in Defaults ---"
source "$SCRIPT_DIR/test_defaults.sh"
echo ""

echo "--- Phase 6: MCP ---"
source "$SCRIPT_DIR/test_mcp.sh"
echo ""

echo "--- Phase 7: Direct LLM API ---"
source "$SCRIPT_DIR/test_llm.sh"
echo ""


print_summary
exit $?
