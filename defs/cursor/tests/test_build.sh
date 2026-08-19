#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_build.sh - Image availability verification

TESTS_RUN=$((TESTS_RUN + 1))
printf "Verifying image %s is available... " "$IMAGE"
if docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull "$IMAGE" >/dev/null 2>&1; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] image is available\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("image is available")
  printf "${RED}FAIL${NC} [%d] image is available\n" "$TESTS_RUN"
  echo "FATAL: Cannot continue without image."
  print_summary
  exit 1
fi

assert_inspect \
  "has security.non-root=true label" \
  "$IMAGE" \
  '{{index .Config.Labels "security.non-root"}}' \
  "true"

assert_inspect \
  "has security.hardened=true label" \
  "$IMAGE" \
  '{{index .Config.Labels "security.hardened"}}' \
  "true"

assert_inspect \
  "Docker USER is non-root (cursor)" \
  "$IMAGE" \
  '{{.Config.User}}' \
  "cursor"

assert_inspect \
  "entrypoint uses dumb-init" \
  "$IMAGE" \
  '{{json .Config.Entrypoint}}' \
  "dumb-init"
