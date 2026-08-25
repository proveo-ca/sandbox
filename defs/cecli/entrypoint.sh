#!/usr/bin/env bash
# SPEC: _spec/defs/cecli/cecli-topology.puml, _spec/defs/cecli/cecli-paradigm.puml
set -euo pipefail

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=cecli
  env PROVEO_SMOKE_TEST= proveo-entrypoint prep cecli || true
  set_working_directory "/app"
  load_env quiet
else
  ensure_runtime_user
  set_working_directory "/app"
fi

ensure_git_safe_directory "$(pwd)"
scope_git_worktree "$(pwd)"
ensure_python_env "$(pwd)"

: "${CECLI_HOME:=/app/.cecli}"
export CECLI_HOME
mkdir -p "$CECLI_HOME" 2>/dev/null || true

seed_cecli_subagents() {
  proveo_seed cecli
}

has_cecli_agent_config() {
  local config_file
  for config_file in .cecli.config.yml .cecli.config.yaml .cecli.conf.yml .cecli.conf.yaml; do
    [[ -f "$config_file" ]] || continue
    if grep -qE '^[[:space:]]*agent-config:' "$config_file"; then
      return 0
    fi
  done
  return 1
}

if ! command -v proveo-entrypoint >/dev/null 2>&1; then
  load_env
  bridge_git_identity
  report_git_context
fi

# Model bridges are declared in defs/bridges/cecli.tsv.
apply_model_bridges cecli



# A local model reaches litellm through Ollama's OPENAI-COMPATIBLE API, not its
# native one. Both litellm ollama providers are broken in this image:
# "ollama_chat/<model>" loses its api_base and every call dies with "Request URL
# is missing an 'http://' or 'https://' protocol", while "ollama/<model>" raises
# FileNotFoundError. Ollama also serves /v1, and that route works — so the model
# is spelled "openai/<model>" and pointed at it.
if [[ -n "${PROVEO_LOCAL_MODEL:-}" ]]; then
  # OPENAI_API_BASE is litellm's name for the endpoint (OPENAI_BASE_URL is the
  # SDK's); prefer whatever `proveo run --local-model` already set, and derive it
  # from the Ollama base otherwise so a hand-run container still works.
  export OPENAI_API_BASE="${OPENAI_API_BASE:-${OLLAMA_API_BASE:-http://ollama:11434}}"
  case "$OPENAI_API_BASE" in
    */v1|*/v1/) : ;;
    *) OPENAI_API_BASE="${OPENAI_API_BASE%/}/v1"; export OPENAI_API_BASE ;;
  esac
  export OPENAI_API_KEY="${OPENAI_API_KEY:-ollama}"
  export CECLI_MODEL="openai/${PROVEO_LOCAL_MODEL}"
  export CECLI_EDITOR_MODEL="openai/${PROVEO_LOCAL_MODEL}"
  export CECLI_WEAK_MODEL="openai/${PROVEO_LOCAL_MODEL}"
fi

case "${DARK_MODE:-}" in
  true|TRUE|True|1|yes|YES|Yes)
    export CECLI_DARK_MODE="${CECLI_DARK_MODE:-true}"
    export AIDER_DARK_MODE="${AIDER_DARK_MODE:-true}"
    ;;
esac

if [[ -n "${CODE_THEME:-}" ]]; then
  export CECLI_CODE_THEME="${CECLI_CODE_THEME:-$CODE_THEME}"
fi

seed_cecli_subagents


if [[ -z "${CECLI_AGENT_CONFIG:-}" ]] && ! has_cecli_agent_config; then
  CECLI_AGENT_CONFIG="{\"large_file_token_threshold\":8192,\"skip_cli_confirmations\":false,\"max_sub_agents\":3,\"subagent_paths\":[\"$CECLI_HOME/agents\",\"/app/.cecli/agents\"]}"
  export CECLI_AGENT_CONFIG
fi

# ── Seed project-level CONVENTIONS.md if missing ──────────
if [[ -f /opt/cecli/defaults/CONVENTIONS.md && ! -f CONVENTIONS.md ]]; then
  cp /opt/cecli/defaults/CONVENTIONS.md CONVENTIONS.md
  echo "🌱 Seeded CONVENTIONS.md into workspace"
fi

command_version() {
  command_version_cecli "$@"
}

echo "cecli version:      $(command_version installed cecli --version)"
echo "Paradigm: Pair-programming specialist (precise, low-token, human-guided)"

if command -v curl >/dev/null 2>&1; then
  echo "curl version:       $(command_version unknown curl --version | head -n1)"
fi

if command -v git >/dev/null 2>&1; then
  echo "git version:        $(command_version unknown git --version)"
fi

