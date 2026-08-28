#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_llm.sh - Live round-trip through the OpenAI backend (needs OPENAI_API_KEY)

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  skip_test "live codex round-trip" "OPENAI_API_KEY not set"
else
  TESTS_RUN=$((TESTS_RUN + 1))
  RESULT=$(run_timeout 300 docker run --rm \
    -e OPENAI_API_KEY="$OPENAI_API_KEY" \
    --entrypoint bash \
    "$IMAGE" -c 'cd /app && codex exec --skip-git-repo-check \
       --dangerously-bypass-approvals-and-sandbox "Reply with exactly: PROVEO_OK"' 2>&1 || true)
  if echo "$RESULT" | grep -q "PROVEO_OK"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] live codex round-trip\n" "$TESTS_RUN"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("live codex round-trip")
    printf "${RED}FAIL${NC} [%d] live codex round-trip (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
  fi
fi
