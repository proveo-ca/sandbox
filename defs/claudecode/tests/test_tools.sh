#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

TOOLS=(
  "claude:claude --help"
  "node:node --version"
  "npm:npm --version"
  "pnpm:pnpm --version"
  "bun:bun --version"
  "bunx:bunx --version"
  "git:git --version"
  "gh:gh --version"
  "python3:python3 --version"
  "pip3:pip3 --version"
  "curl:curl --version"
  "wget:wget --version"
  "dumb-init:dumb-init --version"
)

SOL_TOOLS=(
  "solhint:solhint --version"
  "semgrep:semgrep --version"
  "solc-select:solc-select versions"
  "solc:solc --version"
  "forge:forge --version"
  "cast:cast --version"
)

# SPEC: _spec/_plans/image-size-reduction.puml
SOL_ABSENT=(anvil chisel)

for image in $(images_to_test); do
  tag=$(image_tag "$image")
  for tool_entry in "${TOOLS[@]}"; do
    IFS=':' read -r name cmd <<< "$tool_entry"
    assert_success "[$tag] $name is installed" "$image" "$cmd"
  done
  for tool_entry in "${SOL_TOOLS[@]}"; do
    IFS=':' read -r name cmd <<< "$tool_entry"
    assert_failure "[$tag] $name stays out of the base variant (sol-only)" "$image" "command -v $name"
  done
  for name in "${SOL_ABSENT[@]}"; do
    assert_failure "[$tag] $name is in no variant at all" "$image" "command -v $name"
  done
done

for image in $(images_to_test); do
  assert_output_contains "[$(image_tag "$image")] bun executes a TypeScript file without a build step" "$image" \
    "printf 'const n: number = 21; console.log(n * 2)' > /tmp/x.ts && bun /tmp/x.ts" "42"
done

SOL_IMAGE="$(proveo_resolve_image "${SOL_IMAGE:-proveo/claudecode-solidity:latest}")"
if docker image inspect "$SOL_IMAGE" >/dev/null 2>&1; then
  for tool_entry in "${TOOLS[@]}" "${SOL_TOOLS[@]}"; do
    IFS=':' read -r name cmd <<< "$tool_entry"
    assert_success "[sol] $name is installed" "$SOL_IMAGE" "$cmd"
  done
  for name in "${SOL_ABSENT[@]}"; do
    assert_failure "[sol] $name is removed after foundryup (devnet/REPL, no chain and no prompt here)" \
      "$SOL_IMAGE" "command -v $name"
  done
fi

if $MCP_IMAGE_AVAILABLE; then
  assert_success \
    "[mcp] MCP server directory exists" \
    "$MCP_IMAGE" \
    "test -d /workspace/mcp-servers"
fi

BROWSER_IMAGE="$(proveo_resolve_image "${BROWSER_IMAGE:-proveo/claudecode-browser:latest}")"
if docker image inspect "$BROWSER_IMAGE" >/dev/null 2>&1; then
  assert_success "[browser] playwright CLI is installed" "$BROWSER_IMAGE" "playwright --version"
  assert_success "[browser] agent-browser is installed" "$BROWSER_IMAGE" "agent-browser --version"
  assert_success "[browser] agent-browser points at Playwright's Chromium (no second download)" "$BROWSER_IMAGE" \
    'test -x "$AGENT_BROWSER_EXECUTABLE_PATH" && readlink -f "$AGENT_BROWSER_EXECUTABLE_PATH" | grep -q "^${PLAYWRIGHT_BROWSERS_PATH}/chromium-"'
  assert_output_contains "[browser] agent-browser serves its bundled skills" "$BROWSER_IMAGE" "agent-browser skills list" "core"
  assert_success "[browser] agent-browser skill stub is baked for the seed" "$BROWSER_IMAGE" "test -f /opt/proveo/skills/agent-browser/SKILL.md"
  assert_output_contains "[browser] the seed drops the skill into ~/.claude/skills" "$BROWSER_IMAGE" \
    'export HOME=/tmp; source /entrypoint-lib.sh; proveo_seed_browser_skills claudecode >/dev/null; head -2 /tmp/.claude/skills/agent-browser/SKILL.md' \
    "name: agent-browser"
  assert_output_contains "[browser] agent-browser drives a headless Chromium as the image user" "$BROWSER_IMAGE" \
    'export HOME=/tmp AGENT_BROWSER_SOCKET_DIR=/tmp/ab; mkdir -p /tmp/ab; agent-browser open about:blank >/dev/null && agent-browser get url; agent-browser close >/dev/null' \
    "about:blank"
else
  skip_test "[browser] claudecode-browser variant" "image $BROWSER_IMAGE not built (./build.sh --browser)"
fi
