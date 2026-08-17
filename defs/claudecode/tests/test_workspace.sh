#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_workspace.sh - Directory structure and permissions

for image in $(images_to_test); do
  tag=$(image_tag "$image")

  # Directory existence
  for dir in /workspace /workspace/input /workspace/output \
             /workspace/data /workspace/temp /workspace/mcp-servers; do
    assert_success "[$tag] $dir exists" "$image" "test -d $dir"
  done

  # Ownership
  assert_output_contains \
    "[$tag] /workspace owned by claude" \
    "$image" \
    "stat -c '%U' /workspace" \
    "claude"

  # Permissions
  assert_output_matches \
    "[$tag] /workspace is 755" \
    "$image" \
    "stat -c '%a' /workspace" \
    "^755$"

  assert_output_matches \
    "[$tag] /workspace/input is 750" \
    "$image" \
    "stat -c '%a' /workspace/input" \
    "^750$"

  assert_output_matches \
    "[$tag] /workspace/data is 750" \
    "$image" \
    "stat -c '%a' /workspace/data" \
    "^750$"

  assert_output_matches \
    "[$tag] /workspace/output is 755" \
    "$image" \
    "stat -c '%a' /workspace/output" \
    "^755$"

  assert_output_matches \
    "[$tag] /workspace/temp is 755" \
    "$image" \
    "stat -c '%a' /workspace/temp" \
    "^755$"

  assert_output_matches \
    "[$tag] /workspace/mcp-servers is 755" \
    "$image" \
    "stat -c '%a' /workspace/mcp-servers" \
    "^755$"

  # WORKDIR
  assert_inspect \
    "[$tag] WORKDIR is /workspace" \
    "$image" \
    '{{.Config.WorkingDir}}' \
    "/workspace"

  # Home directory
  assert_output_contains \
    "[$tag] HOME is /home/claude" \
    "$image" \
    'echo $HOME' \
    "/home/claude"

  # Launch contract lives in the baked entrypoint (start-claude.sh was retired)
  assert_success \
    "[$tag] entrypoint.sh is baked and executable" \
    "$image" \
    "test -x /entrypoint.sh"

  assert_output_contains \
    "[$tag] entrypoint launches claude with --dangerously-skip-permissions" \
    "$image" \
    "cat /entrypoint.sh" \
    "exec claude --dangerously-skip-permissions"
done
