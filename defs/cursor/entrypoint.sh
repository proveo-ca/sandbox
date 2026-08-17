#!/usr/bin/env bash
# SPEC: _spec/defs/cursor/cursor-topology.puml, _spec/defs/cursor/cursor-paradigm.puml
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

# Shared prelude (uid, .env, bridges, git, sentinel) via Go entrypoint when baked.
if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=cursor
  env PROVEO_SMOKE_TEST= proveo-entrypoint prep cursor || true
  set_working_directory "/app"
  load_env quiet
else
  ensure_runtime_user
  set_working_directory "/app"
  load_env
  bridge_git_identity
  report_git_context
  attach_rtk
fi

# ARCHITECT_MODEL / EDITOR_MODEL → CURSOR_MODEL (cursor-specific)
if [[ -z "${CURSOR_MODEL:-}" ]]; then
  if [[ -n "${ARCHITECT_MODEL:-}" ]]; then
    export CURSOR_MODEL="$ARCHITECT_MODEL"
  elif [[ -n "${EDITOR_MODEL:-}" ]]; then
    export CURSOR_MODEL="$EDITOR_MODEL"
  fi
fi

# ── Seed user-level defaults (~/.cursor) ────────────────────
# Only seed files that are missing, unless CURSOR_RESEED=1 forces a full
# re-seed. The mounted workspace is never touched here (see CURSOR_SEED_RULES).
CURSOR_HOME="${CURSOR_CONFIG_DIR:-$HOME/.cursor}"

