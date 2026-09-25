#!/usr/bin/env bash
# SPEC: _spec/defs/hermes/hermes-topology.puml, _spec/defs/hermes/hermes-paradigm.puml
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi
proveo_sbx_passthrough "$@"

ensure_runtime_user
set_working_directory "/app"
load_env
bridge_git_identity
report_git_context
attach_rtk
apply_env_bridges
ensure_git_safe_directory "$(pwd)"
ensure_github_git_transport
scope_git_worktree "$(pwd)"

# Upstream normally runs this as an s6-overlay cont-init.d step (UID/GID
# remap, /opt/data volume chown, config seeding, skills sync) before /init
# supervises the main process. Overriding ENTRYPOINT for a single foreground
# process means that step never fires on its own — run it explicitly, once,
# still as root, so the SAME logic upstream maintains does the remap instead
# of a proveo-side reimplementation that drifts from it on the next release.
if [[ -x /opt/hermes/docker/stage2-hook.sh ]]; then
  /opt/hermes/docker/stage2-hook.sh
fi

proveo_seed hermes || true

# The local model is an OpenAI-compatible endpoint, not a config block: hermes
# takes any such endpoint directly via env, so wiring it needs no config-file
# surgery the way opencode's provider map does.
configure_hermes_local_model() {
  [[ -n "${PROVEO_LOCAL_MODEL:-}" ]] || return 0
  local base="${OLLAMA_API_BASE:-http://ollama:11434}"
  export OPENAI_BASE_URL="${base%/}/v1"
  export OPENAI_API_KEY="${OPENAI_API_KEY:-ollama}"
  export HERMES_MODEL="ollama/${PROVEO_LOCAL_MODEL}"
  echo "🧩 Wired Ollama provider (ollama/${PROVEO_LOCAL_MODEL} -> ${base}) via OPENAI_BASE_URL"
}
configure_hermes_local_model

has_api_key() {
  [[ -n "$OPENAI_API_KEY" ]] || \
  [[ -n "$ANTHROPIC_API_KEY" ]] || \
  [[ -n "$XAI_API_KEY" ]] || \
  [[ -n "$GEMINI_API_KEY" ]] || \
  [[ -n "$GOOGLE_API_KEY" ]] || \
  [[ -n "$GOOGLE_GENERATIVE_AI_API_KEY" ]] || \
  [[ -n "$DEEPSEEK_API_KEY" ]] || \
  [[ -n "$GROQ_API_KEY" ]] || \
  [[ -n "$MISTRAL_API_KEY" ]]
}

if ! has_api_key; then
  echo "⚠️  No provider API key env vars detected."
  echo "   Set one of: OPENAI_API_KEY, ANTHROPIC_API_KEY, XAI_API_KEY, GEMINI_API_KEY,"
  echo "   DEEPSEEK_API_KEY, GROQ_API_KEY, MISTRAL_API_KEY — or PROVEO_LOCAL_MODEL for the"
  echo "   Ollama sidecar."
fi

echo "hermes version: $(command_version_opencode hermes unknown --version)"
echo "Paradigm: single foreground Hermes session — native browser tool, no stealth tier"
echo "Browser real-profile-reuse is OFF unless explicitly configured — this sandbox never"
echo "copies the operator's real cookies/saved logins by default."

if [[ "${PROVEO_SMOKE_TEST:-0}" == "1" ]]; then
  echo "✅ PROVEO_SMOKE_READY ${PROVEO_SMOKE_TARGET:-hermes}"
  exec sleep infinity
fi

echo "🚀 Launching hermes"
proveo_exec_agent hermes -- "$@"
