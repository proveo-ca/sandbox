#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_llm.sh - Live round-trip through the Cursor backend (needs CURSOR_API_KEY)

if [[ -z "${CURSOR_API_KEY:-}" ]]; then
  skip_test "live agent round-trip" "CURSOR_API_KEY not set"
else
  TESTS_RUN=$((TESTS_RUN + 1))
  RESULT=$(run_timeout 180 docker run --rm \
    -e CURSOR_API_KEY="$CURSOR_API_KEY" \
    --entrypoint bash \
    "$IMAGE" -c 'cd /app && agent -p --force --trust --output-format text "Reply with exactly: PROVEO_OK"' 2>&1 || true)
  if echo "$RESULT" | grep -q "PROVEO_OK"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] live agent round-trip\n" "$TESTS_RUN"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("live agent round-trip")
    printf "${RED}FAIL${NC} [%d] live agent round-trip (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
  fi
fi
