#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

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

# SPEC: _spec/_devops/agent-version-pin.puml
assert_inspect \
  "proveo.agent label names the agent package" \
  "$IMAGE" \
  '{{index .Config.Labels "proveo.agent"}}' \
  "cursor-agent"
AGENT_VERSION_LABEL="$(docker image inspect -f '{{index .Config.Labels "proveo.agent.version"}}' "$IMAGE" 2>/dev/null)"
if [[ -n "$AGENT_VERSION_LABEL" ]]; then
  assert_success \
    "installed cursor-agent release is proveo.agent.version=$AGENT_VERSION_LABEL" \
    "$IMAGE" \
    "test -d /opt/cursor-dist/.local/share/cursor-agent/versions/$AGENT_VERSION_LABEL"
else
  TESTS_RUN=$((TESTS_RUN + 1))
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("proveo.agent.version label is set")
  printf "${RED}FAIL${NC} [%d] proveo.agent.version label is set (image predates the pin — proveo build cursor)\n" "$TESTS_RUN"
fi

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
