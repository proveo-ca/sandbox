#!/usr/bin/env bash
# SPEC: _spec/defs/codex/codex-topology.puml, _spec/defs/codex/codex-paradigm.puml
# Thin entrypoint: shared prelude via proveo-entrypoint (or bash fallback), then seed + exec.
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=codex
  env PROVEO_SMOKE_TEST= proveo-entrypoint prep codex || true
else
  ensure_runtime_user
  set_working_directory "/app"
  load_env
  bridge_git_identity /app
  report_git_context /app
  attach_rtk
  ensure_project_tools
fi

set_working_directory "/app"
load_env quiet
ensure_git_safe_directory "$(pwd)"
scope_git_worktree "$(pwd)"

# Model bridges are declared in defs/bridges/codex.tsv.
apply_model_bridges codex

printf 'PROVEO_MODELS main=%s\n' "${CODEX_MODEL:-unset}"

# $CODEX_HOME is resolved ONCE, here, from the home proveo_seed itself writes to —
# not from $HOME directly, and not from an inherited value.
#
# The three could disagree. proveo_seed resolves its destination through
# _proveo_agent_home, which prefers PROVEO_HOME: under sbx a setup command runs as
# `user: "1000"`, which resets HOME from /etc/passwd, so the seed's home and the
# process's are not always the same one. A CODEX_HOME pointing anywhere else finds
# none of the config, house rules or subagents that were just written — a session
# that comes up with no setup at all and no error to explain it.
_codex_home="$(_proveo_agent_home)/.codex"
if [[ -n "${CODEX_HOME:-}" && "${CODEX_HOME}" != "$_codex_home" ]]; then
  echo "ℹ️  CODEX_HOME=${CODEX_HOME} overridden to ${_codex_home} — the seed writes"
  echo "   config, house rules and subagents there, and a split home reads none of them"
fi
export CODEX_HOME="$_codex_home"

# ── Seed user-level defaults ($CODEX_HOME) ─────────────────
# Missing-only, unless CODEX_RESEED=1 forces a re-seed. The mounted workspace is
# never touched: codex reads AGENTS.md natively, so there is no bridge file to
# write into the operator's checkout.
seed_codex_defaults() {
  local src=/opt/codex/defaults dst="$CODEX_HOME"
  mkdir -p "$dst" 2>/dev/null || { echo "⚠️  Could not create $dst; continuing" >&2; return 0; }

  if [[ -f "$src/config.toml" ]]; then
    if [[ "${CODEX_RESEED:-0}" == "1" ]]; then
      cp -f "$src/config.toml" "$dst/config.toml" \
        && echo "🔁 CODEX_RESEED=1 — re-seeded $dst/config.toml from the baked defaults"
    elif [[ ! -f "$dst/config.toml" ]]; then
      cp "$src/config.toml" "$dst/config.toml" \
        && echo "🌱 Seeded config.toml into $dst"
    fi
  fi
}
seed_codex_defaults

# ── Compose user-level subagents ($CODEX_HOME/agents) ──────
# Bodies are shared across harnesses; only the frontmatter is Codex's TOML schema.
# This also installs the house rules and provisions the workspace toolchain, and
# it is the SAME function the sbx Kit calls from setup.startup — so both backends
# get the identical files.
proveo_seed codex

