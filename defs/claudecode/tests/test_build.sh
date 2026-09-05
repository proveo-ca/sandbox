#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_build.sh - Image availability verification

# This phase used to BUILD the image it then tested, with a bare `docker build`
# and no BASE_IMAGE build-arg — so `ARG BASE_IMAGE=proveo/base-node-lsp:latest`,
# the Dockerfile's default, applied. That is the PUBLISHED base. Every run
# therefore threw away the local lineage `build.sh` and `ensure.sh` exist to
# establish, overwrote the operator's image with a registry-based one, and then
# asserted against the result.
#
# It surfaced as `bun: command not found`: bun was added to proveo/base-node after
# the last base-node-lsp publish, so a locally built claudecode had it and the one
# this phase substituted did not. Rebuilding by hand "fixed" it exactly until the
# next run of this suite clobbered it again — twice, before the mechanism was
# found. It is also the failure _spec/_devops/image-lineage-and-publish.puml is
# about, reached from inside the test suite rather than from sbx.
#
# opencode and cursor verify AVAILABILITY here and never build; claudecode now
# matches them. Building is build.sh's job, and it is the only caller that knows
# how to resolve the base chain: `proveo build claudecode`.
TESTS_RUN=$((TESTS_RUN + 1))
printf "Verifying image %s is available... " "$STANDALONE_IMAGE"
if docker image inspect "$STANDALONE_IMAGE" >/dev/null 2>&1 || docker pull "$STANDALONE_IMAGE" >/dev/null 2>&1; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] claudecode image is available\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("claudecode image is available")
  printf "${RED}FAIL${NC} [%d] claudecode image is available\n" "$TESTS_RUN"
  echo "FATAL: Cannot continue without the claudecode image — proveo build claudecode"
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
