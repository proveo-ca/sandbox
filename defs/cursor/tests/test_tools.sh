#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_tools.sh - Required CLI/runtime tools are installed

assert_success \
  "cursor cli (agent) is installed and reports a version" \
  "$IMAGE" \
  "agent --version"

assert_success \
  "legacy cursor-agent alias resolves" \
  "$IMAGE" \
  "command -v cursor-agent"

assert_output_matches \
  "agent binary lives under the root-owned dist prefix" \
  "$IMAGE" \
  "readlink -f /usr/local/bin/agent" \
  "^/opt/cursor-dist/"

assert_success "git is installed" "$IMAGE" "git --version"
assert_success "gh is installed" "$IMAGE" "gh --version"
# cursor is FROM proveo/base (no language runtime): the cursor-agent is a
# self-contained binary, so there is no node/pnpm/bun/python/browser here.
assert_failure "bun stays out of the runtime-free cursor image (it lives in proveo/base-node)" "$IMAGE" "command -v bun"
assert_success "docker client is installed (docker via the sbx sandbox backend)" "$IMAGE" "docker --version"
assert_success "shared verification lib is baked" "$IMAGE" \
  'command -v proveo-entrypoint >/dev/null || test -f /opt/proveo/lib/detect-verify.sh'

# Browser variant (proveo/cursor-browser, FROM proveo/base-node-browser):
# Playwright's Chromium plus vercel-labs/agent-browser pointed at that same binary,
# and the discovery stub the seed drops into ~/.cursor/skills.
CURSOR_BROWSER_IMAGE="${CURSOR_BROWSER_IMAGE:-proveo/cursor-browser:latest}"
if docker image inspect "$CURSOR_BROWSER_IMAGE" >/dev/null 2>&1; then
  assert_success "[browser] playwright CLI is installed" "$CURSOR_BROWSER_IMAGE" "playwright --version"
  assert_success "[browser] agent-browser is installed" "$CURSOR_BROWSER_IMAGE" "agent-browser --version"
  assert_output_contains "[browser] bun (from proveo/base-node) runs TypeScript" "$CURSOR_BROWSER_IMAGE" \
    "printf 'const n: number = 21; console.log(n * 2)' > /tmp/x.ts && bun /tmp/x.ts" "42"
  assert_success "[browser] agent-browser points at Playwright's Chromium (no second download)" "$CURSOR_BROWSER_IMAGE" \
    'test -x "$AGENT_BROWSER_EXECUTABLE_PATH" && readlink -f "$AGENT_BROWSER_EXECUTABLE_PATH" | grep -q "^${PLAYWRIGHT_BROWSERS_PATH}/chromium-"'
  assert_output_contains "[browser] agent-browser serves its bundled skills" "$CURSOR_BROWSER_IMAGE" "agent-browser skills list" "core"
  assert_output_contains "[browser] the seed drops the skill into ~/.cursor/skills" "$CURSOR_BROWSER_IMAGE" \
    'export HOME=/tmp; source /entrypoint-lib.sh; proveo_seed_browser_skills cursor >/dev/null; head -2 /tmp/.cursor/skills/agent-browser/SKILL.md' \
    "name: agent-browser"
  assert_output_contains "[browser] agent-browser drives a headless Chromium as the image user" "$CURSOR_BROWSER_IMAGE" \
    'export HOME=/tmp AGENT_BROWSER_SOCKET_DIR=/tmp/ab; mkdir -p /tmp/ab; agent-browser open about:blank >/dev/null && agent-browser get url; agent-browser close >/dev/null' \
    "about:blank"
  assert_failure "[browser] Claude in Chrome relay is claudecode's alone" "$CURSOR_BROWSER_IMAGE" "test -f /opt/proveo/lib/chrome-bridge.js"
else
  skip_test "[browser] cursor-browser variant" "image $CURSOR_BROWSER_IMAGE not built (./build.sh --browser)"
fi
