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
