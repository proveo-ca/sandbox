#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/helpers.sh - Test framework helpers for cursor image

TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0
FAILURES=()

IMAGE="${IMAGE:-proveo/cursor:latest}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

if command -v timeout >/dev/null 2>&1; then
  TIMEOUT_BIN="timeout"
elif command -v gtimeout >/dev/null 2>&1; then
  TIMEOUT_BIN="gtimeout"
else
  TIMEOUT_BIN=""
  printf "WARN: no 'timeout'/'gtimeout' on host; time-bounded steps run unbounded (install coreutils for limits).\n" >&2
fi

# run_timeout <duration> <cmd...> — wraps cmd with $TIMEOUT_BIN when available.
run_timeout() {
  local dur="$1"; shift
  if [[ -n "$TIMEOUT_BIN" ]]; then
    "$TIMEOUT_BIN" "$dur" "$@"
  else
    "$@"
  fi
}

# docker_exec <image> <command_string>
# Runs the command inside the image (overrides entrypoint), captures combined output.
docker_exec() {
  local image="$1"; shift
  local cmd="$*"
  LAST_OUTPUT=$(docker run --rm --entrypoint bash "$image" -c "$cmd" 2>&1)
  return $?
}

assert_success() {
  local desc="$1" image="$2"; shift 2
  local cmd="$*"
  TESTS_RUN=$((TESTS_RUN + 1))
  if docker_exec "$image" "$cmd"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("$desc")
    printf "${RED}FAIL${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
    printf "     Command: %s\n" "$cmd"
    printf "     Output:  %.300s\n" "$LAST_OUTPUT"
  fi
}

assert_failure() {
  local desc="$1" image="$2"; shift 2
  local cmd="$*"
  TESTS_RUN=$((TESTS_RUN + 1))
  if docker_exec "$image" "$cmd"; then
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("$desc")
    printf "${RED}FAIL${NC} [%d] %s (expected failure but got success)\n" "$TESTS_RUN" "$desc"
  else
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
  fi
}

assert_output_contains() {
  local desc="$1" image="$2" cmd="$3" expected="$4"
  TESTS_RUN=$((TESTS_RUN + 1))
  docker_exec "$image" "$cmd"
  if echo "$LAST_OUTPUT" | grep -qF "$expected"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("$desc")
    printf "${RED}FAIL${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
    printf "     Expected to contain: %s\n" "$expected"
    printf "     Actual: %.300s\n" "$LAST_OUTPUT"
  fi
}

assert_output_matches() {
  local desc="$1" image="$2" cmd="$3" pattern="$4"
  TESTS_RUN=$((TESTS_RUN + 1))
  docker_exec "$image" "$cmd"
  if echo "$LAST_OUTPUT" | grep -qE "$pattern"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("$desc")
    printf "${RED}FAIL${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
    printf "     Expected to match: %s\n" "$pattern"
    printf "     Actual: %.300s\n" "$LAST_OUTPUT"
  fi
}

assert_inspect() {
  local desc="$1" image="$2" fmt="$3" expected="$4"
  TESTS_RUN=$((TESTS_RUN + 1))
  local result
  result=$(docker inspect --format="$fmt" "$image" 2>&1)
  if echo "$result" | grep -qF "$expected"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("$desc")
    printf "${RED}FAIL${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
    printf "     Expected: %s\n" "$expected"
    printf "     Got: %s\n" "$result"
  fi
}

skip_test() {
  local desc="$1" reason="$2"
  TESTS_RUN=$((TESTS_RUN + 1))
  TESTS_SKIPPED=$((TESTS_SKIPPED + 1))
  printf "${YELLOW}SKIP${NC} [%d] %s -- %s\n" "$TESTS_RUN" "$desc" "$reason"
}

print_summary() {
  echo ""
  echo "========================================="
  printf "  Tests run:    %d\n" "$TESTS_RUN"
  printf "  ${GREEN}Passed:     %d${NC}\n" "$TESTS_PASSED"
  printf "  ${RED}Failed:     %d${NC}\n" "$TESTS_FAILED"
  printf "  ${YELLOW}Skipped:    %d${NC}\n" "$TESTS_SKIPPED"
  echo "========================================="
  if [[ ${#FAILURES[@]} -gt 0 ]]; then
    echo ""
    printf "${RED}Failed tests:${NC}\n"
    for f in "${FAILURES[@]}"; do
      printf "  - %s\n" "$f"
    done
    echo ""
    return 1
  fi
  printf "\n${GREEN}All tests passed!${NC}\n"
  return 0
}
