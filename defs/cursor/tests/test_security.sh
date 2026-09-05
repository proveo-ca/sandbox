#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

assert_output_contains \
  "container runs as the cursor user" \
  "$IMAGE" \
  "whoami" \
  "cursor"

assert_failure \
  "nc is not present" \
  "$IMAGE" \
  "command -v nc"

assert_failure \
  "netcat is not present" \
  "$IMAGE" \
  "command -v netcat"

TESTS_RUN=$((TESTS_RUN + 1))
SETUID_FILES=$(docker run --rm --entrypoint bash "$IMAGE" -c "find / -xdev -perm -4000 -type f 2>/dev/null" 2>&1 || true)
if [[ -z "$SETUID_FILES" ]]; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] no setuid binaries remain\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("no setuid binaries remain")
  printf "${RED}FAIL${NC} [%d] no setuid binaries remain (%s)\n" "$TESTS_RUN" "$SETUID_FILES"
fi

assert_success \
  "enterprise hooks.json exists" \
  "$IMAGE" \
  "test -f /etc/cursor/hooks.json"

assert_failure \
  "enterprise hooks.json is not writable by the runtime user" \
  "$IMAGE" \
  "test -w /etc/cursor/hooks.json"

assert_failure \
  "cursor dist prefix is not writable by the runtime user (no self-update/tamper)" \
  "$IMAGE" \
  "test -w /opt/cursor-dist/.local/bin"
