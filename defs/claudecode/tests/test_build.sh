#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_build.sh - Image build verification

# The Dockerfile requires the agent version as a build-arg (a bare `docker build`
# fails on purpose — see _spec/_devops/agent-version-pin.puml), so this suite
# resolves it the same way build.sh does. CLAUDE_CODE_VERSION exported wins.
# shellcheck source=../../lib/docker-build.sh
source "$PROJECT_ROOT/../lib/docker-build.sh"
CLAUDE_CODE_VERSION="$(proveo_agent_version CLAUDE_CODE_VERSION npm @anthropic-ai/claude-code)"

TESTS_RUN=$((TESTS_RUN + 1))
printf "Building claudecode image... "
if docker build -t "$STANDALONE_IMAGE" --build-arg CLAUDE_CODE_VERSION="$CLAUDE_CODE_VERSION" \
     -f "$PROJECT_ROOT/mcp/Dockerfile" "$PROJECT_ROOT/../.." 2>&1; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] claudecode image builds successfully\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("claudecode image builds successfully")
  printf "${RED}FAIL${NC} [%d] claudecode image builds successfully\n" "$TESTS_RUN"
  echo "FATAL: Cannot continue without the claudecode image."
  print_summary
  exit 1
fi

# Cover the mcp variant too when its image is available locally (build.sh
# builds both); downstream phases key off this flag.
if docker image inspect "$MCP_IMAGE" >/dev/null 2>&1; then
  MCP_IMAGE_AVAILABLE=true
fi

# --- Verify Docker labels ---
assert_inspect \
  "[claudecode] has security.non-root=true label" \
  "$STANDALONE_IMAGE" \
  '{{index .Config.Labels "security.non-root"}}' \
  "true"

assert_inspect \
  "[claudecode] has security.hardened=true label" \
  "$STANDALONE_IMAGE" \
  '{{index .Config.Labels "security.hardened"}}' \
  "true"

if $MCP_IMAGE_AVAILABLE; then
  assert_inspect \
    "[mcp] has security.non-root=true label" \
    "$MCP_IMAGE" \
    '{{index .Config.Labels "security.non-root"}}' \
    "true"

  assert_inspect \
    "[mcp] has security.hardened=true label" \
    "$MCP_IMAGE" \
    '{{index .Config.Labels "security.hardened"}}' \
    "true"
fi

# The image says which agent it carries, and the label is the truth: the version
# build.sh pinned is the one the installed CLI reports. An image without the label
# predates the pin — rebuild it (proveo build claudecode) rather than trusting it.
# SPEC: _spec/_devops/agent-version-pin.puml
assert_inspect \
  "[claudecode] proveo.agent label names the agent package" \
  "$STANDALONE_IMAGE" \
  '{{index .Config.Labels "proveo.agent"}}' \
  "@anthropic-ai/claude-code"
AGENT_VERSION_LABEL="$(docker image inspect -f '{{index .Config.Labels "proveo.agent.version"}}' "$STANDALONE_IMAGE" 2>/dev/null)"
if [[ -n "$AGENT_VERSION_LABEL" ]]; then
  assert_output_contains \
    "[claudecode] claude --version matches proveo.agent.version=$AGENT_VERSION_LABEL" \
    "$STANDALONE_IMAGE" \
    "claude --version" \
    "$AGENT_VERSION_LABEL"
else
  TESTS_RUN=$((TESTS_RUN + 1))
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("[claudecode] proveo.agent.version label is set")
  printf "${RED}FAIL${NC} [%d] [claudecode] proveo.agent.version label is set (image predates the pin — proveo build claudecode)\n" "$TESTS_RUN"
fi
