#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

WRAPPER_FILES=(
  "$PROJECT_ROOT/run.sh"
)

for wrapper in "${WRAPPER_FILES[@]}"; do
  name="${wrapper#$PROJECT_ROOT/}"

  TESTS_RUN=$((TESTS_RUN + 1))
  if grep -q 'exec "$PROVEO_BIN" run' "$wrapper" \
     && grep -q -- '--variant' "$wrapper"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] [%s] shims to proveo run with --variant\n" "$TESTS_RUN" "$name"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("[$name] shims to proveo run with --variant")
    printf "${RED}FAIL${NC} [%d] [%s] missing proveo run shim contract\n" "$TESTS_RUN" "$name"
  fi
done

TESTS_RUN=$((TESTS_RUN + 1))
if grep -q -- '--shell' "$PROJECT_ROOT/run.sh" \
   && grep -q 'case "$VARIANT" in' "$PROJECT_ROOT/run.sh"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] parent run.sh owns variant run and debug shell flows\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("parent run.sh owns variant run and debug shell flows")
  printf "${RED}FAIL${NC} [%d] parent run.sh missing consolidated run/debug contract\n" "$TESTS_RUN"
fi
