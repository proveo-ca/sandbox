#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_tools.sh - Verify expected tools/runtimes are installed

TOOLS=(
  "opencode:opencode --version"
  "node:node --version"
  "npm:npm --version"
  "pnpm:timeout 10s pnpm --version"
  "git:git --version"
  "gh:gh --version"
  "curl:curl --version"
  "dumb-init:dumb-init --version"
)

for tool_entry in "${TOOLS[@]}"; do
  IFS=':' read -r name cmd <<< "$tool_entry"
  assert_success "$name is installed" "$IMAGE" "$cmd"
done

# Node major version: proveo/base-node ships Node 22 LTS
assert_output_matches \
  "node version is v22.x" \
  "$IMAGE" \
  "node --version" \
  "^v22\."

# opencode CLI exposes the `run` subcommand
assert_output_contains \
  "opencode CLI exposes 'run' subcommand" \
  "$IMAGE" \
  "opencode --help 2>&1" \
  "run"

# Browser variant (proveo/opencode-browser, FROM proveo/base-node-browser):
# Playwright's Chromium plus vercel-labs/agent-browser pointed at that same binary,
# and the discovery stub the seed drops into ~/.config/opencode/skills.
OPENCODE_BROWSER_IMAGE="${OPENCODE_BROWSER_IMAGE:-proveo/opencode-browser:latest}"
if docker image inspect "$OPENCODE_BROWSER_IMAGE" >/dev/null 2>&1; then
  assert_success "[browser] playwright CLI is installed" "$OPENCODE_BROWSER_IMAGE" "playwright --version"
  assert_success "[browser] agent-browser is installed" "$OPENCODE_BROWSER_IMAGE" "agent-browser --version"
  assert_success "[browser] agent-browser points at Playwright's Chromium (no second download)" "$OPENCODE_BROWSER_IMAGE" \
    'test -x "$AGENT_BROWSER_EXECUTABLE_PATH" && readlink -f "$AGENT_BROWSER_EXECUTABLE_PATH" | grep -q "^${PLAYWRIGHT_BROWSERS_PATH}/chromium-"'
  assert_output_contains "[browser] agent-browser serves its bundled skills" "$OPENCODE_BROWSER_IMAGE" "agent-browser skills list" "core"
  assert_output_contains "[browser] the seed drops the skill into ~/.config/opencode/skills" "$OPENCODE_BROWSER_IMAGE" \
    'export HOME=/tmp; source /entrypoint-lib.sh; proveo_seed_browser_skills opencode >/dev/null; head -2 /tmp/.config/opencode/skills/agent-browser/SKILL.md' \
    "name: agent-browser"
  assert_output_contains "[browser] agent-browser drives a headless Chromium as the image user" "$OPENCODE_BROWSER_IMAGE" \
    'export HOME=/tmp AGENT_BROWSER_SOCKET_DIR=/tmp/ab; mkdir -p /tmp/ab; agent-browser open about:blank >/dev/null && agent-browser get url; agent-browser close >/dev/null' \
    "about:blank"
  assert_failure "[browser] Claude in Chrome relay is claudecode's alone" "$OPENCODE_BROWSER_IMAGE" "test -f /opt/proveo/lib/chrome-bridge.js"
else
  skip_test "[browser] opencode-browser variant" "image $OPENCODE_BROWSER_IMAGE not built (./build.sh --browser)"
fi
