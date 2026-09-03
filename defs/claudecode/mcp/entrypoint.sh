#!/usr/bin/env bash
# SPEC: _spec/defs/claudecode/claudecode-topology.puml, _spec/defs/claudecode/claudecode-egress-topology.puml, _spec/defs/claudecode/claudecode-paradigm.puml, _spec/defs/claudecode/chrome-bridge.puml
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
  set_working_directory "/app"
  load_env
  bridge_git_identity /app
  report_git_context /app
  attach_rtk
  run_smoke_test "claudecode"
  ensure_project_tools
fi

# Model bridges are declared in defs/bridges/claudecode.tsv.
set_working_directory "/app"
load_env quiet
ensure_git_safe_directory "$(pwd)"
scope_git_worktree "$(pwd)"

apply_model_bridges claudecode

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

if [[ -f AGENTS.md && ! -f CLAUDE.md ]]; then
  if printf '@AGENTS.md\n' > CLAUDE.md 2>/dev/null; then
    echo "🔗 CLAUDE.md → @AGENTS.md (Claude Code does not read AGENTS.md natively)"
  else
    echo "⚠️  Could not bridge AGENTS.md → CLAUDE.md (workspace may be read-only); continuing" >&2
  fi
fi

# Seed CLAUDE.md when missing (the workspace root is a RW bind by default).
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

# ── Compose user-level subagents (~/.claude/agents) ─────────
# Bodies are shared across harnesses; only the frontmatter is Claude Code's.
proveo_seed claudecode

# ── Claude in Chrome — add-on "claude-in-chrome" ──
# Started by proveo_seed above, which sets PROVEO_CHROME_READY=1 in this shell.

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

# Wire workspace LSP servers as an auto-loading Claude Code plugin (native LSP);
# the toolchains they need are provisioned first.
# Toolchain + LSP config live in proveo_seed above, so BOTH backends get them:
# sbx replaces this entrypoint entirely and would otherwise provision nothing.

# ── Agent evidence ─────────────────────────────────────────
claude_wants_stream_json() {
  local a
  for a in "$@"; do
    case "$a" in
      stream-json | --output-format=stream-json) return 0 ;;
    esac
  done
  return 1
}

CLAUDE_EVIDENCE_ARGS=()
if agent_evidence_verbose; then
  CLAUDE_EVIDENCE_ARGS+=(--verbose)
  if claude_wants_stream_json "$@"; then
    CLAUDE_EVIDENCE_ARGS+=(--include-partial-messages)
  fi
  report_agent_evidence "${CLAUDE_EVIDENCE_ARGS[@]}"
else
  report_agent_evidence
fi

# Two names for one intent, kept together: the older off switch and the
# documented 2.1.132+ opt-out that forces the classic renderer. The manifest's
# agentEnv is the delivery on both backends; these repeat it for a bare
# `docker run`. SPEC: _spec/defs/claudecode/claudecode-paradigm.puml
export CLAUDE_CODE_NO_FLICKER="${CLAUDE_CODE_NO_FLICKER:-0}"
export CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN="${CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN:-1}"

# --chrome is the only switch for this session; nothing is persisted (see
# proveo_chrome_bridge for why claudeInChromeDefaultEnabled stays untouched).
CLAUDE_CHROME_ARGS=()
[[ -n "${PROVEO_CHROME_READY:-}" ]] && CLAUDE_CHROME_ARGS=(--chrome)

echo "🚀 Launching Claude Code..."
proveo_exec_agent claude --dangerously-skip-permissions "${CLAUDE_EVIDENCE_ARGS[@]}" "${CLAUDE_CHROME_ARGS[@]}" -- "$@"
