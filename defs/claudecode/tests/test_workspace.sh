#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

for image in $(images_to_test); do
  tag=$(image_tag "$image")

  for dir in /app /app/output /workspace \
             /workspace/data /workspace/temp /workspace/mcp-servers; do
    assert_success "[$tag] $dir exists" "$image" "test -d $dir"
  done

  assert_output_contains \
    "[$tag] /workspace owned by claude" \
    "$image" \
    "stat -c '%U' /workspace" \
    "claude"

  assert_output_matches \
    "[$tag] /workspace is 755" \
    "$image" \
    "stat -c '%a' /workspace" \
    "^755$"

  assert_output_matches \
    "[$tag] /app is 750" \
    "$image" \
    "stat -c '%a' /app" \
    "^750$"

  assert_output_matches \
    "[$tag] /workspace/data is 750" \
    "$image" \
    "stat -c '%a' /workspace/data" \
    "^750$"

  assert_output_matches \
    "[$tag] /app/output is 755" \
    "$image" \
    "stat -c '%a' /app/output" \
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

  assert_inspect \
    "[$tag] WORKDIR is /app" \
    "$image" \
    '{{.Config.WorkingDir}}' \
    "/app"

  assert_output_contains \
    "[$tag] HOME is /home/agent (the REAL home; /home/claude is the alias)" \
    "$image" \
    'echo $HOME' \
    "/home/agent"

  # SPEC: _spec/_paradigms/runtime-user-boundary.puml
  assert_output_contains \
    "[$tag] /home/claude resolves to /home/agent" \
    "$image" \
    'readlink -f /home/claude' \
    "/home/agent"

  assert_success \
    "[$tag] entrypoint.sh is baked and executable" \
    "$image" \
    "test -x /entrypoint.sh"

  assert_output_contains \
    "[$tag] entrypoint launches claude with --dangerously-skip-permissions" \
    "$image" \
    "cat /entrypoint.sh" \
    "proveo_exec_agent claude --dangerously-skip-permissions"
done
