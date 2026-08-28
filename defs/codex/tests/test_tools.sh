#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_tools.sh - Required CLI/runtime tools are installed

assert_success \
  "codex cli is installed and reports a version" \
  "$IMAGE" \
  "codex --version"

assert_success "git is installed" "$IMAGE" "git --version"
assert_success "gh is installed" "$IMAGE" "gh --version"
assert_success "node is installed" "$IMAGE" "node --version"
assert_success "docker client is installed (docker via the sbx sandbox backend)" "$IMAGE" "docker --version"

# The language servers arrive from proveo/base-node-lsp and reach codex as MCP
# servers; without the bridge binary the entrypoint wires nothing at all.
assert_success \
  "mcp-language-server bridge is baked (LSP reaches codex over MCP)" \
  "$IMAGE" \
  "command -v mcp-language-server"

assert_success "shared verification lib is baked" "$IMAGE" \
  'command -v proveo-entrypoint >/dev/null || test -f /opt/proveo/lib/detect-verify.sh'

assert_success "shared subagent frontmatter is baked" "$IMAGE" \
  "test -d /opt/proveo/subagents/_frontmatter/codex"

assert_success "model bridge table is baked" "$IMAGE" \
  "test -f /opt/proveo/bridges/codex.tsv"