# ── LSP code intelligence via mcp-language-server ──────────
# Codex has no external-LSP config, but it does speak MCP, so the language servers
# arrive as MCP servers — the same bridge cursor uses, written into config.toml as
# a marked block so an operator's own [mcp_servers.*] entries survive a re-run.
_codex_toml_str() { printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"; }

configure_codex_lsp_mcp() {
  command -v mcp-language-server >/dev/null 2>&1 || return 0
  local cfg="$CODEX_HOME/config.toml" langs=() block="" line lang server i n
  local -a f extra
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    # detect_workspace_lsps prints: lang|count|server|server-arg...|extension-csv
    # The trailing CSV maps extensions to a language for editors; MCP has no use
    # for it, so it is dropped rather than passed to the server as an argument.
    IFS='|' read -ra f <<< "$line"
    n=${#f[@]}
    (( n >= 4 )) || continue
    lang="${f[0]}"; server="${f[2]}"
    extra=()
    for (( i = 3; i < n - 1; i++ )); do extra+=("${f[$i]}"); done
    langs+=("$lang")

    block+="[mcp_servers.lsp_${lang}]"$'\n'
    block+="command = \"mcp-language-server\""$'\n'
    block+="args = [$(_codex_toml_str --workspace), $(_codex_toml_str "$(pwd)")"
    block+=", $(_codex_toml_str --lsp), $(_codex_toml_str "$server")"
    if (( ${#extra[@]} > 0 )); then
      block+=", $(_codex_toml_str --)"
      for i in "${extra[@]}"; do block+=", $(_codex_toml_str "$i")"; done
    fi
    block+="]"$'\n'$'\n'
  done < <(detect_workspace_lsps "$(pwd)")

  (( ${#langs[@]} > 0 )) || return 0
  mkdir -p "$CODEX_HOME" 2>/dev/null || return 0
  touch "$cfg" 2>/dev/null || return 0
  # A MARKED block, appended: the operator's own [mcp_servers.*] entries and every
  # other table survive a re-run, and only what proveo generated is replaced.
  if printf '%s' "$block" | _proveo_write_block "$cfg" \
       "# >>> proveo lsp (generated — edits are overwritten) >>>" \
       "# <<< proveo lsp <<<"; then
    echo "🧠 LSP code intelligence (Codex MCP servers): ${langs[*]}"
  fi
}
configure_codex_lsp_mcp

# ── Verification command discovery ────────────────────────
# Prefer Go proveo-entrypoint verify; fall back to the thin detect-verify.sh wrapper.
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

echo "codex cli version:  $(command_version_opencode codex unknown --version)"
echo "Paradigm: ML blackbox algorithm (spec → plan → verify loop)"
echo "node version:       $(command_version_opencode node unknown --version)"
echo "pnpm version:       $(command_version_opencode pnpm n/a -v)"

# ── Steering files: detect and report, never write ─────────
# Codex reads AGENTS.md natively, so unlike claudecode there is nothing to bridge
# into the operator's checkout — the repo's own file is already the project layer,
# and it outranks the house rules proveo installs at the user layer.
echo "── Steering Files ───────────────────────────────────"
if [[ -f AGENTS.md ]]; then
  echo "✅ Found AGENTS.md (project layer — read natively, outranks \$CODEX_HOME/AGENTS.md)"
elif [[ -f CLAUDE.md ]]; then
  echo "✅ Found CLAUDE.md (read via project_doc_fallback_filenames)"
else
  echo "🔎 No project steering file; run /init inside codex to write one"
fi
[[ -f "$CODEX_HOME/AGENTS.md" ]] && echo "✅ House rules: \$CODEX_HOME/AGENTS.md (user layer)"
echo "─────────────────────────────────────────────────────"

# Surface available subagents (user + project)
agent_files=()
[[ -d "$CODEX_HOME/agents" ]] && \
  while IFS= read -r f; do agent_files+=("$(basename "${f%.toml}")"); done \
  < <(find "$CODEX_HOME/agents" -maxdepth 1 -name '*.toml' 2>/dev/null | sort)
[[ -d .codex/agents ]] && \
  while IFS= read -r f; do agent_files+=("$(basename "${f%.toml}") (project)"); done \
  < <(find .codex/agents -maxdepth 1 -name '*.toml' 2>/dev/null | sort)
if (( ${#agent_files[@]} > 0 )); then
  echo "🧑‍💻 Subagents available: ${agent_files[*]}"
fi

[[ -n "${ENFORCEMENT_PROXY:-}" ]] && echo "🛡️  Enforcement proxy: ${ENFORCEMENT_PROXY}"
[[ -n "${INSPECT_PROXY:-}" && "${INSPECT_PROXY}" != "${ENFORCEMENT_PROXY:-}" ]] && echo "🔍  Inspection proxy: ${INSPECT_PROXY}"
[[ -n "${PROVEO_LOCAL_MODEL:-}" ]] && echo "🧠  Local model: ${PROVEO_LOCAL_MODEL}"

# ── Auth check ─────────────────────────────────────────────
# Two ways to be authenticated: a ChatGPT-plan login persisted at
# $CODEX_HOME/auth.json (which the durable proveo home carries between runs), or
# OPENAI_API_KEY. The login wins when both are present — codex's own default —
# and proveo suppresses the competing key host-side for the same reason.
if [[ -z "${OPENAI_API_KEY:-}" && ! -f "$CODEX_HOME/auth.json" ]]; then
  echo "⚠️  No credential found. Either export OPENAI_API_KEY (platform.openai.com/api-keys),"
  echo "   or run 'codex login' once — the login persists under proveo home"
  echo "   (~/.proveo/.codex → \$CODEX_HOME) and is reused by later runs."
fi

run_smoke_test "codex"

# ── Launch ─────────────────────────────────────────────────
# Utility subcommands pass through without the autonomy flags: they do no agent
# work, and `codex login` in particular must not be handed a sandbox posture.
case "${1:-}" in
  login|logout|mcp|mcp-server|app-server|completion|doctor|update|debug|execpolicy|help|-h|--help|-V|--version)
    echo "🚀 Launching: codex $*"
    exec codex "$@"
    ;;
esac

# The caller's own posture wins: passing both ours and theirs is a clap CONFLICT,
# which fails the run outright rather than degrading to one of the two.
codex_has_posture_flag() {
  local a
  for a in "$@"; do
    case "$a" in
      --dangerously-bypass-approvals-and-sandbox | --yolo | --full-auto \
      | --sandbox | --sandbox=* | -s | --ask-for-approval | --ask-for-approval=* | -a) return 0 ;;
    esac
  done
  return 1
}

LAUNCH_ARGS=()
if codex_has_posture_flag "$@"; then
  echo "🔓 caller supplied its own approval/sandbox flags — leaving the posture to them"
elif [[ -n "${PROVEO_CODEX_SANDBOX:-}" ]]; then
  # Opt back INTO codex's own sandbox. Its Landlock+seccomp layer does not need an
  # unprivileged userns, so unlike cursor's it can actually engage in a cap-dropped
  # container — it is off by default because a workspace-write confinement of an
  # already-disposable tree buys little and its approval prompts stall unattended runs.
  LAUNCH_ARGS+=(--sandbox "$PROVEO_CODEX_SANDBOX" --ask-for-approval never)
else
  LAUNCH_ARGS+=(--dangerously-bypass-approvals-and-sandbox)
fi
if [[ -n "${CODEX_MODEL:-}" ]]; then
  LAUNCH_ARGS+=(--model "$CODEX_MODEL")
fi

# ── Agent evidence ─────────────────────────────────────────
#
# The dial is headless-only: the TUI has no verbosity switch, while `codex exec`
# can emit each event as JSONL. A caller's own --json is a parse contract and wins.
codex_is_exec() { [[ "${1:-}" == "exec" ]]; }
codex_has_json() {
  local a
  for a in "$@"; do
    case "$a" in --json | --json=* | --output-last-message | --output-last-message=*) return 0 ;; esac
  done
  return 1
}

if agent_evidence_verbose; then
  if codex_is_exec "$@" && ! codex_has_json "$@"; then
    # --json belongs to `codex exec`, NOT to the global flag set the posture flags
    # come from — so it has to follow the subcommand. Adding it to LAUNCH_ARGS
    # would put it ahead of `exec`, where clap rejects it and the run never starts.
    shift
    set -- exec --json "$@"
    report_agent_evidence --json
  elif codex_is_exec "$@"; then
    echo "🔎 agent evidence: verbose — the caller's own output format stands (it is a parse contract)"
  else
    echo "🔎 agent evidence: verbose requested — codex exposes no verbosity flag outside \`codex exec\`"
  fi
else
  report_agent_evidence
fi

echo "🚀 Launching Codex CLI: codex ${LAUNCH_ARGS[*]}"
proveo_exec_agent codex "${LAUNCH_ARGS[@]}" -- "$@"
