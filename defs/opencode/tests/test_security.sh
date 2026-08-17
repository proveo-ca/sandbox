#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_security.sh - Security hardening verification

assert_output_contains \
  "runs as user opencode" \
  "$IMAGE" \
  "whoami" \
  "opencode"

EXPECTED_UID="${EXPECTED_UID:-1000}"

assert_output_contains \
  "UID is $EXPECTED_UID" \
  "$IMAGE" \
  "id -u" \
  "$EXPECTED_UID"

assert_failure \
  "no setuid binaries" \
  "$IMAGE" \
  "find / -xdev -perm -4000 -type f 2>/dev/null | grep -q ."

assert_failure \
  "no setgid binaries" \
  "$IMAGE" \
  "find / -xdev -perm -2000 -type f 2>/dev/null | grep -q ."

assert_failure "nc not available" "$IMAGE" "which nc"
assert_failure "netcat not available" "$IMAGE" "which netcat"
assert_failure "netstat not available" "$IMAGE" "which netstat"
assert_failure "ss not available" "$IMAGE" "which ss"

assert_failure \
  "cannot write to /usr/bin" \
  "$IMAGE" \
  "touch /usr/bin/testfile 2>/dev/null"

assert_failure \
  "cannot write to /etc" \
  "$IMAGE" \
  "touch /etc/testfile 2>/dev/null"

assert_output_contains \
  "auto-update is disabled" \
  "$IMAGE" \
  'echo $OPENCODE_AUTO_UPDATE' \
  "false"

# Run-as-host-uid contract: any `--user` uid (not just the baked 1000) must
# get a usable identity and writable HOME via ensure_runtime_user.
TESTS_RUN=$((TESTS_RUN + 1))
LAST_OUTPUT=$(docker run --rm --user 4242:4242 --entrypoint bash "$IMAGE" -c \
  'source /entrypoint-lib.sh && ensure_runtime_user && echo "uid=$(id -u) home_writable=$(test -w "$HOME" && echo yes || echo no)"' 2>&1)
if echo "$LAST_OUTPUT" | grep -qF "uid=4242 home_writable=yes"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] arbitrary --user uid gets usable identity and writable HOME\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("arbitrary --user uid gets usable identity and writable HOME")
  printf "${RED}FAIL${NC} [%d] arbitrary --user uid gets usable identity and writable HOME\n" "$TESTS_RUN"
  printf "     Output: %.200s\n" "$LAST_OUTPUT"
fi

# Never root at runtime, even without wrapper flags.
assert_failure \
  "does not run as root by default" \
  "$IMAGE" \
  '[ "$(id -u)" = "0" ]'
