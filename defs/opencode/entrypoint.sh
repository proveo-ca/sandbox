#!/usr/bin/env bash
# SPEC: _spec/defs/opencode/opencode-topology.puml, _spec/defs/opencode/opencode-paradigm.puml
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

# Shared prelude (uid, .env, model bridges, git, sentinel) via Go when baked.
if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=opencode
  env PROVEO_SMOKE_TEST= proveo-entrypoint prep opencode || true
  set_working_directory "/app"
  load_env quiet
  apply_env_bridges
  apply_model_bridges opencode
else
  ensure_runtime_user
  set_working_directory "/app"
  load_env
  bridge_git_identity
  report_git_context
  attach_rtk
  apply_env_bridges
  apply_model_bridges opencode
fi

# ── Seed global defaults (~/.config/opencode) ──────────────
# Only seed files that are missing, unless OPENCODE_RESEED=1 forces a full re-seed.
write_minimal_opencode_config() {
  local target="$1"
  cat >"$target" <<'EOF'
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {},
  "model": "{env:OPENCODE_MODEL}",
  "small_model": "{env:OPENCODE_SMALL_MODEL}",
  "autoupdate": false,
  "agent": {
    "plan": {
      "description": "Read-only planner. Produces specs and step lists; never edits or runs shell.",
      "mode": "primary",
      "model": "{env:OPENCODE_MODEL}",
      "temperature": 0.1,
      "permission": { "edit": "deny", "bash": "deny" }
    },
    "build": {
      "description": "Implementer. Edits allowed; bash requires human approval per command.",
      "mode": "primary",
      "model": "{env:OPENCODE_BUILD_MODEL}",
      "temperature": 0.2,
      "permission": { "edit": "allow", "bash": "ask" }
    }
  }
}
EOF
}
seed_opencode_config() {
  local target="$1"
  local candidate
  local candidates=(
    "${HOME}/.config/opencode/sample_opencode.json"
    "${HOME}/Library/Application Support/opencode/sample_opencode.json"
    "/opt/opencode/sample_opencode.json"
    "/opt/homebrew/share/opencode/sample_opencode.json"
    "/usr/local/share/opencode/sample_opencode.json"
  )

  for candidate in "${candidates[@]}"; do
    if [[ -f "$candidate" ]]; then
      cp -f "$candidate" "$target"
      return 0
    fi
  done

  write_minimal_opencode_config "$target"
}

