#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_volumes.sh - Volume mount behavior

IMAGE="$STANDALONE_IMAGE"

# --- Input volume is readable ---
TESTS_RUN=$((TESTS_RUN + 1))
TEST_INPUT=$(mktemp -d)
echo "volume-test-content-12345" > "$TEST_INPUT/sample.txt"
RESULT=$(docker run --rm --entrypoint bash \
  -v "$TEST_INPUT:/workspace/input:ro" \
  "$IMAGE" -c "cat /workspace/input/sample.txt" 2>&1)
if [[ "$RESULT" == *"volume-test-content-12345"* ]]; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] Input volume is readable\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("Input volume is readable")
  printf "${RED}FAIL${NC} [%d] Input volume is readable (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
fi
rm -rf "$TEST_INPUT"

# --- Input volume can be mounted read-only when requested ---
TESTS_RUN=$((TESTS_RUN + 1))
TEST_INPUT=$(mktemp -d)
RESULT=$(docker run --rm --entrypoint bash \
  -v "$TEST_INPUT:/workspace/input:ro" \
  "$IMAGE" -c "touch /workspace/input/forbidden.txt 2>&1; echo EXIT_CODE=\$?" 2>&1)
if echo "$RESULT" | grep -qE "(Read-only|EXIT_CODE=1)"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] Input volume respects :ro when mounted RO\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("Input volume respects :ro when mounted RO")
  printf "${RED}FAIL${NC} [%d] Input volume respects :ro when mounted RO (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
fi
rm -rf "$TEST_INPUT"

# --- Default input bind is writable (proveo mounts without :ro) ---
TESTS_RUN=$((TESTS_RUN + 1))
TEST_INPUT=$(mktemp -d)
RESULT=$(docker run --rm --entrypoint bash \
  -v "$TEST_INPUT:/workspace/input" \
  "$IMAGE" -c "echo ok > /workspace/input/writable.txt && cat /workspace/input/writable.txt" 2>&1)
if [[ "$RESULT" == *"ok"* ]] && [[ -f "$TEST_INPUT/writable.txt" ]]; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] Input volume is writable when mounted RW\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("Input volume is writable when mounted RW")
  printf "${RED}FAIL${NC} [%d] Input volume is writable when mounted RW (output: %.200s)\n" "$TESTS_RUN" "$RESULT"
fi
rm -rf "$TEST_INPUT"

# --- Output volume is writable and persists to host ---
TESTS_RUN=$((TESTS_RUN + 1))
TEST_OUTPUT=$(mktemp -d)
docker run --rm --entrypoint bash \
  -v "$TEST_OUTPUT:/workspace/output:rw" \
  "$IMAGE" -c "echo 'output-data-67890' > /workspace/output/result.txt" 2>&1
if [[ -f "$TEST_OUTPUT/result.txt" ]] && grep -q "output-data-67890" "$TEST_OUTPUT/result.txt"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] Output volume is writable and persists to host\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("Output volume is writable and persists to host")
  printf "${RED}FAIL${NC} [%d] Output volume is writable and persists to host\n" "$TESTS_RUN"
fi
rm -rf "$TEST_OUTPUT"

# --- /workspace/temp is writable ---
assert_success \
  "/workspace/temp is writable by claude" \
  "$IMAGE" \
  "touch /workspace/temp/test-write && rm /workspace/temp/test-write"
