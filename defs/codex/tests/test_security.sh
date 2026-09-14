#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_security.sh - Runtime hardening posture

assert_output_contains \
  "container runs as the codex user" \
  "$IMAGE" \
  "whoami" \
  "codex"

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

# The baked defaults are the harness's, not the agent's: it may seed FROM them and
# it may edit its own $CODEX_HOME copy, but it must not rewrite the source.
assert_failure \
  "baked defaults are not writable by the runtime user" \
  "$IMAGE" \
  "test -w /opt/codex/defaults/config.toml"

assert_failure \
  "house rules source is not writable by the runtime user" \
  "$IMAGE" \
  "test -w /opt/proveo/AGENTS.md"

# /home/agent must be a REAL directory: sbx's kits mount block volumes under it,
# and a mount target cannot resolve through a symlink.
assert_success \
  "/home/agent is a real directory (sbx mounts volumes under it)" \
  "$IMAGE" \
  "test -d /home/agent && test ! -L /home/agent"
