#!/usr/bin/env bash
# tests/test_config.sh - Claude configuration verification

# ==================== Standalone ====================

IMAGE="$STANDALONE_IMAGE"

# Config file existence
assert_success \
  "[standalone] ~/.claude.json exists" \
  "$IMAGE" \
  "test -f /home/claude/.claude.json"

assert_output_contains \
  "[standalone] ~/.claude.json owned by claude" \
  "$IMAGE" \
  "stat -c '%U' /home/claude/.claude.json" \
  "claude"

# Key config values
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

assert_output_contains \
  '[claudecode] permissions.allow pre-authorizes MCP tools' \
  "$IMAGE" \
  "cat /home/claude/.claude.json" \
  '"mcp__*"'

assert_output_contains \
  "[standalone] hasCompletedOnboarding=true" \
  "$IMAGE" \
  "cat /home/claude/.claude.json" \
  '"hasCompletedOnboarding": true'

# mcpServers should be empty
assert_output_matches \
  "[standalone] mcpServers is empty" \
  "$IMAGE" \
  "python3 -c \"import json; c=json.load(open('/home/claude/.claude.json')); print(len(c['projects']['/workspace']['mcpServers']))\"" \
  "^0$"

# settings.local.json
assert_success \
  "[standalone] ~/.claude/settings.local.json exists" \
  "$IMAGE" \
  "test -f /home/claude/.claude/settings.local.json"

assert_success \
  "[standalone] /workspace/.claude/settings.local.json exists" \
  "$IMAGE" \
  "test -f /workspace/.claude/settings.local.json"

assert_success \
  "[standalone] settings.local.json is valid JSON" \
  "$IMAGE" \
  "python3 -c \"import json; json.load(open('/home/claude/.claude/settings.local.json'))\""

# ==================== MCP ====================

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

  # MCP variant permits MCP tools but does not currently bake in a server.
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

  assert_output_contains \
    "[mcp] MCP wildcard permission is configured" \
    "$IMAGE" \
    "cat /home/claude/.claude.json" \
    'mcp__*'

  # MCP permissions in settings.local
  assert_output_contains \
    "[mcp] settings.local.json allows MCP wildcard" \
    "$IMAGE" \
    "cat /home/claude/.claude/settings.local.json" \
    'mcp__*'
fi
