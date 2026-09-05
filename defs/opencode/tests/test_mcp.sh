#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

FIXTURE_DIR=$(mktemp -d)
trap 'rm -rf "$FIXTURE_DIR"' RETURN

cat >"$FIXTURE_DIR/opencode.json" <<'EOF'
{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "mcp": {
    "fs-test": {
      "type": "local",
      "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/app"],
      "enabled": true
    }
  }
}
EOF
echo "MCP_FIXTURE_OK" >"$FIXTURE_DIR/marker.txt"

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  -w /app \
  --entrypoint bash \
  "$IMAGE" -c 'cd /app && timeout 60 opencode mcp list 2>&1 || true')
if echo "$RESULT" | grep -qE "fs-test|filesystem"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] opencode discovers MCP server from opencode.json\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("opencode discovers MCP server from opencode.json")
  printf "${RED}FAIL${NC} [%d] MCP discovery (output: %.400s)\n" "$TESTS_RUN" "$RESULT"
fi

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  skip_test "opencode can invoke MCP tool end-to-end" "no ANTHROPIC_API_KEY"
  return 0 2>/dev/null || exit 0
fi

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  -w /app \
  -e "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY}" \
  --entrypoint bash \
  "$IMAGE" -c 'cd /app && timeout 180 opencode run -m anthropic/claude-sonnet-4-5 "Use the fs-test MCP server to read /app/marker.txt and reply with its exact contents only." 2>&1')
if echo "$RESULT" | grep -q "MCP_FIXTURE_OK"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] opencode invokes MCP tool end-to-end\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("opencode invokes MCP tool end-to-end")
  printf "${RED}FAIL${NC} [%d] MCP tool invocation (output: %.500s)\n" "$TESTS_RUN" "$RESULT"
fi
