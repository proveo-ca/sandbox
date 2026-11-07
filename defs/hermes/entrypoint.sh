#!/usr/bin/env bash
# SPEC: _spec/defs/hermes/hermes-topology.puml, _spec/defs/hermes/hermes-paradigm.puml
set -e

# --prepare: configure hermes, then exit before launching it (Hermes Desktop starts its own backend).
# SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (PREPARE)
prepare_only=0
if [[ "${1:-}" == "--prepare" ]]; then
  prepare_only=1
  shift
fi

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

set_working_directory "/app"
bridge_google_keys
bridge_git_identity
proveo-entrypoint prep || true
ensure_git_safe_directory "$(pwd)"
ensure_github_git_transport
scope_git_worktree "$(pwd)"

# The manifest's durable home mount at its host path; otherwise a writable fallback.
# SPEC: _spec/defs/hermes/hermes-persistence.puml
hermes_durable=""
for d in "${PROVEO_STATE_HOME:+${PROVEO_STATE_HOME}/hermes/data}"; do
  if [[ -n "$d" && -d "$d" && -w "$d" ]]; then
    hermes_durable="$d"
    break
  fi
done
if [[ -n "$hermes_durable" ]]; then
  # One-time move of a home that only ever lived on the image's /opt/data volume.
  if [[ -z "$(ls -A "$hermes_durable" 2>/dev/null)" && -f /opt/data/state.db ]]; then
    if tar -C /opt/data -cf - . 2>/dev/null | tar -C "$hermes_durable" -xpf - 2>/dev/null; then
      echo "📦 Moved hermes's home from the sandbox volume to ${hermes_durable}"
    else
      echo "⚠️  could not copy /opt/data to ${hermes_durable}; hermes starts with an empty home there" >&2
    fi
  fi
  export HERMES_HOME="$hermes_durable"
elif [[ "$(id -u)" != 0 && ! -w "${HERMES_HOME:-/opt/data}" ]]; then
  export HERMES_HOME="${HOME}/.hermes"
fi

# write_file/patch may also write the workspace and hermes's home, not only the image volume.
# SPEC: _spec/defs/hermes/hermes-persistence.puml
HERMES_WRITE_SAFE_ROOT="${HERMES_WRITE_SAFE_ROOT:+${HERMES_WRITE_SAFE_ROOT}:}${PWD}${HERMES_HOME:+:${HERMES_HOME}}"
export HERMES_WRITE_SAFE_ROOT

# The hook would hand the browser path to s6's environment, which this launch
# never reads; exporting it here also makes the hook skip its /run/s6 write.
if [[ -z "${AGENT_BROWSER_EXECUTABLE_PATH:-}" && -d "${PLAYWRIGHT_BROWSERS_PATH:-}" ]]; then
  AGENT_BROWSER_EXECUTABLE_PATH="$(find "$PLAYWRIGHT_BROWSERS_PATH" -type f -executable \
    \( -name chrome-headless-shell -o -name headless_shell \) 2>/dev/null | head -1)"
  [[ -n "$AGENT_BROWSER_EXECUTABLE_PATH" ]] && export AGENT_BROWSER_EXECUTABLE_PATH
fi

# Host Chrome over CDP, dialled by IP and bypassing the proxy.
# SPEC: _spec/internal/sbx/host-browser-cdp.puml
if [[ -n "${PROVEO_HOST_CDP_PORT:-}" ]]; then
  host_ip="$(getent ahostsv4 host.docker.internal 2>/dev/null | awk 'NR==1{print $1}')"
  if [[ -n "$host_ip" ]]; then
    export BROWSER_CDP_URL="http://${host_ip}:${PROVEO_HOST_CDP_PORT}"
    export NO_PROXY="${NO_PROXY:+${NO_PROXY},}${host_ip}" no_proxy="${no_proxy:+${no_proxy},}${host_ip}"
    echo "🌐 hermes drives the host Chrome at ${BROWSER_CDP_URL}"
  else
    echo "⚠️  host Chrome: host.docker.internal does not resolve — the browser tool stays in-sandbox" >&2
  fi
fi

# /command holds s6's tools (s6-setuidgid); s6's own /init puts it on PATH, and
# this entrypoint replaces /init, so the hook needs it supplied here.
if [[ -x /opt/hermes/docker/stage2-hook.sh ]]; then
  PATH="/command:${PATH}" /opt/hermes/docker/stage2-hook.sh
fi

proveo_seed hermes || true

# Nous guest tier off; hermes's own llama.cpp off (no GPU in the VM — local models run on the host's Ollama).
# SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (NOUS GUEST), _spec/internal/sbx/host-inference.puml (BEST HOST MODEL)
for setting in nous.guest local_runtime.enabled; do
  /opt/hermes/bin/hermes config set "$setting" false >/dev/null 2>&1 \
    || echo "⚠️  could not set ${setting}=false in hermes config" >&2
done

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
  # CPU-only local inference can outlast hermes's 900s local stall ceiling.
  export HERMES_LOCAL_STREAM_STALE_TIMEOUT="${HERMES_LOCAL_STREAM_STALE_TIMEOUT:-3600}"
  echo "🧩 Wired hermes to ${model} at ${base_v1} (provider custom)"
}

configure_hermes_local_model() {
  [[ -n "${PROVEO_LOCAL_MODEL:-}" ]] || return 0
  local base="${OLLAMA_API_BASE:-http://host.docker.internal:11434}"
  wire_hermes_local_endpoint "${base%/}/v1" "${PROVEO_LOCAL_MODEL}"
}
configure_hermes_local_model

# One key per provider in harness.manifest `capabilities.providers`.
HERMES_KEY_VARS=(ANTHROPIC_API_KEY AWS_BEARER_TOKEN_BEDROCK AWS_ACCESS_KEY_ID DEEPINFRA_API_KEY
  DEEPSEEK_API_KEY FIREWORKS_API_KEY GMI_API_KEY GEMINI_API_KEY GOOGLE_API_KEY HF_TOKEN
  HUGGINGFACE_API_KEY MINIMAX_API_KEY NEBIUS_API_KEY NOVITA_API_KEY OPENAI_API_KEY
  OPENROUTER_API_KEY XAI_API_KEY ZAI_API_KEY)
has_api_key() {
  local v
  for v in "${HERMES_KEY_VARS[@]}"; do
    [[ -n "${!v:-}" ]] && return 0
  done
  return 1
}

if [[ "$HERMES_LOCAL_WIRED" != 1 ]] && ! has_api_key; then
  echo "⚠️  No provider API key detected, and no local model."
  echo "   Export a provider key (e.g. OPENROUTER_API_KEY, ANTHROPIC_API_KEY, OPENAI_API_KEY),"
  echo "   or pick Qwen / Muse Glimmer in the model row (--local-model)."
fi

echo "hermes version: $(command_version_opencode hermes unknown --version)"
echo "Paradigm: single foreground Hermes session — native browser tool, no stealth tier"
echo "Browser real-profile-reuse is OFF unless explicitly configured — this sandbox never"
echo "copies the operator's real cookies/saved logins by default."

if (( prepare_only )); then
  echo "✅ hermes configured for Hermes Desktop (HERMES_HOME=${HERMES_HOME:-/opt/data})"
  exit 0
fi

run_smoke_test "hermes"

echo "🚀 Launching hermes"
proveo_exec_agent hermes -- "$@"