seed_defaults() {
  local src="/opt/opencode/defaults"
  local dst="${HOME}/.config/opencode"
  mkdir -p "$dst" "$dst/agents"

  if [[ "${OPENCODE_RESEED:-0}" == "1" ]]; then
    echo "🔁 OPENCODE_RESEED=1 — re-seeding $dst from baked-in defaults"
    seed_opencode_config "$dst/opencode.json"
    proveo_seed opencode
    return 0
  fi

  local seeded=()
  [[ -f "$dst/opencode.json" ]] || { seed_opencode_config "$dst/opencode.json"; seeded+=("opencode.json"); }
  proveo_seed opencode
  if (( ${#seeded[@]} > 0 )); then
    echo "🌱 Seeded global defaults into $dst: ${seeded[*]}"
  fi
}
seed_defaults
ensure_git_safe_directory "$(pwd)"
scope_git_worktree "$(pwd)"

# ── Local model (Ollama) provider ─────────────────────────
configure_opencode_local_model() {
  [[ -n "${PROVEO_LOCAL_MODEL:-}" ]] || return 0
  command -v jq >/dev/null 2>&1 || { echo "⚠️  jq missing; cannot wire Ollama provider" >&2; return 0; }
  local config_file="${HOME}/.config/opencode/opencode.json"
  local base="${OLLAMA_API_BASE:-http://ollama:11434}"
  local model="${PROVEO_LOCAL_MODEL}"
  mkdir -p "$(dirname "$config_file")"
  local existing='{}' tmp
  [[ -f "$config_file" ]] && jq -e . "$config_file" >/dev/null 2>&1 && existing="$(cat "$config_file")"
  tmp="$(mktemp)"
  if printf '%s' "$existing" | jq \
       --arg base "${base%/}/v1" --arg model "$model" '
         .provider.ollama = {
           npm: "@ai-sdk/openai-compatible",
           name: "Ollama (local)",
           options: { baseURL: $base, apiKey: "ollama" },
           models: { ($model): { name: ($model + " (local)") } }
         }
       ' >"$tmp"; then
    mv "$tmp" "$config_file"
    echo "🧩 Wired Ollama provider (ollama/$model → $base) into $config_file"
  else
    rm -f "$tmp"
    echo "⚠️  Could not wire Ollama provider (jq failed)" >&2
  fi
}
configure_opencode_local_model

# ── Seed project-level AGENTS.md if missing ───────────────
seed_project_agents_md() {
  local src="/opt/opencode/defaults/AGENTS.md"
  local dst="AGENTS.md"
  [[ -f "$src" ]] || return 0

  if [[ "${OPENCODE_RESEED:-0}" == "1" ]]; then
    cp -f "$src" "$dst"
    echo "🔁 Re-seeded AGENTS.md into workspace"
  elif [[ ! -f "$dst" ]]; then
    cp "$src" "$dst"
    echo "🌱 Seeded AGENTS.md into workspace"
  fi
}
seed_project_agents_md

configure_workspace_lsps() {
  local config_file="${HOME}/.config/opencode/opencode.json"
  local matched_json

  matched_json="$(detect_workspace_lsps "$(pwd)" | jq -R -s '
    split("\n") | map(select(length > 0) | split("|")) | map({
      key: .[0],
      value: { command: .[2:-1],
               extensions: (if (.[-1] | length) > 0 then (.[-1] | split(",")) else [] end) }
    }) | from_entries
  ')"
  [[ -n "$matched_json" ]] || matched_json="{}"

  echo "── Workspace LSP Match ──────────────────────────────"
  if [[ "$matched_json" == "{}" ]]; then
    echo "🔎 No installed LSP matched files under $(pwd)"
    echo "─────────────────────────────────────────────────────"
    return 0
  fi

  # Merge under .lsp with setdefault semantics (existing entries win), tolerating
  # a missing/invalid config or a non-object / `true` .lsp value.
  mkdir -p "$(dirname "$config_file")"
  local existing='{}' tmp
  [[ -f "$config_file" ]] && jq -e . "$config_file" >/dev/null 2>&1 && existing="$(cat "$config_file")"
  tmp="$(mktemp)"
  if printf '%s' "$existing" | jq --argjson matched "$matched_json" \
       '.lsp = ((if (.lsp | type) == "object" then .lsp else {} end) as $cur | $matched + $cur)' > "$tmp"; then
    mv "$tmp" "$config_file"
  else
    rm -f "$tmp"
    echo "⚠️  Could not update $config_file (jq failed)" >&2
  fi

  printf '✅ Enabled matching LSPs by workspace popularity: %s\n' \
    "$(printf '%s' "$matched_json" | jq -r 'keys_unsorted | join(" ")')"
  echo "Config: $config_file"
  echo "─────────────────────────────────────────────────────"
}
command_version() {
  command_version_opencode "$@"
}

echo "opencode version:   $(command_version opencode unknown --version)"
echo "Paradigm: GStack subagent crew (software engineering team)"
echo "node version:       $(command_version node unknown --version)"
echo "pnpm version:       $(command_version pnpm n/a -v)"

echo "── Team Workflow ────────────────────────────────────"
echo "Lead flow: classify → plan/design → delegate → build → verify → review"
echo "Review gates: @adversarial-reviewer always; @security-reviewer for sensitive changes; @spec-keeper for _spec/docs contracts"
echo "HITL: approve risky bash, destructive ops, publishes, deploys, secrets, and network/security changes"
echo "─────────────────────────────────────────────────────"

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

# ── Configuration check ────────────────────────────────────
echo "── Configuration Check ──────────────────────────────"
if [[ -f opencode.json ]]; then
  echo "✅ Found opencode.json"
elif [[ -f opencode.jsonc ]]; then
  echo "✅ Found opencode.jsonc"
else
  echo "🔎 No project opencode.json"
fi
if [[ -f AGENTS.md ]]; then echo "✅ Found AGENTS.md"; else echo "🔎 No AGENTS.md"; fi

# Surface available subagents (global + project)
agent_files=()
[[ -d "${HOME}/.config/opencode/agents" ]] && \
  while IFS= read -r f; do agent_files+=("@$(basename "${f%.md}")"); done \
  < <(find "${HOME}/.config/opencode/agents" -maxdepth 1 -name '*.md' 2>/dev/null | sort)
[[ -d .opencode/agents ]] && \
  while IFS= read -r f; do agent_files+=("@$(basename "${f%.md}") (project)"); done \
  < <(find .opencode/agents -maxdepth 1 -name '*.md' 2>/dev/null | sort)
if (( ${#agent_files[@]} > 0 )); then
  echo "🧑‍💻 Subagents available: ${agent_files[*]}"
fi
echo "─────────────────────────────────────────────────────"

printf 'PROVEO_MODELS main=%s small=%s\n' \
  "${OPENCODE_MODEL:-unset}" "${OPENCODE_SMALL_MODEL:-unset}"

if [[ "${PROVEO_SMOKE_TEST:-0}" == "1" ]]; then
  echo "✅ PROVEO_SMOKE_READY ${PROVEO_SMOKE_TARGET:-opencode}"
  exec sleep infinity
fi

# Toolchain provisioning moved into proveo_seed (runs on both backends).
configure_workspace_lsps

# ── API key detection ──────────────────────────────────────
# OPENCODE_API_KEY is OpenCode's own gateway — Zen (pay-as-you-go) and Go (the
# subscription) both read it, ahead of auth.json, so a host-exported key is a
# complete login and needs no /connect inside the sandbox (whose auth.json proveo
# scrubs every run).
has_api_key() {
  [[ -n "$OPENCODE_API_KEY" ]] || \
  [[ -n "$ANTHROPIC_API_KEY" ]] || \
  [[ -n "$OPENAI_API_KEY" ]] || \
  [[ -n "$OPENROUTER_API_KEY" ]] || \
  [[ -n "$XAI_API_KEY" ]] || \
  [[ -n "$GEMINI_API_KEY" ]] || \
  [[ -n "$GOOGLE_API_KEY" ]] || \
  [[ -n "$GOOGLE_GENERATIVE_AI_API_KEY" ]] || \
  [[ -n "$DEEPSEEK_API_KEY" ]] || \
  [[ -n "$GROQ_API_KEY" ]] || \
  [[ -n "$MISTRAL_API_KEY" ]]
}

has_project_config() {
  [[ -f opencode.json ]] || [[ -f opencode.jsonc ]]
}

if ! has_api_key && ! has_project_config; then
  echo "⚠️  No provider API key env vars and no opencode.json detected."
  echo "   Set one of: OPENCODE_API_KEY (OpenCode Zen / Go), ANTHROPIC_API_KEY, OPENAI_API_KEY,"
  echo "   OPENROUTER_API_KEY, XAI_API_KEY, GEMINI_API_KEY, DEEPSEEK_API_KEY, GROQ_API_KEY, ..."
  echo "   A key exported on the host is the login; 'opencode auth login' inside the"
  echo "   sandbox writes auth.json, which proveo scrubs on the next run."
fi

# ── Agent evidence ─────────────────────────────────────────
OPENCODE_EVIDENCE_ARGS=()
if agent_evidence_verbose; then
  OPENCODE_EVIDENCE_ARGS=(--log-level DEBUG)
  evidence_desc=("${OPENCODE_EVIDENCE_ARGS[@]}")
  if [[ $# -gt 0 && "$1" != "tui" ]]; then
    OPENCODE_EVIDENCE_ARGS+=(--print-logs)
    evidence_desc+=(--print-logs)
  fi
  if [[ "${1:-}" == "run" ]]; then
    shift
    set -- run --thinking "$@"
    evidence_desc+=(--thinking)
  fi
  report_agent_evidence "${evidence_desc[@]}"
else
  report_agent_evidence
fi

# ── Launch ─────────────────────────────────────────────────
if [[ $# -gt 0 ]]; then
  set -- "${OPENCODE_EVIDENCE_ARGS[@]}" "$@"
  echo "🚀 Launching: opencode $*"
  exec opencode "$@"
fi

echo "🚀 Launching opencode TUI..."
exec opencode "${OPENCODE_EVIDENCE_ARGS[@]}"
