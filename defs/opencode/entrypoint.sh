#!/usr/bin/env bash
# SPEC: _spec/defs/opencode/opencode-topology.puml, _spec/defs/opencode/opencode-paradigm.puml
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi
proveo_sbx_passthrough "$@"

if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=opencode
  env PROVEO_SMOKE_TEST= proveo-entrypoint prep opencode || true
  set_working_directory "/app"
  load_env quiet
  apply_env_bridges
else
  ensure_runtime_user
  set_working_directory "/app"
  load_env
  bridge_git_identity
  report_git_context
  attach_rtk
  apply_env_bridges
fi

# SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/_plans/retire-model-bridging.puml
seed_defaults() {
  if [[ "${PROVEO_SEED_ONLY:-}" == 1 && "${PROVEO_OPENCODE_PREFLIGHT_READY:-}" != 1 ]]; then
    /usr/local/bin/proveo-opencode-runtime --check-prepared >/dev/null 2>&1 || exit 0
  fi
  proveo_seed opencode
}
seed_defaults
ensure_git_safe_directory "$(pwd)"
ensure_github_git_transport
scope_git_worktree "$(pwd)"

configure_opencode_local_model() {
  [[ -n "${PROVEO_LOCAL_MODEL:-}" ]] || return 0
  command -v jq >/dev/null 2>&1 || { echo "❌ Install jq to wire the requested Ollama model" >&2; return 1; }
  local config_file="${HOME}/.config/opencode/opencode.json"
  local base="${OLLAMA_API_BASE:-http://ollama:11434}"
  local model="${PROVEO_LOCAL_MODEL}"
  if [[ -e "${config_file}c" ]]; then
    echo "❌ Wire the requested Ollama model in ${config_file}c directly; automatic wiring cannot edit the higher-priority JSONC config" >&2
    return 1
  fi
  mkdir -p "$(dirname "$config_file")" || return 1
  local existing='{}' tmp
  if [[ -e "$config_file" ]]; then
    if ! jq -e -s 'length == 1 and (.[0] | type == "object")' "$config_file" >/dev/null 2>&1; then
      echo "❌ Fix $config_file to contain one valid JSON object before wiring the requested Ollama model" >&2
      return 1
    fi
    existing="$(cat "$config_file")" || return 1
  fi
  while [[ "$base" == */ ]]; do base="${base%/}"; done
  while [[ "$base" == */v1 ]]; do
    base="${base%/v1}"
    while [[ "$base" == */ ]]; do base="${base%/}"; done
  done
  tmp="$(mktemp "${config_file}.tmp.XXXXXX")" || return 1
  # SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/_plans/retire-model-bridging.puml
  if printf '%s' "$existing" | jq \
       --arg base "$base/v1" --arg model "$model" '
          .providers.ollama = {
            package: "@opencode/ai/providers/openai-compatible",
            name: "Ollama (local)",
            settings: { baseURL: $base, apiKey: "ollama" },
            models: { ($model): { name: ($model + " (local)") } }
          }
          | .model = ("ollama/" + $model)
          | .agents.title.model = ("ollama/" + $model)
        ' >"$tmp" && mv "$tmp" "$config_file"; then
    export OPENCODE_MODEL="ollama/$model"
    export OPENCODE_SMALL_MODEL="ollama/$model"
    echo "🧩 Wired Ollama provider (ollama/$model → $base) into $config_file"
  else
    rm -f "$tmp"
    echo "❌ Could not wire the requested Ollama model in $config_file; check its providers/agents objects and write permissions" >&2
    return 1
  fi
}
configure_opencode_local_model

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

echo "── Configuration Check ──────────────────────────────"
if [[ -f opencode.json ]]; then
  echo "✅ Found opencode.json"
elif [[ -f opencode.jsonc ]]; then
  echo "✅ Found opencode.jsonc"
else
  echo "🔎 No project opencode.json"
fi
if [[ -f AGENTS.md ]]; then echo "✅ Found AGENTS.md"; else echo "🔎 No AGENTS.md"; fi

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
   echo "   Credentials created inside the sandbox do not persist in saved history."
fi

OPENCODE_EVIDENCE_ARGS=()
if agent_evidence_verbose; then
  OPENCODE_EVIDENCE_ARGS=(--log-level debug)
  evidence_desc=("${OPENCODE_EVIDENCE_ARGS[@]}")
  if [[ "${1:-}" == "run" ]]; then
    OPENCODE_EVIDENCE_ARGS+=(--print-logs)
    evidence_desc+=(--print-logs)
    shift
    set -- run --thinking "$@"
    evidence_desc+=(--thinking)
  fi
  report_agent_evidence "${evidence_desc[@]}"
else
  report_agent_evidence
fi

# SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/_paradigms/capability-ladder.puml
echo "🚀 Launching opencode ${OPENCODE_EVIDENCE_ARGS[*]}"
proveo_exec_agent opencode "${OPENCODE_EVIDENCE_ARGS[@]}" -- "$@"
