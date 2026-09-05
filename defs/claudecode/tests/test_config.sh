#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

IMAGE="$STANDALONE_IMAGE"

assert_success \
  "[standalone] ~/.claude.json exists" \
  "$IMAGE" \
  "test -f /home/claude/.claude.json"

assert_output_contains \
  "[standalone] ~/.claude.json owned by claude" \
  "$IMAGE" \
  "stat -c '%U' /home/claude/.claude.json" \
  "claude"

assert_output_contains \
  "[standalone] dangerouslySkipPermissions=true" \
  "$IMAGE" \
  "cat /home/claude/.claude.json" \
  '"dangerouslySkipPermissions": true'

assert_output_contains \
  "[standalone] autoTrustNewProjects=true" \
  "$IMAGE" \
  "cat /home/claude/.claude.json" \
  '"autoTrustNewProjects": true'

assert_output_contains \
  "[standalone] has /workspace project" \
  "$IMAGE" \
  "cat /home/claude/.claude.json" \
  '"/workspace"'

assert_output_contains \
  '[claudecode] permissions.allow pre-authorizes Bash' \
  "$IMAGE" \
  "cat /home/claude/.claude.json" \
  '"Bash"'

TESTS_RUN=$((TESTS_RUN + 1))
BAD_RULES=$(run_timeout 30s docker run --rm --entrypoint bash "$IMAGE" -c \
  'cat /home/claude/.claude.json /home/claude/.claude/settings.local.json /app/.claude/settings.local.json 2>/dev/null' | grep -c 'mcp__\*' || true)
if [[ "$BAD_RULES" == "0" ]]; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] [claudecode] no wildcard-only MCP allow rule in any seeded settings\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("no wildcard-only MCP allow rule in any seeded settings")
  printf "${RED}FAIL${NC} [%d] [claudecode] found %s 'mcp__*' rule(s) — Claude Code rejects them at startup\n" "$TESTS_RUN" "$BAD_RULES"
fi

assert_output_contains \
  "[standalone] hasCompletedOnboarding=true" \
  "$IMAGE" \
  "cat /home/claude/.claude.json" \
  '"hasCompletedOnboarding": true'

assert_output_matches \
  "[standalone] mcpServers is empty" \
  "$IMAGE" \
  "python3 -c \"import json; c=json.load(open('/home/claude/.claude.json')); print(len(c['projects']['/workspace']['mcpServers']))\"" \
  "^0$"

assert_success \
  "[standalone] ~/.claude/settings.local.json exists" \
  "$IMAGE" \
  "test -f /home/claude/.claude/settings.local.json"

assert_success \
  "[standalone] /app/.claude/settings.local.json exists" \
  "$IMAGE" \
  "test -f /app/.claude/settings.local.json"

assert_success \
  "[standalone] settings.local.json is valid JSON" \
  "$IMAGE" \
  "python3 -c \"import json; json.load(open('/home/claude/.claude/settings.local.json'))\""

# SPEC: _spec/defs/agent-definition-sharing.puml
assert_success \
  "[standalone] every subagent in the claudecode roster is baked in" \
  "$IMAGE" \
  'set -eu
   roster="$(jq -r ".claudecode[]" /opt/proveo/subagents/_roster.json)"
   [ -n "$roster" ] || { echo "claudecode roster is empty"; exit 1; }
   for a in $roster; do
     test -f "/opt/proveo/subagents/$a.md" || { echo "missing /opt/proveo/subagents/$a.md"; exit 1; }
   done'

FRONTMATTER=/opt/proveo/subagents/_frontmatter/claudecode
assert_success \
  "[standalone] claudecode subagent frontmatter is present" \
  "$IMAGE" \
  "ls ${FRONTMATTER}/*.yaml >/dev/null 2>&1 && grep -lq '^tools:' ${FRONTMATTER}/*.yaml"

assert_failure \
  "[standalone] no subagent grants Bash" \
  "$IMAGE" \
  "grep -lE '^tools:.*Bash' ${FRONTMATTER}/*.yaml | grep -q ."

assert_failure \
  "[standalone] only spec-keeper may write" \
  "$IMAGE" \
  "grep -lE '^tools:.*(Edit|Write)' ${FRONTMATTER}/*.yaml \
   | grep -v spec-keeper.yaml | grep -q ."

assert_output_contains \
  "[standalone] render_subagents seeds the roster into \$HOME/.claude/agents" \
  "$IMAGE" \
  "export HOME=/tmp/seedhome && mkdir -p \$HOME \
   && source /entrypoint-lib.sh \
   && render_subagents claudecode \"\$HOME/.claude/agents\" >/dev/null \
   && ls \$HOME/.claude/agents | tr '\n' ' '" \
  "adversarial-reviewer.md architect.md monorepo-coordinator.md security-reviewer.md spec-keeper.md"

assert_output_contains \
  "[standalone] CLAUDE.md wires the review gates" \
  "$IMAGE" \
  "cat /opt/claudecode/defaults/CLAUDE.md" \
  "Review Gates"

if $MCP_IMAGE_AVAILABLE; then
  IMAGE="$MCP_IMAGE"

  assert_success \
    "[mcp] ~/.claude.json exists" \
    "$IMAGE" \
    "test -f /home/claude/.claude.json"

  assert_output_contains \
    "[mcp] dangerouslySkipPermissions=true" \
    "$IMAGE" \
    "cat /home/claude/.claude.json" \
    '"dangerouslySkipPermissions": true'

  assert_output_contains \
    "[mcp] hasCompletedOnboarding=true" \
    "$IMAGE" \
    "cat /home/claude/.claude.json" \
    '"hasCompletedOnboarding": true'

  assert_output_contains \
    "[mcp] mcpServers is present" \
    "$IMAGE" \
    "cat /home/claude/.claude.json" \
    '"mcpServers"'

  assert_output_contains \
    "[mcp] mcpServers is empty" \
    "$IMAGE" \
    "cat /home/claude/.claude.json" \
    '"mcpServers": {}'
fi
