#!/usr/bin/env bash
# SPEC: _spec/defs/claudecode/claudecode-topology.puml, _spec/defs/claudecode/claudecode-egress-topology.puml, _spec/defs/claudecode/claudecode.paradigm.md
# Thin entrypoint: shared prelude via proveo-entrypoint (or bash fallback), then seed + exec.
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=claudecode
  proveo-entrypoint prep claudecode || true
else
  ensure_runtime_user
  set_working_directory "/workspace"
  load_env
  bridge_git_identity /workspace/input
  report_git_context /workspace/input
  attach_rtk
  run_smoke_test "claudecode"
  ensure_project_tools
fi

# ── Verification command discovery ────────────────────────
# Prefer Go proveo-entrypoint verify; fall back to thin detect-verify.sh wrapper.
if command -v proveo-entrypoint >/dev/null 2>&1; then
  echo "── Verification Commands ────────────────────────────"
  proveo-entrypoint verify "$(pwd)" | while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    printf '  %s\n' "$line"
  done
  echo "─────────────────────────────────────────────────────"
elif [[ -f /opt/proveo/lib/detect-verify.sh ]]; then
  # shellcheck source=/dev/null
  source /opt/proveo/lib/detect-verify.sh
  echo "── Verification Commands ────────────────────────────"
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    printf '  %s\n' "$line"
  done < <(detect_verify_commands "$(pwd)")
  echo "─────────────────────────────────────────────────────"
fi

# Seed CLAUDE.md when missing (input is a RW bind by default for input-output).
if [[ -f /opt/claudecode/defaults/CLAUDE.md && ! -f CLAUDE.md ]]; then
  if cp /opt/claudecode/defaults/CLAUDE.md CLAUDE.md 2>/dev/null; then
    echo "🌱 Seeded CLAUDE.md into workspace"
  else
    echo "⚠️  Could not seed CLAUDE.md (workspace may be read-only); continuing" >&2
  fi
fi

# When proveo mounts ~/.proveo at /proveo-home and sets HOME, seed Claude config
# from the image bake into the durable home (missing-only).
seed_claude_proveo_home() {
  mkdir -p "${HOME}/.claude"
  if [[ -f /home/claude/.claude.json && ! -f "${HOME}/.claude.json" ]]; then
    cp /home/claude/.claude.json "${HOME}/.claude.json"
    echo "🌱 Seeded \$HOME/.claude.json into proveo home"
  fi
  if [[ -f /home/claude/.claude/settings.local.json && ! -f "${HOME}/.claude/settings.local.json" ]]; then
    cp /home/claude/.claude/settings.local.json "${HOME}/.claude/settings.local.json"
    echo "🌱 Seeded \$HOME/.claude/settings.local.json into proveo home"
  fi
}
seed_claude_proveo_home

echo "Paradigm: ML blackbox algorithm (spec → plan → verify loop)"
[[ -n "${ENFORCEMENT_PROXY:-}" ]] && echo "🛡️  Enforcement proxy: ${ENFORCEMENT_PROXY}"
[[ -n "${INSPECT_PROXY:-}" && "${INSPECT_PROXY}" != "${ENFORCEMENT_PROXY:-}" ]] && echo "🔍  Inspection proxy: ${INSPECT_PROXY}"
[[ -n "${PROVEO_LOCAL_MODEL:-}" ]] && echo "🧠  Local model: ${PROVEO_LOCAL_MODEL}"

echo "🚀 Launching Claude Code..."
# Wire workspace LSP servers as an auto-loading Claude Code plugin (native LSP).
configure_claude_lsp

exec claude --dangerously-skip-permissions "$@"
