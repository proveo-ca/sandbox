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
# /command holds s6's tools (s6-setuidgid); s6's own /init puts it on PATH, and
# this entrypoint replaces /init, so the hook needs it supplied here.
if [[ -x /opt/hermes/docker/stage2-hook.sh ]]; then
  PATH="/command:${PATH}" /opt/hermes/docker/stage2-hook.sh
fi

proveo_seed hermes || true

# hermes reads a local endpoint from its own config.yaml (model.provider custom
# + model.base_url + model.default), not from OPENAI_BASE_URL or HERMES_MODEL.
# SPEC: _spec/defs/hermes/hermes-paradigm.puml
HERMES_LOCAL_WIRED=0
wire_hermes_local_endpoint() {
  local base_v1="$1" model="$2" h=/opt/hermes/bin/hermes
  "$h" config set model.provider custom >/dev/null \
    && "$h" config set model.base_url "$base_v1" >/dev/null \
    && "$h" config set model.default "$model" >/dev/null \
    || { echo "⚠️  could not write hermes config for ${model} at ${base_v1}" >&2; return 0; }
  HERMES_LOCAL_WIRED=1
  echo "🧩 Wired hermes to ${model} at ${base_v1} (provider custom)"
}

# A baked-model variant (hermes-muse-glimmer, hermes-qwen3.8) carries its own
# weights and starts the daemon locally before the agent runs.
start_baked_ollama() {
  [[ -n "${OLLAMA_BAKED_MODEL_TAG:-}" ]] || return 0
  if ! command -v ollama >/dev/null 2>&1; then
    echo "⚠️  OLLAMA_BAKED_MODEL_TAG is set but ollama is not installed in this image" >&2
    return 0
  fi
  ollama serve >/tmp/ollama-serve.log 2>&1 &
  local waited=0
  until curl -sf http://localhost:11434/api/tags >/dev/null 2>&1; do
    sleep 1
    waited=$((waited + 1))
    if (( waited >= 30 )); then
      echo "⚠️  baked ollama did not become ready within 30s — see /tmp/ollama-serve.log" >&2
      return 0
    fi
  done
  wire_hermes_local_endpoint "http://localhost:11434/v1" "${OLLAMA_BAKED_MODEL_TAG}"
}
start_baked_ollama

# Skipped on a baked variant — it wired its own model above.
configure_hermes_local_model() {
  [[ -z "${OLLAMA_BAKED_MODEL_TAG:-}" ]] || return 0
  [[ -n "${PROVEO_LOCAL_MODEL:-}" ]] || return 0
  local base="${OLLAMA_API_BASE:-http://ollama:11434}"
  wire_hermes_local_endpoint "${base%/}/v1" "${PROVEO_LOCAL_MODEL}"
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

if [[ "$HERMES_LOCAL_WIRED" != 1 ]] && ! has_api_key; then
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
