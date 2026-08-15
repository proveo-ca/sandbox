#!/usr/bin/env bash
# SPEC: _spec/defs/claudecode/claudecode-topology.puml, _spec/defs/claudecode/claudecode-egress-topology.puml, _spec/defs/claudecode/claudecode-paradigm.puml
# Thin entrypoint: shared prelude via proveo-entrypoint (or bash fallback), then seed + exec.
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=claudecode
  env PROVEO_SMOKE_TEST= proveo-entrypoint prep claudecode || true
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

# ── Model aliases → Claude Code env (CODING_HARNESSES.md) ──
set_working_directory "/workspace"
load_env quiet
ensure_git_safe_directory "$(pwd)"
scope_git_worktree "$(pwd)"

bare_model() { printf '%s' "${1##*/}"; }

if [[ -z "${ANTHROPIC_MODEL:-}" ]]; then
  for candidate in "${ARCHITECT_MODEL:-}" "${EDITOR_MODEL:-}"; do
    if [[ -n "$candidate" ]]; then
      ANTHROPIC_MODEL="$(bare_model "$candidate")"
      export ANTHROPIC_MODEL
      break
    fi
  done
fi

if [[ -z "${ANTHROPIC_SMALL_FAST_MODEL:-}" && -n "${SMALL_MODEL:-}" ]]; then
  ANTHROPIC_SMALL_FAST_MODEL="$(bare_model "$SMALL_MODEL")"
  export ANTHROPIC_SMALL_FAST_MODEL
fi

printf 'PROVEO_MODELS main=%s small=%s\n' \
  "${ANTHROPIC_MODEL:-unset}" "${ANTHROPIC_SMALL_FAST_MODEL:-unset}"

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

# Claude Code reads CLAUDE.md and NOT AGENTS.md, so a project carrying the shared
# standard is otherwise invisible to it. Bridge with the documented @import rather
# than copying: the import is re-read each run, a copy would go stale, and a
# symlink needs Administrator on Windows.
if [[ -f AGENTS.md && ! -f CLAUDE.md ]]; then
  if printf '@AGENTS.md\n' > CLAUDE.md 2>/dev/null; then
    echo "🔗 CLAUDE.md → @AGENTS.md (Claude Code does not read AGENTS.md natively)"
  else
    echo "⚠️  Could not bridge AGENTS.md → CLAUDE.md (workspace may be read-only); continuing" >&2
  fi
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

# ── Seed user-level subagents (~/.claude/agents) ────────────
# Claude Code reads user-level agents from $HOME/.claude/agents and project-level
# ones from .claude/agents in the workspace; the workspace is never written here.
# Missing-only, so a durable proveo home keeps local edits, unless
# CLAUDECODE_RESEED=1 forces a refresh from the image bake.
seed_claude_subagents() {
  local src="/opt/claudecode/defaults/agents"
  local dst="${HOME}/.claude/agents"
  [[ -d "$src" ]] || return 0
  # set -e is on: a read-only home must degrade to "no subagents", not kill the run.
  mkdir -p "$dst" 2>/dev/null || { echo "⚠️  Could not seed subagents into $dst; continuing" >&2; return 0; }

  local seeded=()
  for f in "$src"/*.md; do
    [[ -e "$f" ]] || continue
    local name; name="$(basename "$f")"
    if [[ "${CLAUDECODE_RESEED:-0}" == "1" || ! -f "$dst/$name" ]]; then
      if cp -f "$f" "$dst/$name" 2>/dev/null; then
        seeded+=("agents/$name")
      fi
    fi
  done
  if (( ${#seeded[@]} > 0 )); then
    echo "🌱 Seeded Claude Code subagents into $dst: ${seeded[*]}"
  fi
}
seed_claude_subagents

# Surface available subagents (user + project)
agent_files=()
[[ -d "${HOME}/.claude/agents" ]] && \
  while IFS= read -r f; do agent_files+=("$(basename "${f%.md}")"); done \
  < <(find "${HOME}/.claude/agents" -maxdepth 1 -name '*.md' 2>/dev/null | sort)
[[ -d .claude/agents ]] && \
  while IFS= read -r f; do agent_files+=("$(basename "${f%.md}") (project)"); done \
  < <(find .claude/agents -maxdepth 1 -name '*.md' 2>/dev/null | sort)
if (( ${#agent_files[@]} > 0 )); then
  echo "🧑‍💻 Subagents available: ${agent_files[*]}"
fi

echo "Paradigm: ML blackbox algorithm (spec → plan → verify loop)"
[[ -n "${ENFORCEMENT_PROXY:-}" ]] && echo "🛡️  Enforcement proxy: ${ENFORCEMENT_PROXY}"
[[ -n "${INSPECT_PROXY:-}" && "${INSPECT_PROXY}" != "${ENFORCEMENT_PROXY:-}" ]] && echo "🔍  Inspection proxy: ${INSPECT_PROXY}"
[[ -n "${PROVEO_LOCAL_MODEL:-}" ]] && echo "🧠  Local model: ${PROVEO_LOCAL_MODEL}"

run_smoke_test "claudecode"

echo "🚀 Launching Claude Code..."
ensure_language_servers "$(pwd)"
configure_claude_lsp

exec claude --dangerously-skip-permissions "$@"
