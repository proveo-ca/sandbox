#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

for image in $(images_to_test); do
  tag=$(image_tag "$image")
  TESTS_RUN=$((TESTS_RUN + 1))
  RESULT=$(docker run --rm --entrypoint bash \
    -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-}" \
    "$image" -c "claude --version" 2>&1)
  if echo "$RESULT" | grep -qE "[0-9]+\.[0-9]+"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] [$tag] claude --version returns version\n" "$TESTS_RUN"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("[$tag] claude --version returns version")
    printf "${RED}FAIL${NC} [%d] [$tag] claude --version (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
  fi
done

if [[ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  skip_test "claude -p prompt test (standalone)" "no CLAUDE_CODE_OAUTH_TOKEN"
  skip_test "claude reads input volume (standalone)" "no CLAUDE_CODE_OAUTH_TOKEN"
  skip_test "claude writes output volume (standalone)" "no CLAUDE_CODE_OAUTH_TOKEN"
  if $MCP_IMAGE_AVAILABLE; then
    skip_test "claude -p prompt test (mcp)" "no CLAUDE_CODE_OAUTH_TOKEN"
    skip_test "claude lists MCP tools (mcp)" "no CLAUDE_CODE_OAUTH_TOKEN"
  fi
  return 0 2>/dev/null || exit 0
fi

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(docker run --rm --entrypoint bash \
  -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
  "$STANDALONE_IMAGE" -c 'timeout 120 claude -p "Respond with only the word PONG" 2>&1')
if echo "$RESULT" | grep -qi "PONG"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] [standalone] claude -p returns expected output\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("[standalone] claude -p returns expected output")
  printf "${RED}FAIL${NC} [%d] [standalone] claude -p (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
fi

TESTS_RUN=$((TESTS_RUN + 1))
TEST_INPUT=$(mktemp -d)
echo "TEST_MARKER_ABC123" > "$TEST_INPUT/marker.txt"
RESULT=$(docker run --rm --entrypoint bash \
  -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
  -v "$TEST_INPUT:/workspace/input:ro" \
  "$STANDALONE_IMAGE" -c 'timeout 120 claude -p "Read the file /workspace/input/marker.txt and reply with its exact contents only" 2>&1')
rm -rf "$TEST_INPUT"
if echo "$RESULT" | grep -q "TEST_MARKER_ABC123"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] [standalone] claude can read input volume files\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("[standalone] claude can read input volume files")
  printf "${RED}FAIL${NC} [%d] [standalone] claude can read input volume files (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
fi

TESTS_RUN=$((TESTS_RUN + 1))
TEST_OUTPUT=$(mktemp -d)
docker run --rm --entrypoint bash \
  -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
  -v "$TEST_OUTPUT:/workspace/output:rw" \
  "$STANDALONE_IMAGE" -c 'timeout 120 claude -p "Write the text OUTPUT_MARKER_XYZ789 to /workspace/output/test-result.txt" 2>&1'
if [[ -f "$TEST_OUTPUT/test-result.txt" ]] && grep -q "OUTPUT_MARKER_XYZ789" "$TEST_OUTPUT/test-result.txt"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] [standalone] claude can write to output volume\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("[standalone] claude can write to output volume")
  printf "${RED}FAIL${NC} [%d] [standalone] claude can write to output volume\n" "$TESTS_RUN"
fi
rm -rf "$TEST_OUTPUT"

if $MCP_IMAGE_AVAILABLE; then
  TESTS_RUN=$((TESTS_RUN + 1))
  RESULT=$(docker run --rm --entrypoint bash \
    -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
    "$MCP_IMAGE" -c 'timeout 120 claude -p "Respond with only the word PONG" 2>&1')
  if echo "$RESULT" | grep -qi "PONG"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] [mcp] claude -p returns expected output\n" "$TESTS_RUN"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("[mcp] claude -p returns expected output")
    printf "${RED}FAIL${NC} [%d] [mcp] claude -p (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
  fi

  TESTS_RUN=$((TESTS_RUN + 1))
  RESULT=$(docker run --rm --entrypoint bash \
    -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
    "$MCP_IMAGE" -c 'timeout 120 claude -p "List your available MCP tools" 2>&1 || true')
  if echo "$RESULT" | grep -qiE "mcp|tool"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] [mcp] claude recognizes MCP tools\n" "$TESTS_RUN"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("[mcp] claude responds to MCP tools prompt")
    printf "${RED}FAIL${NC} [%d] [mcp] claude responds to MCP tools prompt (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
  fi
fi
