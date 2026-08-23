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
# self-contained binary, so there is no node/pnpm/python/browser here.
assert_success "docker client is installed (docker via the sbx sandbox backend)" "$IMAGE" "docker --version"
assert_success "shared verification lib is baked" "$IMAGE" \
  'command -v proveo-entrypoint >/dev/null || test -f /opt/proveo/lib/detect-verify.sh'