seed_defaults() {
  local src="/opt/cursor/defaults"
  local dst="$CURSOR_HOME"
  mkdir -p "$dst" "$dst/agents"

  if [[ "${CURSOR_RESEED:-0}" == "1" ]]; then
    echo "🔁 CURSOR_RESEED=1 — re-seeding $dst from baked-in defaults"
    cp -f "$src/cli-config.json" "$dst/cli-config.json"
    if [[ -d "$src/agents" ]]; then
      for f in "$src/agents/"*.md; do
        [[ -e "$f" ]] || continue
        cp -f "$f" "$dst/agents/"
      done
    fi
    return 0
  fi

  local seeded=()
  [[ -f "$dst/cli-config.json" ]] || { cp "$src/cli-config.json" "$dst/cli-config.json"; seeded+=("cli-config.json"); }
  if [[ -d "$src/agents" ]]; then
    for f in "$src/agents/"*.md; do
      [[ -e "$f" ]] || continue
      local name; name="$(basename "$f")"
      [[ -f "$dst/agents/$name" ]] || { cp "$f" "$dst/agents/$name"; seeded+=("agents/$name"); }
    done
  fi
  if (( ${#seeded[@]} > 0 )); then
    echo "🌱 Seeded global defaults into $dst: ${seeded[*]}"
  fi
}
seed_defaults
ensure_git_safe_directory "$(pwd)"
scope_git_worktree "$(pwd)"

# ── Proxy compatibility ─────────────────────────────────────
# Cursor's agent traffic uses HTTP/2 bidirectional streaming by default, which
# does not survive every proxy chain (Squid CONNECT + mitmproxy interception).
# Behind a proxy, force the documented HTTP/1.1 SSE fallback in the seeded
# config; the CLI's Node stack honors HTTP(S)_PROXY and NODE_EXTRA_CA_CERTS.
configure_proxy_compat() {
  [[ -n "${HTTP_PROXY:-}${HTTPS_PROXY:-}${http_proxy:-}${https_proxy:-}" ]] || return 0
  export NODE_USE_ENV_PROXY=1
  local cfg="$CURSOR_HOME/cli-config.json" tmp
  tmp="$(mktemp)"
  # Start from the existing config (or a minimal doc if missing/invalid), then set
  # the nested network.useHttp1ForAgent=true. The shipped CLI's config normalizer
  # drops the top-level spelling, so it MUST live under "network". jq auto-creates
  # the object; guard against a non-object "network" value.
  local base='{"version":1}'
  if [[ -f "$cfg" ]] && jq -e . "$cfg" >/dev/null 2>&1; then
    base="$(cat "$cfg")"
  fi
  if printf '%s' "$base" \
       | jq '(.network |= (if type == "object" then . else {} end)) | .network.useHttp1ForAgent = true' > "$tmp"; then
    mv "$tmp" "$cfg"
    echo "🛡️  Proxy detected — set network.useHttp1ForAgent=true in $cfg"
  else
    rm -f "$tmp"
    echo "⚠️  Could not set proxy compatibility in $cfg (jq failed)" >&2
  fi
}
configure_proxy_compat

# ── LSP code intelligence via mcp-language-server ──────────
# Cursor has no native external-LSP config, so LSP-grade tools are exposed
# through the mcp-language-server MCP bridge (one instance per language server).
# The SHARED detector (entrypoint-lib.sh §8) selects only the languages present
# whose static-binary LSP is installed in the image; each is wrapped in an
# mcp-language-server entry written to the GLOBAL ~/.cursor/mcp.json — never the
# mounted repo. Existing entries win on merge (don't clobber user config).
configure_cursor_lsp_mcp() {
  command -v mcp-language-server >/dev/null 2>&1 || return 0
  local mcp_file="$CURSOR_HOME/mcp.json" tmp base entries
  entries="$(detect_workspace_lsps "$(pwd)" | jq -R -s '
    split("\n") | map(select(length > 0) | split("|")) | map({
      key: .[0],
      value: {
        command: "mcp-language-server",
        args: (["--workspace", "/app", "--lsp", .[2]]
               + (.[3:-1] | if length > 0 then ["--"] + . else [] end))
      }
    }) | from_entries')"
  [[ -z "$entries" || "$entries" == "{}" ]] && return 0

  mkdir -p "$CURSOR_HOME"
  base='{}'
  [[ -f "$mcp_file" ]] && jq -e . "$mcp_file" >/dev/null 2>&1 && base="$(cat "$mcp_file")"
  tmp="$(mktemp)"
  if printf '%s' "$base" | jq --argjson e "$entries" \
       '.mcpServers = ($e + ((.mcpServers // {}) | if type == "object" then . else {} end))' > "$tmp"; then
    mv "$tmp" "$mcp_file"
    echo "🧠 LSP code intelligence via mcp-language-server: $(printf '%s' "$entries" | jq -r 'keys_unsorted | join(" ")')"
  else
    rm -f "$tmp"
  fi
}
command_version() {
  command_version_opencode "$@"
}

echo "cursor cli version: $(command_version agent unknown --version)"
echo "Paradigm: policy-gated autonomous loop (spec → plan → implement → verify)"
echo "node version:       $(command_version node unknown --version)"
echo "pnpm version:       $(command_version pnpm n/a -v)"

# ── Policy layer report ────────────────────────────────────
echo "── Policy Layer ─────────────────────────────────────"
deny_count="$(jq -r '(.permissions.deny // []) | length' "$CURSOR_HOME/cli-config.json" 2>/dev/null || echo 0)"
deny_count="${deny_count:-0}"
echo "Deny rules (survive --force): ${deny_count} — $CURSOR_HOME/cli-config.json"
if [[ -f /etc/cursor/hooks.json ]]; then
  echo "Shell audit hook: /etc/cursor/hooks.json (enterprise layer, root-owned, fail-open)"
fi

# Surface available subagents (user + project)
agent_files=()
[[ -d "$CURSOR_HOME/agents" ]] && \
  while IFS= read -r f; do agent_files+=("$(basename "${f%.md}")"); done \
  < <(find "$CURSOR_HOME/agents" -maxdepth 1 -name '*.md' 2>/dev/null | sort)
[[ -d .cursor/agents ]] && \
  while IFS= read -r f; do agent_files+=("$(basename "${f%.md}") (project)"); done \
  < <(find .cursor/agents -maxdepth 1 -name '*.md' 2>/dev/null | sort)
if (( ${#agent_files[@]} > 0 )); then
  echo "🧑‍💻 Subagents available: ${agent_files[*]}"
fi
echo "─────────────────────────────────────────────────────"

# ── Steering files: detect and report, never write by default ──────────
# Project steering is the repo's own. The baked loop rule reaches the
# workspace only when explicitly requested with CURSOR_SEED_RULES=1.
report_steering() {
  echo "── Steering Files ───────────────────────────────────"
  local found=0
  [[ -f AGENTS.md ]] && { echo "✅ Found AGENTS.md"; found=1; }
  [[ -f CLAUDE.md ]] && { echo "✅ Found CLAUDE.md"; found=1; }
  [[ -f .cursorrules ]] && { echo "✅ Found .cursorrules (legacy)"; found=1; }
  if [[ -d .cursor/rules ]]; then
    local n
    n="$(find .cursor/rules -maxdepth 1 -name '*.mdc' 2>/dev/null | wc -l | tr -d ' ')"
    if [[ "$n" -gt 0 ]]; then
      echo "✅ Found .cursor/rules (${n} rule(s))"
      found=1
    fi
  fi

  if [[ "${CURSOR_SEED_RULES:-0}" == "1" ]]; then
    if mkdir -p .cursor/rules 2>/dev/null && \
       cp -f /opt/cursor/defaults/rules/proveo-loop.mdc .cursor/rules/proveo-loop.mdc 2>/dev/null; then
      echo "🌱 Seeded .cursor/rules/proveo-loop.mdc into workspace (CURSOR_SEED_RULES=1)"
    else
      echo "⚠️  CURSOR_SEED_RULES=1 but $(pwd) is not writable; skipping rule seed"
    fi
  elif (( found == 0 )); then
    echo "🔎 No steering files detected; seed the baked loop rule with CURSOR_SEED_RULES=1"
  fi
  echo "─────────────────────────────────────────────────────"
}
report_steering

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

# ── Smoke test mode ────────────────────────────────────────
run_smoke_test "cursor"

ensure_project_tools
ensure_python_env "$(pwd)"
ensure_language_servers "$(pwd)"
configure_cursor_lsp_mcp

# ── Auth check ─────────────────────────────────────────────
# All inference transits the Cursor backend; there is no provider-key or
# local-model alternative. Headless auth is CURSOR_API_KEY.
if [[ -z "${CURSOR_API_KEY:-}" ]]; then
  echo "⚠️  CURSOR_API_KEY not set. Create one at cursor.com/dashboard → API Keys,"
  echo "   or run 'agent login' interactively (NO_OPEN_BROWSER=1 prints the URL)."
  echo "   Sessions persist under proveo home (~/.proveo/.cursor → \$HOME/.cursor);"
  echo "   prefer CURSOR_API_KEY — login tokens are scrubbed from the durable cache."
fi

# ── Launch ─────────────────────────────────────────────────
# Utility subcommands pass through without the autonomy flags.
case "${1:-}" in
  login|logout|status|whoami|ls|resume|update|upgrade|mcp|create-chat|uninstall|help|-v|--version|-h|--help)
    echo "🚀 Launching: agent $*"
    exec agent "$@"
    ;;
esac

# Landlock needs unprivileged userns; this cap-dropped container cannot provide
# them. Docker is the FS/process boundary; network containment is the free-tier
# sbx experiment (_spec/_experiments/docker-sandbox.puml). Override via
# PROVEO_CURSOR_SANDBOX only on hosts where userns works.
LAUNCH_ARGS=(--force --sandbox "${PROVEO_CURSOR_SANDBOX:-disabled}")
if [[ -n "${CURSOR_MODEL:-}" ]]; then
  LAUNCH_ARGS+=(--model "$CURSOR_MODEL")
fi
for arg in "$@"; do
  if [[ "$arg" == "-p" || "$arg" == "--print" ]]; then
    # Headless runs need workspace trust up front (no prompt to answer).
    LAUNCH_ARGS+=(--trust)
    break
  fi
done

echo "🚀 Launching Cursor CLI: agent ${LAUNCH_ARGS[*]}"
exec agent "${LAUNCH_ARGS[@]}" "$@"