if command -v gh >/dev/null 2>&1; then
  echo "gh version:         $(command_version unknown gh --version | head -n1)"
fi

if command -v npm >/dev/null 2>&1; then
  echo "npm version:        $(command_version unknown npm -v)"
fi

if command -v pnpm >/dev/null 2>&1; then
  echo "pnpm version:       $(command_version n/a pnpm -v)"
fi

if [[ -n "${CECLI_MODEL:-${AIDER_MODEL:-}}" ]]; then
  echo "model:              ${CECLI_MODEL:-${AIDER_MODEL:-}}"
fi

if [[ -n "${CECLI_EDITOR_MODEL:-${AIDER_EDITOR_MODEL:-}}" ]]; then
  echo "editor model:       ${CECLI_EDITOR_MODEL:-${AIDER_EDITOR_MODEL:-}}"
fi

if [[ -n "${CECLI_WEAK_MODEL:-${AIDER_WEAK_MODEL:-}}" ]]; then
  echo "weak model:         ${CECLI_WEAK_MODEL:-${AIDER_WEAK_MODEL:-}}"
fi

if [[ -n "${CECLI_DARK_MODE:-${AIDER_DARK_MODE:-}}" ]]; then
  echo "dark mode:          ${CECLI_DARK_MODE:-${AIDER_DARK_MODE:-}}"
fi

if [[ -n "${CECLI_CODE_THEME:-}" ]]; then
  echo "code theme:         $CECLI_CODE_THEME"
fi

printf 'PROVEO_MODELS main=%s small=%s\n' \
  "${CECLI_MODEL:-unset}" "${CECLI_WEAK_MODEL:-unset}"

echo "── Configuration Check ──────────────────────────────"
if [[ -f .cecli.config.yml ]]; then
  echo "✅ Found .cecli.config.yml"
elif [[ -f .cecli.config.yaml ]]; then
  echo "✅ Found .cecli.config.yaml"
elif [[ -f .cecli.conf.yml ]]; then
  echo "✅ Found .cecli.conf.yml"
elif [[ -f .cecli.conf.yaml ]]; then
  echo "✅ Found .cecli.conf.yaml"
else
  echo "🔎 Not found .cecli.config.yml"
fi

if [[ -f .cecliignore ]]; then echo "✅ Found .cecliignore"; else echo "🔎 Not found .cecliignore"; fi
if [[ -f CONVENTIONS.md ]]; then echo "✅ Found CONVENTIONS.md"; else echo "🔎 Not found CONVENTIONS.md"; fi
if [[ -d "$CECLI_HOME/agents" ]]; then
  subagent_files=()
  while IFS= read -r f; do subagent_files+=("@$(basename "${f%.md}")"); done \
    < <(find "$CECLI_HOME/agents" -maxdepth 1 -name '*.md' 2>/dev/null | sort)
  if (( ${#subagent_files[@]} > 0 )); then
    echo "🧑‍💻 Subagents available: ${subagent_files[*]}"
  fi
fi
echo "─────────────────────────────────────────────────────"

run_smoke_test "cecli"

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

ensure_project_tools

CECLI_RULE_ARGS=()
for f in AGENTS.md CONVENTIONS.md; do
  [[ -f "$f" ]] && CECLI_RULE_ARGS+=(--rules "$f")
done
if [[ ${#CECLI_RULE_ARGS[@]} -gt 0 ]]; then
  echo "📐 rules: ${CECLI_RULE_ARGS[*]}"
fi

# ── Agent evidence ─────────────────────────────────────────
CECLI_EVIDENCE_ARGS=()
if agent_evidence_verbose; then
  CECLI_EVIDENCE_ARGS=(--verbose --show-diffs)
  report_agent_evidence "${CECLI_EVIDENCE_ARGS[@]}"
else
  report_agent_evidence
fi

# Evidence flags ride with the rules: both are ours to add, and both belong only
# on a cecli invocation — never on the bash/git/... passthroughs below.
if [[ $# -eq 0 ]]; then
  set -- cecli "${CECLI_RULE_ARGS[@]}" "${CECLI_EVIDENCE_ARGS[@]}"
elif [[ "$1" == -* ]]; then
  set -- cecli "${CECLI_RULE_ARGS[@]}" "${CECLI_EVIDENCE_ARGS[@]}" "$@"
elif [[ "$1" != "cecli" && "$1" != "bash" && "$1" != "sh" && "$1" != "python" && "$1" != "python3" && "$1" != "node" && "$1" != "npm" && "$1" != "pnpm" && "$1" != "git" && "$1" != "curl" ]]; then
  set -- cecli "${CECLI_RULE_ARGS[@]}" "${CECLI_EVIDENCE_ARGS[@]}" "$@"
fi

exec "$@"
