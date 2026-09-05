#!/usr/bin/env bash
# SPEC: _spec/packages/lib/steps.puml, _spec/packages/lib/language-server-provisioning.puml, _spec/_paradigms/runtime-user-boundary.puml, _spec/cmd/proveo-entrypoint/prep-process-boundary.puml, _spec/_runtimes/toolchain-provisioning.puml, _spec/internal/entrypoint/model-alias-bridges.puml, _spec/internal/sbx/state-sync.puml, _spec/internal/sbx/seed-node-version-abort.puml, _spec/packages/lib/seed-and-launch.puml

ensure_runtime_user() {
 local uid gid
 uid="$(id -u)"; gid="$(id -g)"

 if ! getent passwd "$uid" >/dev/null 2>&1 && [[ -w /etc/passwd ]]; then
 printf 'agent:x:%s:%s:agent:%s:/bin/bash\n' "$uid" "$gid" "${HOME:-/tmp}" >> /etc/passwd
 fi

 if [[ -z "${HOME:-}" || ! -w "${HOME:-/}" ]]; then
 export HOME=/tmp
 fi
}

if [[ -z "${HOME:-}" || ! -w "${HOME:-/}" ]]; then
 export HOME=/tmp
fi

set_working_directory() {
 local default_dir="${1:-/app}"
 local wd="${PROVEO_WORKDIR:-}"
 if [[ -n "$wd" && -d "$wd" ]]; then
  cd "$wd" || return 0
  return 0
 fi
 if [[ -d "$default_dir" ]]; then
  cd "$default_dir" || return 0
 fi
}

accept_workspace_trust() {
 local dir="${1:-$PWD}" home
 home="$(_proveo_agent_home)"
 [[ -n "$home" && -d "$home" ]] || return 0
 command -v node >/dev/null 2>&1 || return 0
 PROVEO_TRUST_DIR="$dir" PROVEO_AGENT_HOME="$home" node -e '
   const fs = require("fs"), path = process.env.PROVEO_AGENT_HOME + "/.claude.json";
   let j = {};
   try { j = JSON.parse(fs.readFileSync(path, "utf8")) || {}; } catch (e) {}
   j.projects = j.projects || {};
   const d = process.env.PROVEO_TRUST_DIR;
   j.projects[d] = Object.assign({}, j.projects[d], { hasTrustDialogAccepted: true });
   fs.writeFileSync(path, JSON.stringify(j, null, 2));
 ' 2>/dev/null || true
}

find_env_file() {
 if [[ -f .env ]]; then
 printf '%s/.env' "$(pwd)"
 return 0
 fi

 if command -v git >/dev/null 2>&1; then
 local git_root
 git_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
 if [[ -n "$git_root" && -f "$git_root/.env" ]]; then
 printf '%s' "$git_root/.env"
 return 0
 fi
 fi

 local dir; dir="$(pwd)"
 while [[ "$dir" != "/" ]]; do
 if [[ -d "$dir/.git" && -f "$dir/.env" ]]; then
 printf '%s/.env' "$dir"
 return 0
 fi
 dir="$(dirname "$dir")"
 done

 local sub_env
 sub_env=$(find . -maxdepth 5 -name .env -not -path '*/node_modules/*' -not -path '*/.*/*' -print -quit 2>/dev/null)
 if [[ -n "$sub_env" && -f "$sub_env" ]]; then
 printf '%s/%s' "$(pwd)" "${sub_env#./}"
 return 0
 fi

 return 1
}

load_env() {
 local quiet=0
 [[ "${1:-}" == "quiet" ]] && quiet=1
 say() { (( quiet )) || echo "$@"; }

 case "$(printf '%s' "${PROVEO_EGRESS_MODE:-}" | tr '[:upper:]' '[:lower:]')" in
 open|allowlist|review)
 say "🔒 Skipping .env load (egress mode ${PROVEO_EGRESS_MODE} — secrets stay on host / broker)"
 apply_broker_sentinel
 unset -f say
 return 0
 ;;
 esac

 local env_path; env_path="$(find_env_file || true)"
 if [[ -n "$env_path" ]]; then
 say "✅ Found .env at $env_path"
 set -a
 # shellcheck source=/dev/null
 source "$env_path"
 set +a
 say "✅ Loaded environment variables from .env"
 else
 say "🔎 No .env found"
 fi
 unset -f say

 if [[ -z "${GOOGLE_GENERATIVE_AI_API_KEY:-}" ]]; then
 if [[ -n "${GEMINI_API_KEY:-}" ]]; then
 export GOOGLE_GENERATIVE_AI_API_KEY="$GEMINI_API_KEY"
 elif [[ -n "${GOOGLE_API_KEY:-}" ]]; then
 export GOOGLE_GENERATIVE_AI_API_KEY="$GOOGLE_API_KEY"
 fi
 fi

 if [[ -n "${GOOGLE_GENERATIVE_AI_API_KEY:-}" ]]; then
 export GEMINI_API_KEY="${GEMINI_API_KEY:-$GOOGLE_GENERATIVE_AI_API_KEY}"
 export GOOGLE_API_KEY="${GOOGLE_API_KEY:-$GOOGLE_GENERATIVE_AI_API_KEY}"
 fi

 apply_broker_sentinel
}

apply_broker_sentinel() {
 case "$(printf '%s' "${PROVEO_EGRESS_MODE:-}" | tr '[:upper:]' '[:lower:]')" in
 open|allowlist|review) ;;
 *) return 0 ;;
 esac
 local keys="${PROVEO_CREDENTIAL_BROKER_KEYS:-}"
 [[ -n "$keys" ]] || return 0
 local sentinel="${PROVEO_BROKER_SENTINEL:-proveo-brokered}"
 local k IFS=','
 for k in $keys; do
 k="$(printf '%s' "$k" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
 [[ -n "$k" ]] || continue
 if [[ -n "${!k:-}" && "${!k}" != "$sentinel" ]]; then
 export "$k=$sentinel"
 fi
 done
 echo "🔒 Broker sentinel applied to: $keys"
}

bridge_git_identity() {
 command -v git >/dev/null 2>&1 || return 0

 local dir="${1:-$(pwd)}"
 local name email idx
 name="${GIT_AUTHOR_NAME:-${GIT_COMMITTER_NAME:-}}"
 email="${GIT_AUTHOR_EMAIL:-${GIT_COMMITTER_EMAIL:-}}"
 idx="${GIT_CONFIG_COUNT:-0}"

 if [[ -n "$name" ]] && ! git -C "$dir" config --get user.name >/dev/null 2>&1; then
 export "GIT_CONFIG_KEY_${idx}=user.name" "GIT_CONFIG_VALUE_${idx}=$name"
 idx=$((idx + 1))
 fi

 if [[ -n "$email" ]] && ! git -C "$dir" config --get user.email >/dev/null 2>&1; then
 export "GIT_CONFIG_KEY_${idx}=user.email" "GIT_CONFIG_VALUE_${idx}=$email"
 idx=$((idx + 1))
 fi

 if (( idx > ${GIT_CONFIG_COUNT:-0} )); then
 export GIT_CONFIG_COUNT="$idx"
 fi
}

report_git_context() {
 command -v git >/dev/null 2>&1 || return 0

 local dir="${1:-$(pwd)}"

 if git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
 echo "✅ Git repository at $(git -C "$dir" rev-parse --show-toplevel)"
 local origin
 if origin="$(git -C "$dir" remote get-url origin 2>/dev/null)" && [[ -n "$origin" ]]; then
 echo "✅ Remote origin: $origin"
 else
 echo "🔎 Not tracking a remote repo"
 fi
 else
 echo "🔎 Not a git repository: $dir"
 fi

 local id_name id_email
 id_name="$(git -C "$dir" config --get user.name 2>/dev/null || true)"
 id_email="$(git -C "$dir" config --get user.email 2>/dev/null || true)"
 if [[ -n "$id_name" || -n "$id_email" ]]; then
 echo "✅ Git identity: ${id_name:-unset} <${id_email:-unset}>"
 else
 echo "🔎 No git identity (provide GIT_AUTHOR_NAME/GIT_AUTHOR_EMAIL)"
 fi

 if command -v gh >/dev/null 2>&1; then
 if timeout 5s gh auth status >/dev/null 2>&1; then
 echo "✅ gh session authenticated"
 else
 echo "🔎 gh session not authenticated (set GH_TOKEN or GITHUB_TOKEN)"
 fi
 fi
}

# SPEC: _spec/internal/workspace/git-mount-by-scope.puml
ensure_git_safe_directory() {
  command -v git >/dev/null 2>&1 || return 0
  local dir="${1:-$(pwd)}" idx
  [[ -e "${dir}/.git" || -d /app/.git ]] || return 0
  git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1 && return 0

  idx="${GIT_CONFIG_COUNT:-0}"
  export "GIT_CONFIG_KEY_${idx}=safe.directory" "GIT_CONFIG_VALUE_${idx}=$dir"
  idx=$((idx + 1))
  export GIT_CONFIG_COUNT="$idx"
  if git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "🔐 git: declared ${dir} safe (worktree owner differs from the run-as uid)"
  fi
}

scope_git_worktree() {
  [[ -n "${PROVEO_SCOPE_REL:-}" ]] || return 0
  command -v git >/dev/null 2>&1 || return 0

  local dir="${1:-$(pwd)}" idx="${HOME}/.cache/proveo/scoped-index"
  git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1 || return 0

  mkdir -p "$(dirname "$idx")" || return 0
  local real
  real="$(git -C "$dir" rev-parse --git-dir 2>/dev/null)/index"
  [[ -f "$real" ]] || return 0
  cp "$real" "$idx" 2>/dev/null || return 0
  export GIT_INDEX_FILE="$idx"

  local missing
  missing="$(cd "$dir" && git ls-files -z | while IFS= read -r -d '' f; do
    [[ -e "$f" ]] || printf '%s\0' "$f"
  done | tr '\0' '\n' | wc -l | tr -d ' ')"
  if [[ "$missing" == "0" ]]; then
    return 0
  fi
  (cd "$dir" && git ls-files -z | while IFS= read -r -d '' f; do
    [[ -e "$f" ]] || printf '%s\0' "$f"
  done | xargs -0 -r git update-index --skip-worktree) 2>/dev/null \
    || { echo "⚠️  Could not scope the git index; status will list unmounted paths as deleted" >&2; return 0; }

  echo "🔭 git scoped to ${PROVEO_SCOPE_REL} (${missing} unmounted path(s) hidden; host .git untouched)"
}

attach_rtk() {
 if [[ "${ATTACH_RTK:-0}" =~ ^(1|true|yes|on)$ && ! -d rtk ]]; then
 if [[ ! -w . ]]; then
 echo "⚠️ Current directory $(pwd) is not writable; skipping RTK attachment."
 return 0
 fi
 echo "🔄 Attaching RTK repository..."
 git clone --depth 1 https://github.com/rtk-ai/rtk.git rtk || true
 fi
}

run_smoke_test() {
 local target_name="$1"
 if [[ "${PROVEO_SMOKE_TEST:-0}" == "1" ]]; then
 echo "✅ PROVEO_SMOKE_READY ${PROVEO_SMOKE_TARGET:-$target_name}"
 exec sleep infinity
 fi
}

command_version_cecli() {
 local fallback="$1"; shift
 timeout 5s "$@" 2>/dev/null || echo "$fallback"
}

command_version_opencode() {
 local name="$1"; shift
 local fallback="${1:-n/a}"; shift || true
 if ! command -v "$name" >/dev/null 2>&1; then
 echo "$fallback"
 return 0
 fi
 timeout 5s "$name" "$@" 2>/dev/null || echo "$fallback"
}

_normalize_model() {
 local m="$1" lower
 [[ -n "$m" ]] || return 0
 case "$m" in */*) printf '%s' "$m"; return 0 ;; esac
 lower="$(printf '%s' "$m" | tr '[:upper:]' '[:lower:]')"
 case "$lower" in
 gpt-* | chatgpt-* | o[0-9]*) printf 'openai/%s' "$m" ;;
 claude-*) printf 'anthropic/%s' "$m" ;;
 grok-*) printf 'xai/%s' "$m" ;;
 gemini-*) printf 'google/%s' "$m" ;;
 deepseek-*) printf 'deepseek/%s' "$m" ;;
 *) printf '%s' "$m" ;;
 esac
}

_model_provider() {
 local n
 n="$(_normalize_model "$1")"
 case "$n" in
 ollama/* | ollama_chat/* | openai-compatible/*) printf '' ;;
 */*) printf '%s' "${n%%/*}" ;;
 *) printf '' ;;
 esac
}

_apply_env_bridge() {
 local from="$1" to="$2" fallback="$3" default="$4" transform="$5" val
 printenv "$to" >/dev/null 2>&1 && return 0
 val="$(printenv "$from" 2>/dev/null || true)"
 [[ -z "$val" && -n "$fallback" ]] && val="$(printenv "$fallback" 2>/dev/null || true)"
 if [[ -z "$val" && -n "$default" ]]; then
  case "$default" in
  '$'*) val="$(printenv "${default#\$}" 2>/dev/null || true)" ;;
  *) val="$default" ;;
  esac
 fi
 [[ -n "$val" ]] || return 0
 [[ "$transform" == normalize ]] && val="$(_normalize_model "$val")"
 export "$to=$val"
}

apply_env_bridges() {
 _apply_env_bridge GEMINI_API_KEY GOOGLE_GENERATIVE_AI_API_KEY "" "" ""
 _apply_env_bridge GOOGLE_API_KEY GOOGLE_GENERATIVE_AI_API_KEY "" "" ""
}

_apply_model_bridge() {
 local targets="$1" roles="$2" default="$3" transform="$4" want="$5"
 local val="" r t got
 local -a role_list target_list
 IFS=',' read -ra role_list <<< "$roles"
 for r in "${role_list[@]}"; do
  val="$(printenv "$r" 2>/dev/null || true)"
  [[ -n "$val" ]] && break
 done
 if [[ -z "$val" && -n "$default" && "$default" != "-" ]]; then
  case "$default" in
  '$'*) val="$(printenv "${default#\$}" 2>/dev/null || true)" ;;
  *) val="$default" ;;
  esac
 fi
 [[ -n "$val" ]] || return 0
 # SPEC: _spec/internal/entrypoint/model-alias-bridges.puml
 if [[ -n "$want" && "$want" != "-" ]]; then
  got="$(_model_provider "$val")"
  if [[ -n "$got" && "$got" != "$want" ]]; then
   echo "⚠️  model: $targets takes models from $want only — refusing $val, which resolves to $got. The slot is left unset, so the agent falls back to its own default; name a model from $want, or run a harness that accepts $got." >&2
   return 0
  fi
 fi
 case "$transform" in
 normalize) val="$(_normalize_model "$val")" ;;
 bare) val="${val##*/}" ;;
 esac
 IFS=',' read -ra target_list <<< "$targets"
 for t in "${target_list[@]}"; do
  printenv "$t" >/dev/null 2>&1 && continue
  export "$t=$val"
 done
}

apply_model_bridges() {
 local harness="$1"
 local file="${PROVEO_BRIDGES_DIR:-/opt/proveo/bridges}/$harness.tsv"
 [[ -f "$file" ]] || return 0
 local slot targets roles default transform want
 while IFS=$'\t' read -r slot targets roles default transform want; do
  [[ -n "$slot" && "$slot" != \#* ]] || continue
  _apply_model_bridge "$targets" "$roles" "$default" "$transform" "$want"
 done < "$file"
}

_proveo_auto_install_enabled() {
  case "$(printf '%s' "${PROVEO_AUTO_INSTALL_TOOLS:-true}" | tr '[:upper:]' '[:lower:]')" in
    false|0|no|off|disable|disabled) return 1 ;;
  esac
  return 0
}

# SPEC: _spec/_plans/config-seeding-and-persistence.puml

_proveo_durable_home() {
  local d="${PROVEO_STATE_HOME:-}"
  [ -n "$d" ] || d="$(_proveo_agent_home)"
  printf '%s' "$d"
}

_proveo_container_platform() {
  local m
  m="$(uname -m 2>/dev/null || echo unknown)"
  [ -n "$m" ] || m=unknown
  case "$m" in
    x86_64 | amd64) m=amd64 ;;
    aarch64 | arm64) m=arm64 ;;
  esac
  printf 'linux-%s' "$m"
}

_proveo_tool_rel() { printf 'toolchains/%s' "$(_proveo_container_platform)"; }

# SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
_proveo_tool_home() {
  if [ -n "${_PROVEO_TOOL_HOME:-}" ]; then printf '%s' "$_PROVEO_TOOL_HOME"; return 0; fi
  local t
  t="$(_proveo_agent_home)/$(_proveo_tool_rel)"
  mkdir -p "$t/.local/bin" 2>/dev/null || true
  _PROVEO_TOOL_HOME="$t"
  export _PROVEO_TOOL_HOME
  printf '%s' "$t"
}

_proveo_tool_store() {
  case "$(printf '%s' "${PROVEO_TOOL_SYNC:-on}" | tr '[:upper:]' '[:lower:]')" in
  off | false | 0 | no | disable | disabled) return 0 ;;
  esac
  local host="${PROVEO_STATE_HOME:-}"
  [ -n "$host" ] && [ -d "$host" ] || return 0
  printf '%s/%s' "$host" "$(_proveo_tool_rel)"
}

proveo_sync_tools() {
  local mode="${1:-}" store home lock src dst rc=0
  case "$mode" in
  restore | save) ;;
  *) return 2 ;;
  esac
  store="$(_proveo_tool_store)"
  [ -n "$store" ] || return 0
  home="$(_proveo_tool_home)"
  [ -n "$home" ] || return 0

  case "$mode" in
  restore) src="$store" dst="$home" ;;
  save) src="$home" dst="$store" ;;
  esac
  [ -d "$src" ] || return 0
  [ -n "$(ls -A "$src" 2>/dev/null)" ] || return 0

  lock="$(_proveo_agent_home)/.proveo-tools.lock"
  if ! _proveo_sync_lock "$lock"; then
    printf 'proveo: toolchain %s skipped — another sync still holds the lock\n' "$mode" >&2
    return 1
  fi
  _proveo_sync_tree "$src" "$dst" || rc=1
  rm -rf "$lock" 2>/dev/null
  ((rc == 0)) || printf 'proveo: toolchain %s completed with copy errors\n' "$mode" >&2
  return "$rc"
}

_proveo_tool_path() {
  local t
  t="$(_proveo_tool_home)"
  export MISE_DATA_DIR="$t/.local/share/mise"
  export MISE_CONFIG_DIR="$t/.config/mise"
  export MISE_STATE_DIR="$t/.local/state/mise"
  export MISE_CACHE_DIR="$t/.cache/mise"
  case ":${PATH}:" in
    *":$t/.local/bin:"*) ;;
    *) export PATH="$t/.local/bin:${PATH}" ;;
  esac
  case ":${PATH}:" in
    *":${MISE_DATA_DIR}/shims:"*) ;;
    *) export PATH="${MISE_DATA_DIR}/shims:${PATH}" ;;
  esac
  export DOTNET_SYSTEM_GLOBALIZATION_INVARIANT="${DOTNET_SYSTEM_GLOBALIZATION_INVARIANT:-1}"
}

_proveo_bounded() {
  local secs="$1"; shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$secs" "$@"
  else
    "$@"
  fi
}

_proveo_github_token() {
  if [ -n "${GITHUB_TOKEN:-}" ]; then printf '%s' "$GITHUB_TOKEN"; return 0; fi
  if [ -n "${GH_TOKEN:-}" ]; then printf '%s' "$GH_TOKEN"; return 0; fi
  command -v gh >/dev/null 2>&1 || return 0
  _proveo_bounded 5 gh auth token 2>/dev/null | head -n1 || return 0
}

_mise_install() {
  local spec="$1" tok="${2:-}"
  if [ -n "$tok" ]; then
    MISE_YES=1 GITHUB_TOKEN="$tok" \
      _proveo_bounded "${PROVEO_LSP_INSTALL_TIMEOUT:-180}" mise use -g "$spec" 2>&1
  else
    MISE_YES=1 \
      _proveo_bounded "${PROVEO_LSP_INSTALL_TIMEOUT:-180}" mise use -g "$spec" 2>&1
  fi
}

_go_current_version() {
  command -v go >/dev/null 2>&1 || return 0
  local v
  v="$(go env GOVERSION 2>/dev/null)"
  printf '%s' "${v#go}"
}

# SPEC: _spec/_runtimes/toolchain-provisioning.puml
_install_go() {
  local version="$1" out
  if ! command -v mise >/dev/null 2>&1; then
    echo "WARN: mise is not on PATH; Go ${version} not installed"
    return 0
  fi
  if ! out="$(_mise_install "go@${version}")"; then
    echo "WARN: mise could not install Go ${version}: ${out}"
  fi
}

_proveo_lock_installs() {
  command -v flock >/dev/null 2>&1 || return 0
  local dir
  dir="$(_proveo_tool_home)/.local/share/proveo"
  mkdir -p "$dir" 2>/dev/null || return 0
  if ! : >>"${dir}/install.lock" 2>/dev/null; then return 0; fi
  exec 9>>"${dir}/install.lock"
  if ! flock -w "${PROVEO_INSTALL_LOCK_WAIT:-300}" 9; then
    echo "⏳ another proveo run is provisioning tools under $(_proveo_tool_home); skipping installs this run"
    _proveo_unlock_installs
    return 1
  fi
  return 0
}

_proveo_unlock_installs() { exec 9>&-; }

ensure_project_tools() {
 _proveo_tool_path
 _proveo_auto_install_enabled || return 0
 _proveo_lock_installs || return 0

 local -a npm_net=(--fetch-timeout=60000 --fetch-retries=1)
 local tool_home; tool_home="$(_proveo_tool_home)"

 if [[ -f nx.json ]]; then
 if ! command -v nx >/dev/null 2>&1; then
 echo "📦 Detected nx.json. Dynamically installing nx..."
 npm install -g "${npm_net[@]}" --prefix "${tool_home}/.local" nx@latest || echo "⚠️ Failed to dynamically install nx"
 fi
 fi

 if [[ -f turbo.json ]]; then
 if ! command -v turbo >/dev/null 2>&1; then
 echo "📦 Detected turbo.json. Dynamically installing turbo..."
 npm install -g "${npm_net[@]}" --prefix "${tool_home}/.local" turbo@latest || echo "⚠️ Failed to dynamically install turbo"
 fi
 fi

 if [[ -f mise.toml || -f mise.local.toml || -f .mise.toml || -f .mise.local.toml || -d mise || -d .mise || -f .tool-versions ]]; then
 if ! command -v mise >/dev/null 2>&1; then
 echo "📦 Detected mise config or .tool-versions. Dynamically installing mise..."
 local mise_installer
 mise_installer="$(mktemp)"
 if curl -fsSL --connect-timeout 5 --max-time 120 https://mise.run -o "$mise_installer"; then
 MISE_INSTALL_PATH="${tool_home}/.local/bin/mise" sh "$mise_installer" || echo "⚠️ mise install script failed"
 else
 npm install -g "${npm_net[@]}" --prefix "${tool_home}/.local" @jdx/mise@latest || echo "⚠️ Failed to dynamically install mise"
 fi
 rm -f "$mise_installer"
 fi
 fi

 if [[ -f go.mod || -f go.work ]] || compgen -G "*.go" >/dev/null 2>&1; then
 export GOPATH="${GOPATH:-${tool_home}/go}"
 export PATH="${GOPATH}/bin:${PATH}"

 local go_version="latest" pinned="" current=""
 if [[ -f go.mod ]]; then
 pinned="$(sed -n 's/^toolchain go\([0-9][^ ]*\).*/\1/p' go.mod | head -n1)"
 [[ -n "$pinned" ]] && go_version="$pinned"
 fi
 current="$(_go_current_version)"

 if [[ -z "$current" ]]; then
 echo "Detected a Go project. Dynamically installing Go ${go_version} via mise..."
 _install_go "$go_version"
 elif [[ -n "$pinned" && "$current" != "$pinned" ]]; then
 echo "Go ${current} is installed but go.mod pins ${pinned}; installing the pinned toolchain..."
 _install_go "$pinned"
 fi

 if command -v go >/dev/null 2>&1 && ! command -v gopls >/dev/null 2>&1; then
 echo "Installing gopls for Go code intelligence..."
 go install golang.org/x/tools/gopls@latest >/dev/null 2>&1 \
   || echo "WARN: failed to install gopls"
 fi
 fi

 _proveo_unlock_installs
}

# SPEC: _spec/packages/lib/python-environment.puml
_py_project_kind() {
  local d="${1:-$(pwd)}"
  [[ -f "$d/environment.yml" || -f "$d/environment.yaml" ]] && { echo conda; return 0; }
  [[ -f "$d/uv.lock" ]] && { echo uv; return 0; }
  [[ -f "$d/poetry.lock" ]] && { echo poetry; return 0; }
  [[ -f "$d/pyproject.toml" || -f "$d/Pipfile" ]] && { echo pip; return 0; }
  compgen -G "$d/requirements*.txt" >/dev/null 2>&1 && { echo pip; return 0; }
  return 0
}

_py_requested_version() {
  local d="${1:-$(pwd)}" v=""
  [[ -f "$d/.python-version" ]] && v="$(head -n1 "$d/.python-version" 2>/dev/null | tr -d '[:space:]')"
  if [[ -z "$v" && -f "$d/pyproject.toml" ]]; then
    v="$(sed -n 's/^requires-python[[:space:]]*=[[:space:]]*"[^0-9]*\([0-9]\+\.[0-9]\+\).*/\1/p' "$d/pyproject.toml" | head -n1)"
  fi
  printf '%s' "${v:-${PROVEO_PYTHON_VERSION:-3.12}}"
}

_py_venv_capable() {
  command -v python3 >/dev/null 2>&1 || return 1
  local probe; probe="$(mktemp -d)/v"
  python3 -m venv "$probe" >/dev/null 2>&1 || { rm -rf "$(dirname "$probe")"; return 1; }
  rm -rf "$(dirname "$probe")"
  return 0
}

_py_env_dir() {
  local d="${1:-$(pwd)}" h
  h="$(printf '%s' "$d" | cksum | cut -d' ' -f1)"
  printf '%s' "${HOME}/.cache/proveo/venv/${h}"
}

_proveo_project_roots() {
  local scan="$1" depth="$2"; shift 2
  local f d rel seps n tab m first=1
  local pat=()
  for m in "$@"; do
    if [[ $first -eq 1 ]]; then pat+=( -name "$m" ); first=0
    else pat+=( -o -name "$m" ); fi
  done
  [[ ${#pat[@]} -gt 0 ]] || return 0
  tab="$(printf '\t')"
  _proveo_walk "$scan" -type f "(" "${pat[@]}" ")" -print \
    | while IFS= read -r f; do
        d="${f%/*}"
        rel="${d#"$scan"}"; rel="${rel#/}"
        if [[ -z "$rel" ]]; then
          n=0
        else
          seps="${rel//[!\/]/}"; n=$(( ${#seps} + 1 ))
        fi
        [[ "$n" -le "$depth" ]] && printf '%s%s%s\n' "$n" "$tab" "$d"
      done \
    | sort -t"$tab" -k1,1n -k2,2 | cut -f2- | awk '!seen[$0]++'
}

_py_project_roots() {
  _proveo_project_roots "${1:-$(pwd)}" "${PROVEO_PYTHON_SCAN_DEPTH:-${PROVEO_DEP_SCAN_DEPTH:-4}}" \
    pyproject.toml poetry.lock uv.lock Pipfile environment.yml environment.yaml 'requirements*.txt'
}

_py_provision_one() {
  local kind="$1" root="$2" ver env_dir spec
  ver="$(_py_requested_version "$root")"
  env_dir="$(_py_env_dir "$root")"

  if ! _py_venv_capable; then
    echo "🐍 Provisioning Python ${ver} (the image's python3 cannot create a venv)..."
    _mise_install "python@${ver}" "$(_proveo_github_token)" >/dev/null 2>&1 \
      || { echo "⚠️  Could not provision Python ${ver}; skipping environment setup"; return 1; }
    _proveo_tool_path
  fi

  case "$kind" in
    uv)     spec=uv ;;
    poetry) spec=poetry ;;
    conda)  spec=micromamba ;;
    *)      spec="" ;;
  esac
  if [[ -n "$spec" ]] && ! command -v "$spec" >/dev/null 2>&1; then
    _mise_install "$spec" "$(_proveo_github_token)" >/dev/null 2>&1 \
      || echo "⚠️  Could not provision ${spec}; falling back to pip"
    _proveo_tool_path
  fi

  _py_build_env "$kind" "$root" "$env_dir"
}

ensure_python_env() {
  _proveo_tool_path
  local scan="${1:-$(pwd)}" roots root kind primary="" deferred="" others="" max n=0
  _proveo_auto_install_enabled || return 0
  case "$(printf '%s' "${PROVEO_PYTHON_ENV:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  roots="$(_py_project_roots "$scan")"
  [[ -n "$roots" ]] || return 0
  command -v mise >/dev/null 2>&1 || { echo "ℹ️  mise not on PATH — skipping Python environment"; return 0; }

  max="${PROVEO_PYTHON_MAX_PROJECTS:-4}"
  _proveo_lock_installs || return 0
  while IFS= read -r root; do
    [[ -n "$root" ]] || continue
    kind="$(_py_project_kind "$root")"
    [[ -n "$kind" ]] || continue
    n=$(( n + 1 ))
    if [[ "$n" -gt "$max" ]]; then
      deferred="${deferred} ${root#"$scan"/}"
      continue
    fi
    _py_provision_one "$kind" "$root" || break
    if [[ -z "$primary" ]]; then
      primary="$root"
    else
      others="${others} ${root#"$scan"/}"
    fi
  done <<< "$roots"
  _proveo_unlock_installs

  [[ -n "$primary" ]] || return 0
  _py_activate "$(_py_env_dir "$primary")" "$primary"
  [[ -n "$others" ]] && echo "🐍 Also provisioned (not activated):${others}"
  [[ -n "$deferred" ]] && \
    echo "ℹ️  Python projects past PROVEO_PYTHON_MAX_PROJECTS=${max}, not provisioned:${deferred}"
  return 0
}

_py_build_env() {
  local kind="$1" scan="$2" env_dir="$3" rc=0
  mkdir -p "$(dirname "$env_dir")" 2>/dev/null || return 0
  local bounded="${PROVEO_PYTHON_TIMEOUT:-600}"
  case "$kind" in
    conda)
      [[ -x "$env_dir/bin/python" ]] && return 0
      _proveo_bounded "$bounded" micromamba create -y -q -p "$env_dir" \
        -f "$(ls "$scan"/environment.y*ml 2>/dev/null | head -n1)" >/dev/null 2>&1 || rc=$?
      ;;
    uv)
      [[ -x "$env_dir/bin/python" ]] || _proveo_bounded "$bounded" uv venv "$env_dir" >/dev/null 2>&1
      (cd "$scan" && _proveo_bounded "$bounded" env VIRTUAL_ENV="$env_dir" uv sync --active) >/dev/null 2>&1 || rc=$?
      ;;
    poetry)
      [[ -x "$env_dir/bin/python" ]] || _proveo_bounded "$bounded" python3 -m venv "$env_dir" >/dev/null 2>&1
      (cd "$scan" && _proveo_bounded "$bounded" env VIRTUAL_ENV="$env_dir" poetry install --no-root) >/dev/null 2>&1 || rc=$?
      ;;
    *)
      [[ -x "$env_dir/bin/python" ]] || _proveo_bounded "$bounded" python3 -m venv "$env_dir" >/dev/null 2>&1
      local req; req="$(ls "$scan"/requirements*.txt 2>/dev/null | head -n1)"
      if [[ -n "$req" ]]; then
        _proveo_bounded "$bounded" "$env_dir/bin/pip" install -q -r "$req" >/dev/null 2>&1 || rc=$?
      elif [[ -f "$scan/pyproject.toml" ]]; then
        _proveo_bounded "$bounded" "$env_dir/bin/pip" install -q -e "$scan" >/dev/null 2>&1 || rc=$?
      fi
      ;;
  esac
  if [[ $rc -ne 0 ]]; then
    echo "⚠️  Python dependency install did not complete; the environment may be partial"
    if ! command -v cc >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1; then
      echo "   No C toolchain in this image — a dependency with no linux-$(uname -m) wheel"
      echo "   cannot build from source here. Pin a version that publishes one."
    fi
  fi
  return 0
}

_py_activate() {
  local env_dir="$1" scan="$2"
  [[ -x "$env_dir/bin/python" ]] || { echo "⚠️  No usable Python environment at ${env_dir}"; return 0; }
  export VIRTUAL_ENV="$env_dir"
  case ":${PATH}:" in
    *":${env_dir}/bin:"*) ;;
    *) export PATH="${env_dir}/bin:${PATH}" ;;
  esac
  if [[ -e "$scan/.venv" && ! -x "$scan/.venv/bin/python" ]]; then
    echo "⚠️  ${scan}/.venv is a host-built environment and cannot run here; using ${env_dir} instead"
  fi
  echo "🐍 Python: $("$env_dir/bin/python" --version 2>&1) · ${scan} · env ${env_dir}"
}

# SPEC: _spec/packages/lib/dependency-trees.puml

_dep_lang_class() { case "$1" in
  typescript|ruby|lua|terraform) REPLY=addons ;;
  rust|cpp|zig)                  REPLY=artifacts ;;
  python)                        REPLY=provisioned ;;
  go|java|kotlin|nix)            REPLY=portable ;;
  bash|css|docker|html|json|markdown|mermaid|plantuml|toml|yaml) REPLY=none ;;
  *)                             REPLY="" ;;
esac; }

_dep_langs() { echo "typescript python ruby lua terraform rust cpp zig go java kotlin nix"; }

_dep_lang_markers() { case "$1" in
  typescript) echo "package.json" ;;
  python)     echo "pyproject.toml requirements*.txt Pipfile uv.lock poetry.lock environment.yml environment.yaml" ;;
  ruby)       echo "Gemfile" ;;
  lua)        echo "*.rockspec" ;;
  terraform)  echo ".terraform.lock.hcl" ;;
  rust)       echo "Cargo.toml" ;;
  cpp)        echo "CMakeLists.txt meson.build" ;;
  zig)        echo "build.zig" ;;
  go)         echo "go.mod" ;;
esac; }

_dep_lang_dirs() { case "$1" in
  typescript) echo "node_modules" ;;
  python)     echo ".venv venv" ;;
  ruby)       echo "vendor/bundle" ;;
  lua)        echo "lua_modules" ;;
  terraform)  echo ".terraform" ;;
  rust)       echo "target" ;;
  cpp)        echo "build" ;;
  zig)        echo "zig-cache .zig-cache zig-out" ;;
esac; }

_dep_lang_binaries() { case "$1" in
  typescript) echo "*.node" ;;
  ruby)       echo "*.so *.bundle" ;;
  lua)        echo "*.so *.dylib" ;;
  terraform)  echo "terraform-provider-*" ;;
  rust|cpp|zig) echo "*.o *.so *.dylib" ;;
esac; }

_dep_install_cmd() { local lang="$1" d="$2" spec; case "$lang" in
  typescript)
    [[ -f "$d/pnpm-lock.yaml" ]] && { echo "pnpm install --frozen-lockfile"; return 0; }
    [[ -f "$d/bun.lockb" || -f "$d/bun.lock" ]] && { echo "bun install --frozen-lockfile"; return 0; }
    [[ -f "$d/yarn.lock" ]] && { echo "yarn install --immutable"; return 0; }
    [[ -f "$d/package-lock.json" ]] && { echo "npm ci"; return 0; }
    echo "npm install" ;;
  ruby)      echo "bundle install" ;;
  lua)
    spec="$(ls "$d"/*.rockspec 2>/dev/null | head -n1)"
    [[ -n "$spec" ]] && echo "luarocks --tree lua_modules install --only-deps ${spec##*/}" ;;
  terraform) echo "terraform init -input=false" ;;   # honours .terraform.lock.hcl; -upgrade would rewrite it
  rust)      echo "cargo fetch" ;;                    # crates land in CARGO_HOME, outside the tree
  go)        echo "go mod download" ;;                # modules land in GOMODCACHE; gopls needs them present
esac; return 0; }

_dep_install_idempotent() { case "$1" in "npm ci"*) return 1 ;; esac; return 0; }

_proveo_dep_is_isolated() {
  local real
  real="$(cd "$1" 2>/dev/null && pwd -P)" || return 1
  [[ -r /proc/self/mountinfo ]] || return 1
  awk -v p="$real" '$5 == p { found = 1 } END { exit found ? 0 : 1 }' /proc/self/mountinfo
}

_dep_clear_tree() {
  local dir="$1"
  find "$dir" -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null
  return 0
}

_proveo_foreign_object() {
  local magic
  magic="$(head -c 4 "$1" 2>/dev/null | od -An -tx1 | tr -d ' \n')"
  [[ "$magic" == "7f454c46" ]] && return 1
  return 0
}

_dep_owner() {
  local lang="$1" path="$2" dir="$3" rel="${2#"$3"/}" head rest
  case "$lang" in
    typescript)
      case "$rel" in .pnpm/*) rel="${rel#.pnpm/}"; printf '%s' "${rel%%/*}"; return 0 ;; esac
      head="${rel%%/*}"
      if [[ "$head" == @* ]]; then rest="${rel#*/}"; printf '%s/%s' "$head" "${rest%%/*}"; return 0; fi
      printf '%s' "$head"; return 0 ;;
    ruby)
      case "$rel" in *gems/*) rel="${rel#*gems/}"; printf '%s' "${rel%%/*}"; return 0 ;; esac ;;
    lua)
      head="${path##*/}"; printf '%s' "${head%.*}"; return 0 ;;
    terraform)
      case "$rel" in
        providers/*/*/*)
          rel="${rel#providers/}"; rel="${rel#*/}"
          head="${rel%%/*}"; rest="${rel#*/}"
          printf '%s/%s' "$head" "${rest%%/*}"; return 0 ;;
      esac ;;
  esac
  printf '%s' "${rel%%/*}"
}

_dep_probe() {
  local dir="$1" lang="$2" cap="${PROVEO_DEPS_PROBE_LIMIT:-60}" f n=0 p first=1
  local pat=()
  for p in $(_dep_lang_binaries "$lang"); do
    if [[ $first -eq 1 ]]; then pat+=( -name "$p" ); first=0
    else pat+=( -o -name "$p" ); fi
  done
  [[ ${#pat[@]} -gt 0 ]] || return 0
  while IFS= read -r f; do
    n=$(( n + 1 ))
    [[ "$n" -gt "$cap" ]] && break
    _proveo_foreign_object "$f" || continue
    _dep_owner "$lang" "$f" "$dir"; printf '\n'
  done < <(find "$dir" -type f "(" "${pat[@]}" ")" 2>/dev/null)
  return 0
}

_dep_install() {
  local lang="$1" root="$2" why="$3" cmd bounded="${PROVEO_DEPS_TIMEOUT:-900}" rc=0
  cmd="$(_dep_install_cmd "$lang" "$root")"
  [[ -n "$cmd" ]] || return 0
  if ! command -v "${cmd%% *}" >/dev/null 2>&1; then
    echo "    ⚠️  ${cmd%% *} is not on PATH; cannot install ${lang} dependencies for ${root#"$(_proveo_scan_root)"/}"
    return 0
  fi
  echo "    📦 ${cmd} — ${why}"
  ( cd "$root" && _proveo_bounded "$bounded" $cmd ) || rc=$?
  if [[ $rc -eq 0 ]]; then
    echo "    ✅ ${lang} dependencies ready in ${root#"$(_proveo_scan_root)"/}"
  else
    echo "    ⚠️  ${cmd} failed (rc=${rc}). If the registry is not reachable under this egress tier,"
    echo "        rerun with --egress-mode open; a lockfile that disagrees with its manifest is"
    echo "        reported rather than rewritten — reconcile it and rerun."
  fi
  return 0
}

_dep_addons_tree() {
  local lang="$1" root="$2" dir="$3" mode="$4" foreign names count cmd
  foreign="$(_dep_probe "$dir" "$lang" | sort -u)"
  if [[ -n "$foreign" ]]; then
    count="$(printf '%s\n' "$foreign" | grep -c .)"
    names="$(printf '%s\n' "$foreign" | head -n 5 | paste -sd' ' -)"
    echo "⚠️  ${dir#"$root"/} was built on the host: ${count} ${lang} package(s) carry binaries"
    echo "    this platform cannot load (${names})."
    echo "    The portable majority still loads, so the failure arrives later, at the first"
    echo "    call into one of them, naming the TOOL rather than the platform."
    if _proveo_dep_is_isolated "$dir"; then
      echo "    ♻️  this is proveo's private copy; the host checkout is untouched — clearing it"
      _dep_clear_tree "$dir"
      _dep_install "$lang" "$root" "rebuilding ${dir#"$root"/} for this platform"
    else
      case "$mode" in
        reinstall) _dep_install "$lang" "$root" "rebuilding ${dir#"$root"/} IN PLACE — this rewrites the operator's checkout (PROVEO_DEPS=reinstall)" ;;
        *)
          echo "    ℹ️  this tree is the host's own (sbx mirrors the checkout): PROVEO_DEPS=reinstall rewrites it"
          echo "        in place, or run with --clone so untracked trees stay behind and the seed installs fresh" ;;
      esac
    fi
    return 0
  fi
  cmd="$(_dep_install_cmd "$lang" "$root")"
  [[ -n "$cmd" ]] && _dep_install_idempotent "$cmd" || return 0
  _dep_install "$lang" "$root" "refreshing ${dir#"$root"/} against its lockfile"
}

_dep_artifacts_tree() {
  local lang="$1" root="$2" dir="$3"
  [[ -n "$(_dep_probe "$dir" "$lang" | head -n1)" ]] || return 0
  if _proveo_dep_is_isolated "$dir"; then
    echo "♻️  ${dir#"$root"/} holds ${lang} build output from the host; clearing proveo's private copy so the toolchain rebuilds it here"
    _dep_clear_tree "$dir"
  else
    echo "ℹ️  ${dir#"$root"/} holds ${lang} build output from the host and cannot be reused here."
    echo "    The toolchain rebuilds it; remove that directory if a build reads the stale one."
  fi
  return 0
}

_ts_is_workspace_root() {
  local d="$1"
  [[ -f "$d/pnpm-workspace.yaml" ]] && return 0
  [[ -f "$d/package.json" ]] && grep -q '"workspaces"' "$d/package.json" 2>/dev/null
}

_dep_collapse_workspaces() {
  local root w skip
  local ws=()
  while IFS= read -r root; do
    [[ -n "$root" ]] || continue
    skip=0
    if [[ ${#ws[@]} -gt 0 ]]; then
      for w in "${ws[@]}"; do
        case "$root" in "$w"/*) skip=1; break ;; esac
      done
    fi
    [[ $skip -eq 1 ]] && continue
    _ts_is_workspace_root "$root" && ws+=("$root")
    printf '%s\n' "$root"
  done
  return 0
}

ensure_dependency_trees() {
  local scan="${1:-$(pwd)}" mode lang class roots root dir dirs markers primary
  _proveo_auto_install_enabled || return 0
  mode="$(printf '%s' "${PROVEO_DEPS:-auto}" | tr '[:upper:]' '[:lower:]')"
  case "$mode" in off|false|0|no|disable|disabled) return 0 ;; esac

  for lang in $(_dep_langs); do
    _dep_lang_class "$lang"; class="$REPLY"
    markers="$(_dep_lang_markers "$lang")"
    [[ -n "$markers" ]] || continue
    dirs="$(_dep_lang_dirs "$lang")"; primary="${dirs%% *}"
    roots="$(_proveo_project_roots "$scan" "${PROVEO_DEP_SCAN_DEPTH:-4}" $markers)"
    [[ -n "$roots" ]] || continue
    [[ "$lang" == typescript ]] && roots="$(printf '%s\n' "$roots" | _dep_collapse_workspaces)"
    while IFS= read -r root; do
      [[ -n "$root" ]] || continue
      case "$class" in
        addons)
          if [[ ! -d "$root/$primary" ]]; then
            echo "⚠️  ${lang} project at ${root#"$scan"/} has no ${primary} — nothing is installed here"
            _dep_install "$lang" "$root" "installing ${primary} before the agent starts"
          else
            for dir in $dirs; do
              [[ -d "$root/$dir" ]] && _dep_addons_tree "$lang" "$root" "$root/$dir" "$mode"
            done
          fi ;;
        artifacts)
          for dir in $dirs; do
            [[ -d "$root/$dir" ]] && _dep_artifacts_tree "$lang" "$root" "$root/$dir"
          done
          _dep_install "$lang" "$root" "fetching ${lang} dependencies before the agent starts" ;;
        portable)
          _dep_install "$lang" "$root" "fetching ${lang} dependencies before the agent starts" ;;
        provisioned|none|"") ;;
      esac
    done <<< "$roots"
  done
  return 0
}

proveo_exec_agent() {
  local agent="$1"; shift
  local launch=()
  while [[ $# -gt 0 && "$1" != "--" ]]; do launch+=("$1"); shift; done
  [[ "${1:-}" == "--" ]] && shift
  if [[ $# -gt 0 && "$1" != -* ]] && command -v -- "$1" >/dev/null 2>&1; then
    echo "🔁 launcher supplied its own command; running it instead of ${agent}: $1 …"
    exec "$@"
  fi
  exec "$agent" "${launch[@]}" "$@"
}

# SPEC: _spec/packages/lib/house-rules.puml

readonly PROVEO_HOUSE_RULES_FILE=/opt/proveo/AGENTS.md
readonly PROVEO_RULES_START="<!-- >>> proveo house rules (generated — edits are overwritten) >>> -->"
readonly PROVEO_RULES_END="<!-- <<< proveo house rules <<< -->"

_house_rules_target() { case "$1" in
  claudecode) echo "" ;;
  opencode)   echo ".config/opencode/AGENTS.md" ;;
  cursor|cecli) echo "" ;;
esac; }

proveo_compose_house_rules() {
  local target="${1:-}" home rel dest
  case "$(printf '%s' "${PROVEO_HOUSE_RULES:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  [[ -n "$target" && -s "$PROVEO_HOUSE_RULES_FILE" ]] || return 0
  rel="$(_house_rules_target "$target")"
  [[ -n "$rel" ]] || return 0
  home="$(_proveo_agent_home)"
  [[ -n "$home" ]] || return 0
  dest="$home/$rel"
  {
    printf '<!-- proveo house rules · source: %s -->\n\n' "$PROVEO_HOUSE_RULES_FILE"
    cat "$PROVEO_HOUSE_RULES_FILE"
    printf '\n'
  } | _proveo_write_block "$dest" "$PROVEO_RULES_START" "$PROVEO_RULES_END"
  echo "📐 house rules → ${dest} (project instructions still take precedence)"
}

# SPEC: _spec/_runtimes/toolchain-provisioning.puml

_node_nearest_pkg() {
  local d; d="$(cd "${1:-$PWD}" 2>/dev/null && pwd)" || return 0
  while [[ -n "$d" && "$d" != "/" ]]; do
    [[ -f "$d/package.json" ]] && { printf '%s' "$d/package.json"; return 0; }
    d="$(dirname "$d")"
  done
  [[ -f /package.json ]] && printf '%s' /package.json
  return 0
}

_node_json_field() {
  local file="$1" expr="$2"
  [[ -f "$file" ]] || return 0
  command -v node >/dev/null 2>&1 || return 0
  node -e '
    try {
      const p = require(process.argv[1]);
      const v = process.argv[2].split(".").reduce((o, k) => (o == null ? o : o[k]), p);
      if (v != null) process.stdout.write(String(v));
    } catch (e) {}
  ' "$file" "$expr" 2>/dev/null || return 0
}

ensure_node_toolchain() {
  local scan="${1:-$PWD}" pkg pm want_node have
  case "$(printf '%s' "${PROVEO_NODE_TOOLCHAIN:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  _proveo_auto_install_enabled || return 0
  pkg="$(_node_nearest_pkg "$scan")"
  [[ -n "$pkg" ]] || return 0

  pm="$(_node_json_field "$pkg" packageManager)"
  if [[ -n "$pm" && "$pm" != bun@* ]] && command -v corepack >/dev/null 2>&1; then
    if _proveo_bounded "${PROVEO_NODE_TIMEOUT:-180}" corepack prepare "$pm" --activate >/dev/null 2>&1 \
       && _proveo_bounded 30 corepack enable >/dev/null 2>&1; then
      echo "📦 package manager: ${pm} (corepack, from $(basename "$(dirname "$pkg")")/package.json)"
    else
      echo "⚠️  could not activate ${pm}; continuing with $(pnpm --version 2>/dev/null || echo 'the image default')"
    fi
  fi

  ensure_bun_pin "$pkg" "$scan"

  want_node="$(_node_json_field "$pkg" engines.node)"
  [[ -n "$want_node" ]] || want_node="$(_node_version_file "$scan")"
  [[ -n "$want_node" ]] || return 0
  have="$(node --version 2>/dev/null | tr -d 'v')"
  if _node_satisfies "$have" "$want_node"; then
    return 0
  fi
  echo "🟩 node ${have:-none} does not satisfy ${want_node}; provisioning via mise..."
  command -v mise >/dev/null 2>&1 || { echo "ℹ️  mise not on PATH — keeping node ${have}"; return 0; }
  _mise_install "node@${want_node%%.x*}" "$(_proveo_github_token)" >/dev/null 2>&1 \
    && _proveo_tool_path || echo "⚠️  could not provision node ${want_node}; keeping ${have}"
}

_bun_wanted() {
  local pkg="$1" scan="$2" pm want
  pm="$(_node_json_field "$pkg" packageManager)"
  case "$pm" in bun@?*) printf '%s' "${pm#bun@}"; return 0 ;; esac
  want="$(_node_json_field "$pkg" engines.bun)"
  [[ -n "$want" ]] && { printf '%s' "$want"; return 0; }
  [[ -f "$scan/.bun-version" ]] && head -n1 "$scan/.bun-version" | tr -d ' v\t'
  return 0
}

_bun_satisfies() {
  local have="$1" want="$2"
  [[ -n "$have" ]] || return 1
  case "$want" in
    [0-9]*.[0-9]*.[0-9]*) [[ "${want%%[^0-9.]*}" == "$want" ]] && { [[ "$have" == "$want" ]]; return $?; } ;;
  esac
  _node_satisfies "$have" "$want"
}

_bun_mise_spec() {
  local want="$1" v
  v="$(printf '%s' "$want" | sed -n 's/^[^0-9]*\([0-9][0-9.]*\).*/\1/p')"
  v="${v%.x}"; v="${v%.}"
  printf '%s' "${v:-latest}"
}

ensure_bun_pin() {
  local pkg="$1" scan="$2" want have
  want="$(_bun_wanted "$pkg" "$scan")"
  [[ -n "$want" ]] || return 0
  have="$(bun --version 2>/dev/null)"
  if _bun_satisfies "$have" "$want"; then
    return 0
  fi
  echo "🥟 bun ${have:-none} does not satisfy ${want}; provisioning via mise..."
  command -v mise >/dev/null 2>&1 || { echo "ℹ️  mise not on PATH — keeping bun ${have:-none}"; return 0; }
  if _mise_install "bun@$(_bun_mise_spec "$want")" "$(_proveo_github_token)" >/dev/null 2>&1; then
    _proveo_tool_path
    echo "📦 bun: $(bun --version 2>/dev/null || echo "$want") (mise, from $(basename "$(dirname "$pkg")")/package.json)"
  else
    echo "⚠️  could not provision bun ${want}; keeping ${have:-none}"
  fi
  return 0
}

_node_version_file() {
  local d="${1:-$PWD}" f
  for f in .nvmrc .node-version; do
    [[ -f "$d/$f" ]] && { head -n1 "$d/$f" | tr -d ' v\t'; return 0; }
  done
  return 0
}

_node_satisfies() {
  local have="$1" want="$2" have_major want_major
  [[ -n "$have" ]] || return 1
  have_major="${have%%.*}"
  want_major="$(printf '%s' "$want" | sed -n 's/^[^0-9]*\([0-9][0-9]*\).*/\1/p')"
  [[ -n "$want_major" ]] || return 0
  [[ "$have_major" == "$want_major" ]]
}

# SPEC: _spec/packages/lib/house-rules.puml
proveo_apply_ui_defaults() {
  local target="${1:-}" home themes src
  case "$(printf '%s' "${PROVEO_UI_DEFAULTS:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  [[ "$target" == claudecode ]] || return 0
  home="$(_proveo_agent_home)"
  [[ -n "$home" ]] || return 0

  src=/opt/proveo/themes/proveo-sandbox.json
  if [[ -s "$src" ]]; then
    themes="$home/.claude/themes"
    mkdir -p "$themes" 2>/dev/null && cp -f "$src" "$themes/" 2>/dev/null \
      && echo "🎨 theme: proveo sandbox (cyan) — a sandboxed session is meant to look unlike a host one"
  fi

  command -v node >/dev/null 2>&1 || return 0
  PROVEO_AGENT_HOME="$home" node -e '
    const fs = require("fs");
    const dir = process.env.PROVEO_AGENT_HOME + "/.claude";
    const path = dir + "/settings.json";
    let j = {};
    try { j = JSON.parse(fs.readFileSync(path, "utf8")) || {}; } catch (e) {}
    if (j.theme === undefined) j.theme = "custom:proveo-sandbox";
    if (j.syntaxHighlightingDisabled === undefined) j.syntaxHighlightingDisabled = false;
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path, JSON.stringify(j, null, 2) + "\n");
  ' 2>/dev/null || true
}

# SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
proveo_install_claude_hooks() {
  local target="${1:-}" home hook
  case "$(printf '%s' "${PROVEO_CWD_GUARD:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  [[ "$target" == claudecode ]] || return 0
  home="$(_proveo_agent_home)"
  [[ -n "$home" ]] || return 0
  hook="${PROVEO_CWD_GUARD_HOOK:-/opt/claudecode/defaults/hooks/cwd-guard.sh}"
  [[ -s "$hook" ]] || return 0
  command -v node >/dev/null 2>&1 || return 0
  PROVEO_AGENT_HOME="$home" PROVEO_HOOK="$hook" node -e '
    const fs = require("fs");
    const dir = process.env.PROVEO_AGENT_HOME + "/.claude";
    const path = dir + "/settings.json";
    const cmd = "bash " + process.env.PROVEO_HOOK;
    let j = {};
    try { j = JSON.parse(fs.readFileSync(path, "utf8")) || {}; } catch (e) {}
    if (typeof j.hooks !== "object" || j.hooks === null) j.hooks = {};
    if (!Array.isArray(j.hooks.PreToolUse)) j.hooks.PreToolUse = [];
    const present = j.hooks.PreToolUse.some(g => g && Array.isArray(g.hooks)
      && g.hooks.some(h => h && h.command === cmd));
    if (!present) j.hooks.PreToolUse.push({ matcher: "Bash", hooks: [{ type: "command", command: cmd, timeout: 5 }] });
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path, JSON.stringify(j, null, 2) + "\n");
  ' 2>/dev/null && echo "🛡️  cwd guard: PreToolUse(Bash) hook names a vanished working directory instead of a silent exit 1"
  return 0
}

# SPEC: _spec/defs/claudecode/lsp-plugins-seed.puml
_claude_lsp_plugins() { echo "typescript-lsp pyright-lsp gopls-lsp rust-analyzer-lsp clangd-lsp jdtls-lsp lua-lsp"; }
_claude_lsp_plugin_binary() { case "$1" in
  typescript-lsp)    echo "typescript-language-server" ;;
  pyright-lsp)       echo "pyright-langserver" ;;
  gopls-lsp)         echo "gopls" ;;
  rust-analyzer-lsp) echo "rust-analyzer" ;;
  clangd-lsp)        echo "clangd" ;;
  jdtls-lsp)         echo "jdtls" ;;
  lua-lsp)           echo "lua-language-server" ;;
esac; }
_claude_lsp_plugin_lang() { case "$1" in
  typescript-lsp)    echo "typescript" ;;
  pyright-lsp)       echo "python" ;;
  gopls-lsp)         echo "go" ;;
  rust-analyzer-lsp) echo "rust" ;;
  clangd-lsp)        echo "cpp" ;;
  jdtls-lsp)         echo "java" ;;
  lua-lsp)           echo "lua" ;;
esac; }

proveo_enable_claude_lsp_plugins() {
  local target="${1:-}" home seed p bin candidates="" enabled="" langs=""
  export PROVEO_CLAUDE_LSP_OFFICIAL=""
  case "$(printf '%s' "${PROVEO_CLAUDE_LSP_PLUGINS:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  [[ "$target" == claudecode ]] || return 0
  seed="${CLAUDE_CODE_PLUGIN_SEED_DIR:-}"
  [[ -n "$seed" && -d "$seed/cache/claude-plugins-official" ]] || return 0
  home="$(_proveo_agent_home)"
  [[ -n "$home" ]] || return 0
  for p in $(_claude_lsp_plugins); do
    [[ -d "$seed/cache/claude-plugins-official/$p" ]] || continue
    bin="$(_claude_lsp_plugin_binary "$p")"
    command -v "$bin" >/dev/null 2>&1 || continue
    candidates="${candidates:+$candidates }$p"
  done
  [[ -n "$candidates" ]] || return 0
  command -v node >/dev/null 2>&1 || return 0
  enabled="$(PROVEO_AGENT_HOME="$home" PROVEO_SEED="$seed" PROVEO_PLUGINS="$candidates" node -e '
    const fs = require("fs"), path = require("path");
    const home = process.env.PROVEO_AGENT_HOME + "/.claude", seed = process.env.PROVEO_SEED;
    const MKT = "claude-plugins-official";
    const read = (p, fallback) => { try { return JSON.parse(fs.readFileSync(p, "utf8")) || fallback; } catch (e) { return fallback; } };
    const write = (p, j) => { fs.mkdirSync(path.dirname(p), { recursive: true }); fs.writeFileSync(p, JSON.stringify(j, null, 2) + "\n"); };
    // 1. the marketplace, at the seed clone, unless the operator has one already
    const known = read(home + "/plugins/known_marketplaces.json", {});
    const seedKnown = read(seed + "/known_marketplaces.json", {});
    if (!known[MKT]) {
      known[MKT] = seedKnown[MKT] || { source: { source: "github", repo: "anthropics/" + MKT } };
      known[MKT].installLocation = seed + "/marketplaces/" + MKT;
      write(home + "/plugins/known_marketplaces.json", known);
    }
    // 2. the install records, at the seed cache, unless already installed
    const inst = read(home + "/plugins/installed_plugins.json", { version: 2, plugins: {} });
    if (typeof inst.plugins !== "object" || inst.plugins === null) inst.plugins = {};
    const seedInst = read(seed + "/installed_plugins.json", { plugins: {} }).plugins || {};
    const now = new Date().toISOString();
    for (const p of process.env.PROVEO_PLUGINS.split(" ")) {
      const key = p + "@" + MKT;
      if (inst.plugins[key]) continue;
      const cache = seed + "/cache/" + MKT + "/" + p;
      let version = "", installPath = "";
      const fromSeed = (seedInst[key] || [])[0];
      if (fromSeed && fromSeed.installPath && fs.existsSync(fromSeed.installPath)) { version = fromSeed.version; installPath = fromSeed.installPath; }
      else { const vs = fs.existsSync(cache) ? fs.readdirSync(cache).filter(d => !d.startsWith(".")) : []; if (vs.length) { version = vs[0]; installPath = cache + "/" + vs[0]; } }
      if (!installPath) continue;
      inst.plugins[key] = [{ scope: "user", installPath, version, installedAt: now, lastUpdated: now }];
    }
    write(home + "/plugins/installed_plugins.json", inst);
    // 3. enablement, respecting an explicit false
    const settingsPath = home + "/settings.json";
    const j = read(settingsPath, {});
    if (typeof j.enabledPlugins !== "object" || j.enabledPlugins === null) j.enabledPlugins = {};
    const on = [];
    for (const p of process.env.PROVEO_PLUGINS.split(" ")) {
      const key = p + "@" + MKT;
      if (!inst.plugins[key]) continue;
      if (j.enabledPlugins[key] === undefined) j.enabledPlugins[key] = true;
      if (j.enabledPlugins[key] === true) on.push(p);
    }
    write(settingsPath, j);
    process.stdout.write(on.join(" "));
  ' 2>/dev/null)" || return 0
  [[ -n "$enabled" ]] || return 0
  for p in $enabled; do langs="${langs:+$langs }$(_claude_lsp_plugin_lang "$p")"; done
  export PROVEO_CLAUDE_LSP_OFFICIAL="$langs"
  echo "🧠 code intelligence plugins (seeded in the image, no install prompt): ${enabled}"
  return 0
}

# SPEC: _spec/defs/browser-layer.puml, _spec/defs/claudecode/chrome-bridge.puml
_browser_skill_dir() { case "$1" in
  claudecode) echo ".claude/skills" ;;
  cursor)     echo ".cursor/skills" ;;
  opencode)   echo ".config/opencode/skills" ;;
  cecli|*)    echo "" ;;
esac; }

readonly PROVEO_BROWSER_SKILL_SRC=/opt/proveo/skills/agent-browser/SKILL.md

proveo_seed_browser_skills() {
  local target="${1:-}" home dir dst ver
  case "$(printf '%s' "${PROVEO_BROWSER_SKILL:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  command -v agent-browser >/dev/null 2>&1 || return 0
  [[ -s "$PROVEO_BROWSER_SKILL_SRC" ]] || return 0
  dir="$(_browser_skill_dir "$target")"
  [[ -n "$dir" ]] || return 0
  home="$(_proveo_agent_home)"
  [[ -n "$home" ]] || return 0
  dst="$home/$dir/agent-browser"
  mkdir -p "$dst" 2>/dev/null || return 0
  if [[ "$(sha256sum < "$PROVEO_BROWSER_SKILL_SRC")" != "$(sha256sum < "$dst/SKILL.md" 2>/dev/null)" ]]; then
    cp -f "$PROVEO_BROWSER_SKILL_SRC" "$dst/SKILL.md" 2>/dev/null || return 0
  fi
  ver="$(agent-browser --version 2>/dev/null | awk '{print $NF}')"
  echo "🕸️  browser: agent-browser ${ver:-?} + Playwright Chromium (headless) — skill at ~/${dir}/agent-browser"
  return 0
}

# SPEC: _spec/defs/claudecode/chrome-bridge.puml
readonly PROVEO_CHROME_BRIDGE_JS=/opt/proveo/lib/chrome-bridge.js
PROVEO_CHROME_READY=""

# SPEC: _spec/defs/claudecode/chrome-bridge.puml
_proveo_chrome_scope_ok() {
  local scopes home
  if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
    scopes="${CLAUDE_CODE_OAUTH_SCOPES:-user:inference}"
    case " $scopes " in
      *" user:profile "*|*" user:office "*|*" user:ccr_inference "*) return 0 ;;
    esac
    return 1
  fi
  [[ -n "${CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR:-}" ]] && return 0
  home="$(_proveo_agent_home)"
  if [[ -n "$home" ]] && grep -q '"accessToken":"[^"]' "$home/.claude/.credentials.json" 2>/dev/null; then
    return 0
  fi
  [[ -n "${ANTHROPIC_API_KEY:-}" ]] && return 1
  return 0  # nothing we can classify: not ours to warn about
}

proveo_chrome_bridge() {
  local target="${1:-}" home log out sock i
  [[ "$target" == claudecode ]] || return 0
  [[ -n "${PROVEO_CHROME_BRIDGE:-}" ]] || return 0
  if [[ -z "${PROVEO_CHROME_BRIDGE_TOKEN:-}" ]]; then
    echo "⚠️  chrome: PROVEO_CHROME_BRIDGE is set without PROVEO_CHROME_BRIDGE_TOKEN — the relay will not run unauthenticated; Claude in Chrome stays off" >&2
    return 0
  fi
  command -v node >/dev/null 2>&1 || return 0
  if [[ "${PROVEO_CHROME_READY:-}" == 1 ]]; then
    return 0
  fi
  if [[ ! -s "$PROVEO_CHROME_BRIDGE_JS" ]]; then
    echo "⚠️  chrome: $PROVEO_CHROME_BRIDGE_JS is not in this image; Claude in Chrome stays off" >&2
    return 0
  fi
  log="${TMPDIR:-/tmp}/proveo-chrome-bridge.log"
  out="${TMPDIR:-/tmp}/proveo-chrome-bridge.sock-path"
  : > "$out"
  if command -v setsid >/dev/null 2>&1; then
    setsid node "$PROVEO_CHROME_BRIDGE_JS" > "$out" 2>> "$log" < /dev/null &
  else
    node "$PROVEO_CHROME_BRIDGE_JS" > "$out" 2>> "$log" < /dev/null &
  fi
  for i in 1 2 3 4 5 6 7 8 9 10; do
    sock="$(head -n1 "$out" 2>/dev/null)"
    [[ -n "$sock" && -S "$sock" ]] && break
    sleep 0.3
  done
  if [[ -z "$sock" || ! -S "$sock" ]]; then
    echo "⚠️  chrome: relay did not come up (see $log); Claude in Chrome stays off" >&2
    return 0
  fi
  home="$(_proveo_agent_home)"
  if [[ -n "$home" ]]; then
    PROVEO_AGENT_HOME="$home" node -e '
      const fs = require("fs"), path = process.env.PROVEO_AGENT_HOME + "/.claude.json";
      let j = {};
      try { j = JSON.parse(fs.readFileSync(path, "utf8")) || {}; } catch (e) {}
      if (j.hasCompletedClaudeInChromeOnboarding === undefined) j.hasCompletedClaudeInChromeOnboarding = true;
      fs.writeFileSync(path, JSON.stringify(j, null, 2) + "\n");
    ' 2>/dev/null || true
  fi
  # shellcheck disable=SC2034  # read by defs/claudecode/mcp/entrypoint.sh after this returns
  PROVEO_CHROME_READY=1
  echo "🧭 chrome: Claude in Chrome via the host browser — relay ${sock} → ${PROVEO_CHROME_BRIDGE} (the agent drives YOUR Chrome, with your logins)"
  if ! _proveo_chrome_scope_ok; then
    echo "⚠️  chrome: this session's OAuth scopes name none of user:profile / user:office / user:ccr_inference, so Claude Code turns Chrome integration off; set CLAUDE_CODE_OAUTH_SCOPES to the scopes the token was issued with, or sign in with /login" >&2
  fi
  return 0
}

_lsp_ext_lang() { case "$1" in
  .ts|.tsx|.js|.jsx|.mts|.cts|.mjs|.cjs|.vue|.svelte) REPLY=typescript ;;
  .py|.pyi) REPLY=python ;;
  .go) REPLY=go ;; .rs) REPLY=rust ;;
  .sh|.bash|.zsh|.ksh) REPLY=bash ;;
  .json|.jsonc) REPLY=json ;;
  .yml|.yaml) REPLY=yaml ;;
  .html|.htm) REPLY=html ;;
  .css|.scss|.sass|.less) REPLY=css ;;
  .md|.mdx) REPLY=markdown ;;
  .toml) REPLY=toml ;;
  .tf|.tfvars) REPLY=terraform ;;
  .lua) REPLY=lua ;; .java) REPLY=java ;;
  .c|.h|.cc|.cpp|.cxx|.hpp|.hh) REPLY=cpp ;;
  .rb) REPLY=ruby ;; .kt|.kts) REPLY=kotlin ;; .nix) REPLY=nix ;; .zig) REPLY=zig ;;
  .puml|.plantuml) REPLY=plantuml ;;
  .mmd|.mermaid) REPLY=mermaid ;;
  *) REPLY="" ;;
esac; }
_lsp_marker_lang() { case "$1" in
  package.json|tsconfig.json|jsconfig.json) REPLY=typescript ;;
  pyproject.toml|requirements.txt|setup.py|Pipfile) REPLY=python ;;
  go.mod) REPLY=go ;; Cargo.toml) REPLY=rust ;;
  Dockerfile|Containerfile|docker-compose.yml|docker-compose.yaml) REPLY=docker ;;
  Gemfile) REPLY=ruby ;;
  .terraform.lock.hcl|Terraform.lock.hcl) REPLY=terraform ;;
  *) REPLY="" ;;
esac; }
_lsp_server() { case "$1" in
  typescript) echo "typescript-language-server --stdio" ;;
  python) echo "pyright-langserver --stdio" ;;
  bash) echo "bash-language-server start" ;;
  docker) echo "docker-langserver --stdio" ;;
  yaml) echo "yaml-language-server --stdio" ;;
  json) echo "vscode-json-language-server --stdio" ;;
  html) echo "vscode-html-language-server --stdio" ;;
  css) echo "vscode-css-language-server --stdio" ;;
  markdown) echo "marksman server" ;;
  toml) echo "taplo lsp stdio" ;;
  terraform) echo "terraform-ls serve" ;;
  lua) echo "lua-language-server" ;;
  go) echo "gopls" ;;
  rust) echo "rust-analyzer" ;;
  java) echo "jdtls" ;;
  cpp) echo "clangd" ;;
  ruby) echo "ruby-lsp" ;;
  kotlin) echo "kotlin-language-server" ;;
  nix) echo "nil" ;;
  zig) echo "zls" ;;
  plantuml) echo "plantuml-lsp" ;;
esac; }

_lsp_mise_spec() { case "$1" in
  rust)      echo "rust-analyzer" ;;
  markdown)  echo "marksman" ;;
  toml)      echo "taplo" ;;
  terraform) echo "terraform-ls" ;;
  lua)       echo "lua-language-server" ;;
  zig)       echo "zls" ;;
  cpp)       echo "ubi:clangd/clangd" ;;
  kotlin)    echo "ubi:fwcd/kotlin-language-server[exe=kotlin-language-server,extract_all=true,bin_path=bin]" ;;
esac; }

_lsp_precondition() {
  case "$1" in
    cpp)
      case "$(uname -m)" in
        x86_64|amd64) ;;
        *) echo "clangd publishes x86_64 Linux builds only — none for $(uname -m)"; return 1 ;;
      esac ;;
  esac
  return 0
}

_java_major() {
  command -v java >/dev/null 2>&1 || { echo 0; return 0; }
  local v
  v="$(java -version 2>&1 | sed -n 's/.*version "\([0-9][0-9]*\).*/\1/p' | head -n1)"
  echo "${v:-0}"
}

_lsp_custom_install() { case "$1" in
  java) _install_jdtls "${2:-}" ;;
  *)    return 1 ;;
esac; }
_lsp_has_custom_install() { case "$1" in
  java) return 0 ;;
  *)    return 1 ;;
esac; }

_install_jdtls() {
  local gh_token="${1:-}" t home tarball rc
  t="$(_proveo_tool_home)"
  home="$t/.local/share/proveo/jdtls"
  local url="${PROVEO_JDTLS_URL:-https://download.eclipse.org/jdtls/snapshots/jdt-language-server-latest.tar.gz}"

  if [ "$(_java_major)" -lt 21 ]; then
    echo "   provisioning a Java 21 runtime (jdtls needs it; the floor's JRE is $(_java_major))..."
    _mise_install "java@21" "$gh_token" >/dev/null 2>&1 \
      || { echo "   could not provision Java 21"; return 1; }
    _proveo_tool_path
  fi

  tarball="$(mktemp)" || return 1
  _proveo_bounded "${PROVEO_LSP_INSTALL_TIMEOUT:-180}" \
    curl -fsSL --connect-timeout 5 "$url" -o "$tarball"
  rc=$?
  if [ $rc -ne 0 ]; then rm -f "$tarball"; return 1; fi
  mkdir -p "$home"
  tar -xzf "$tarball" -C "$home"
  rc=$?
  rm -f "$tarball"
  [ $rc -eq 0 ] || return 1

  mkdir -p "$t/.local/bin"
  cat > "$t/.local/bin/jdtls" <<PROVEO_JDTLS
#!/bin/sh
J="${home}"
case "\$(uname -m)" in
  aarch64|arm64) CFG="\$J/config_linux_arm" ;;
  *)             CFG="\$J/config_linux" ;;
esac
L=\$(ls "\$J"/plugins/org.eclipse.equinox.launcher_*.jar 2>/dev/null | head -n1)
[ -n "\$L" ] || { echo "jdtls: equinox launcher not found under \$J" >&2; exit 1; }
exec java \\
  -Declipse.application=org.eclipse.jdt.ls.core.id1 \\
  -Dosgi.bundles.defaultStartLevel=4 \\
  -Declipse.product=org.eclipse.jdt.ls.core.product \\
  -Dosgi.sharedConfiguration.area="\$CFG" \\
  -Dosgi.sharedConfiguration.area.readOnly=true \\
  -Dosgi.configuration.cascaded=true \\
  --add-modules=ALL-SYSTEM \\
  --add-opens java.base/java.util=ALL-UNNAMED \\
  --add-opens java.base/java.lang=ALL-UNNAMED \\
  -jar "\$L" \\
  -data "\${JDTLS_WORKSPACE:-\$HOME/.cache/jdtls-workspace}" "\$@"
PROVEO_JDTLS
  chmod +x "$t/.local/bin/jdtls"
}

_proveo_walk() {
  local root="$1"; shift
  find "$root" \
    \( -name .git -o -name node_modules -o -name vendor -o -name target \
       -o -name dist -o -name build -o -name .next -o -name .nx \
       -o -name .turbo -o -name .cache -o -name .gradle -o -name .tox \
       -o -name .venv -o -name venv -o -name __pycache__ \
       -o -name .mypy_cache -o -name .pytest_cache -o -name .ruff_cache \
       -o -name .terraform -o -name Pods \
       -o -name .pnpm-store -o -name .npm -o -name .yarn \) -prune \
    -o "$@" 2>/dev/null
}

_lsp_walk() {
  local scan_root="$1" f base ext lang marker ftype mext ml
  while IFS= read -r -d '' f; do
    base="${f##*/}"
    lang=""; ftype=""
    _lsp_marker_lang "$base"; marker="$REPLY"
    if [[ -n "$marker" ]]; then
      lang="$marker"
      [[ "$lang" == docker ]] && ftype="$base"
    fi
    if [[ -z "$lang" ]]; then
      if [[ "$base" == *.* ]]; then ext=".${base##*.}"; else ext=""; fi
      if [[ -n "$ext" ]]; then
        _lsp_ext_lang "$ext"; lang="$REPLY"
        [[ -n "$lang" ]] && ftype="$ext"
      fi
    fi
    if [[ -z "$lang" && ( "$base" == *Dockerfile* || "$base" == *Containerfile* ) ]]; then
      lang=docker; ftype="$base"
    fi
    [[ -n "$lang" ]] || continue
    printf '%s\t%s\n' "$lang" "$ftype"
    if [[ -n "$marker" && "$base" == *.* ]]; then
      mext=".${base##*.}"
      _lsp_ext_lang "$mext"; ml="$REPLY"
      [[ -n "$ml" ]] && printf '%s\t%s\n' "$ml" "$mext"
    fi
  done < <(_proveo_walk "$scan_root" -type f -print0)
}

# SPEC: _spec/packages/lib/language-server-provisioning.puml
ensure_language_servers() {
  _proveo_tool_path
  _proveo_auto_install_enabled || return 0

  local scan_root="${1:-$(pwd)}" mode min_files installed="" lang cnt server cmd spec reason out rc gh_token
  mode="$(printf '%s' "${PROVEO_LSP_INSTALL:-auto}" | tr '[:upper:]' '[:lower:]')"
  case "$mode" in off|false|0|no|disable|disabled) return 0 ;; esac
  min_files="${PROVEO_LSP_MIN_FILES:-1}"

  if ! command -v mise >/dev/null 2>&1; then
    echo "ℹ️  mise not on PATH — skipping language-server provisioning"
    return 0
  fi

  gh_token="$(_proveo_github_token)"

  _proveo_lock_installs || return 0

  while IFS=$'\t' read -r lang cnt; do
    [[ -n "$lang" ]] || continue
    [[ "$cnt" -ge "$min_files" ]] || continue
    if [[ "$mode" != auto ]]; then
      case ",${mode}," in *",${lang},"*) ;; *) continue ;; esac
    fi
    server="$(_lsp_server "$lang")"
    [[ -n "$server" ]] || continue          # server-less language (mermaid)
    cmd="${server%% *}"
    command -v "$cmd" >/dev/null 2>&1 && continue   # image layer, or an earlier run

    spec="$(_lsp_mise_spec "$lang")"
    if [[ -z "$spec" ]] && ! _lsp_has_custom_install "$lang"; then
      continue                              # no recipe — see _lsp_mise_spec
    fi

    if ! reason="$(_lsp_precondition "$lang")"; then
      echo "⏭️  Skipping ${cmd} for ${lang}: ${reason}"
      continue
    fi

    echo "📦 Detected ${lang} (${cnt} files). Installing ${cmd}..."
    if [[ -n "$spec" ]]; then
      out="$(_mise_install "$spec" "$gh_token")"
      rc=$?
    else
      out="$(_lsp_custom_install "$lang" "$gh_token" 2>&1)"
      rc=$?
      [[ -n "$out" ]] && printf '%s\n' "$out"
    fi

    if [[ $rc -eq 0 ]]; then
      installed="${installed} ${cmd}"
    else
      case "$out" in
        *403*|*"rate limit"*|*Forbidden*)
          if [[ -n "$gh_token" ]]; then
            echo "⚠️  GitHub API refused the request for ${cmd} despite an authenticated token."
            echo "    Check the token's scopes; code intelligence for ${lang} stays off."
          else
            echo "⚠️  GitHub API rate limit while installing ${cmd}, and no token was found."
            echo "    Run 'gh auth login' on the host (proveo mounts ~/.config/gh into the"
            echo "    container) or set GITHUB_TOKEN; code intelligence for ${lang} stays off."
          fi ;;
        *)
          echo "⚠️  Failed to install ${cmd}; code intelligence for ${lang} stays off" ;;
      esac
    fi
  done < <(_lsp_walk "$scan_root" | cut -f1 | sort | uniq -c \
             | awk '{ printf "%s\t%s\n", $2, $1 }')

  _proveo_unlock_installs
  [[ -n "$installed" ]] && echo "🧠 Provisioned language servers:${installed}"
  return 0
}

detect_workspace_lsps() {
  local scan_root="${1:-$(pwd)}"
  local tab; tab="$(printf '\t')"
  _lsp_walk "$scan_root" | awk -F'\t' '
    BEGIN {
      n = split("typescript python java cpp go rust kotlin ruby bash json yaml docker html css markdown toml terraform lua nix zig plantuml mermaid", P, " ")
      for (i = 1; i <= n; i++) pop[P[i]] = i - 1
    }
    {
      total[$1]++
      if ($2 != "" && !((k = $1 SUBSEP $2) in seen)) { seen[k] = 1; e[$1] = (e[$1] == "" ? $2 : e[$1] "," $2) }
    }
    END { for (l in total) printf "%d\t%d\t%s\t%s\n", total[l], (l in pop ? pop[l] : 999), l, e[l] }
  ' | sort -t"$tab" -k1,1nr -k2,2n -k3,3 | while IFS="$tab" read -r cnt _pop lang extcsv; do
    local server cmd
    server="$(_lsp_server "$lang")"
    [[ -n "$server" ]] || continue
    cmd="${server%% *}"
    command -v "$cmd" >/dev/null 2>&1 || continue
    extcsv="$(printf '%s' "$extcsv" | tr ',' '\n' | sort -u | paste -sd, -)"
    printf '%s|%s|%s|%s\n' "$lang" "$cnt" "${server// /|}" "$extcsv"
  done
}

configure_claude_lsp() {
  command -v jq >/dev/null 2>&1 || return 0
  local scan="${1:-$(pwd)}" lsp_json plugdir
  plugdir="$(_proveo_agent_home)/.claude/skills/proveo-lsp"
  lsp_json="$(detect_workspace_lsps "$scan" | jq -R -s '
    split("\n") | map(select(length > 0) | split("|")) | map(. as $f | {
      key: $f[0],
      value: {
        command: $f[2],
        args: $f[3:-1],
        extensionToLanguage: (($f[-1] | split(",") | map(select(length > 0)))
          | map({key: ., value: $f[0]}) | from_entries)
      }
    }) | from_entries')"
  [ -z "$lsp_json" ] && lsp_json="{}"
  if [[ -n "${PROVEO_CLAUDE_LSP_OFFICIAL:-}" ]]; then
    lsp_json="$(printf '%s' "$lsp_json" | jq --arg drop "$PROVEO_CLAUDE_LSP_OFFICIAL" \
      'with_entries(select(.key as $k | ($drop | split(" ") | index($k)) == null))')"
  fi
  [ "$lsp_json" = "{}" ] && return 0

  mkdir -p "$plugdir/.claude-plugin"
  printf '{"name":"proveo-lsp","description":"Workspace language servers (auto-detected)","version":"1.0.0"}\n' \
    > "$plugdir/.claude-plugin/plugin.json"
  printf '%s\n' "$lsp_json" > "$plugdir/.lsp.json"
  echo "🧠 LSP code intelligence (Claude Code plugin): $(printf '%s' "$lsp_json" | jq -r 'keys_unsorted | join(" ")')"
}

configure_opencode_lsp() {
  command -v jq >/dev/null 2>&1 || return 0
  local scan="${1:-$(pwd)}" config_file matched_json existing='{}' tmp
  config_file="$(_proveo_agent_home)/.config/opencode/opencode.json"
  matched_json="$(detect_workspace_lsps "$scan" | jq -R -s '
    split("\n") | map(select(length > 0) | split("|")) | map({
      key: .[0],
      value: { command: .[2:-1],
               extensions: (if (.[-1] | length) > 0 then (.[-1] | split(",")) else [] end) }
    }) | from_entries
  ')"
  [[ -n "$matched_json" ]] || matched_json="{}"

  echo "── Workspace LSP Match ──────────────────────────────"
  if [[ "$matched_json" == "{}" ]]; then
    echo "🔎 No installed LSP matched files under $scan"
    echo "─────────────────────────────────────────────────────"
    return 0
  fi

  mkdir -p "$(dirname "$config_file")"
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

# SPEC: _spec/_plans/config-seeding-and-persistence.puml
configure_opencode_formatter() {
  command -v jq >/dev/null 2>&1 || return 0
  case "$(printf '%s' "${PROVEO_OPENCODE_FORMATTER:-auto}" | tr '[:upper:]' '[:lower:]')" in
  off | false | 0 | no | disable | disabled) return 0 ;;
  esac
  local config_file existing='{}' tmp
  config_file="$(_proveo_agent_home)/.config/opencode/opencode.json"
  mkdir -p "$(dirname "$config_file")" 2>/dev/null || return 0
  [[ -f "$config_file" ]] && jq -e . "$config_file" >/dev/null 2>&1 && existing="$(cat "$config_file")"
  if printf '%s' "$existing" | jq -e 'has("formatter")' >/dev/null 2>&1; then
    return 0
  fi
  tmp="$(mktemp)"
  if printf '%s' "$existing" | jq '.formatter = true' >"$tmp" 2>/dev/null; then
    mv "$tmp" "$config_file"
    echo "🧹 formatters enabled (opencode's built-ins, each still gated on its own availability): $config_file"
  else
    rm -f "$tmp"
    echo "⚠️  Could not enable formatters in $config_file (jq failed)" >&2
  fi
}

configure_cursor_lsp() {
  command -v jq >/dev/null 2>&1 || return 0
  command -v mcp-language-server >/dev/null 2>&1 || return 0
  local scan="${1:-$(pwd)}" cursor_home mcp_file tmp base entries
  cursor_home="${CURSOR_CONFIG_DIR:-$(_proveo_agent_home)/.cursor}"
  mcp_file="$cursor_home/mcp.json"
  entries="$(detect_workspace_lsps "$scan" | jq -R -s --arg ws "$scan" '
    split("\n") | map(select(length > 0) | split("|")) | map({
      key: .[0],
      value: {
        command: "mcp-language-server",
        args: (["--workspace", $ws, "--lsp", .[2]]
               + (.[3:-1] | if length > 0 then ["--"] + . else [] end))
      }
    }) | from_entries')"
  [[ -z "$entries" || "$entries" == "{}" ]] && return 0

  mkdir -p "$cursor_home"
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

# SPEC: _spec/_plans/config-seeding-and-persistence.puml,
_proveo_config_classes() { echo "plugin lsp formatter mcp"; }

_proveo_class_wire() {
  case "$1:$2" in
  lsp:claudecode) echo "configure_claude_lsp" ;;
  lsp:opencode) echo "configure_opencode_lsp" ;;
  lsp:cursor) echo "configure_cursor_lsp" ;;
  formatter:opencode) echo "configure_opencode_formatter" ;;
  mcp:cecli) echo "configure_cecli_mcp" ;;
  plugin:claudecode) echo "configure_claude_plugins" ;;
  esac
}

_proveo_class_probe() {
  case "$1" in
  mcp) echo "_proveo_mcp_probe" ;;
  plugin) echo "_proveo_plugin_probe" ;;
  esac
}

_proveo_mcp_probe() {
  local secs="${PROVEO_MCP_PROBE_TIMEOUT:-10}" out
  command -v "$1" >/dev/null 2>&1 || return 1
  out="$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"proveo","version":"1"}}}' \
    | _proveo_bounded "$secs" "$@" 2>/dev/null | head -c 65536)" || true
  case "$out" in
  *'"result"'*'"protocolVersion"'* | *'"result"'*'"serverInfo"'*) return 0 ;;
  esac
  return 1
}

_proveo_plugin_probe() { command -v "$1" >/dev/null 2>&1; }

configure_claude_plugins() { proveo_enable_claude_lsp_plugins "${2:-}"; }

# SPEC: _spec/defs/cecli/cecli-paradigm.puml
configure_cecli_mcp() {
  local scan="${1:-$(pwd)}" conf probe
  case "$(printf '%s' "${PROVEO_CECLI_MCP:-auto}" | tr '[:upper:]' '[:lower:]')" in
  off | false | 0 | no | disable | disabled) return 0 ;;
  esac
  command -v serena >/dev/null 2>&1 || return 0

  conf="$(_proveo_agent_home)/.cecli.conf.yml"
  if [[ -f "$conf" ]] && grep -qE '^[[:space:]]*mcp-servers:' "$conf"; then
    echo "🧠 MCP: mcp-servers already declared in $conf; leaving it alone"
    return 0
  fi

  probe="$(_proveo_class_probe mcp)"
  if ! "$probe" serena start-mcp-server --context ide-assistant --project "$scan"; then
    echo "⚠️  MCP: serena did not answer an initialize handshake within ${PROVEO_MCP_PROBE_TIMEOUT:-10}s;" >&2
    echo "    code intelligence for cecli stays off rather than declaring a dead server." >&2
    return 0
  fi

  cat >>"$conf" <<YAML
mcp-servers:
  mcpServers:
    serena:
      command: serena
      args: [start-mcp-server, --context, ide-assistant, --project, $scan]
YAML
  echo "🧠 MCP code intelligence via Serena (initialize handshake answered): $conf"
}

proveo_wire_config() {
  local target="${1:-}" scan class fn
  [[ -n "$target" ]] || return 0
  scan="$(_proveo_scan_root "${2:-}")"
  for class in $(_proveo_config_classes); do
    fn="$(_proveo_class_wire "$class" "$target")"
    [[ -n "$fn" ]] || continue
    command -v "$fn" >/dev/null 2>&1 || {
      echo "⚠️  no $class wiring function $fn in this image; skipping" >&2
      continue
    }
    "$fn" "$scan" "$target" || true
  done
}

agent_evidence_verbose() {
 [[ "${PROVEO_AGENT_EVIDENCE:-verbose}" != "default" ]]
}

report_agent_evidence() {
 if (( $# > 0 )); then
 echo "🔎 agent evidence: verbose — $*"
 else
 echo "🔎 agent evidence: default (harness defaults, no extra narration)"
 fi
}

render_subagents() {
 local harness="$1" dst="$2" reseed="${3:-0}"
 local src="${PROVEO_SUBAGENTS_DIR:-/opt/proveo/subagents}"
 local fmdir="$src/_frontmatter/$harness" varfile="$src/_vars/$harness.env"

 [[ -d "$fmdir" ]] || return 0
 mkdir -p "$dst" 2>/dev/null || { echo "⚠️  Could not seed subagents into $dst; continuing" >&2; return 0; }

 local keys=() vals=() k v
 if [[ -f "$varfile" ]]; then
  while IFS='=' read -r k v; do
   [[ -n "$k" && "$k" != \#* ]] || continue
   keys+=("$k"); vals+=("$v")
  done < "$varfile"
 fi

 local seeded=() yaml name body line out i
 for yaml in "$fmdir"/*.yaml; do
  [[ -e "$yaml" ]] || continue
  name="$(basename "$yaml" .yaml)"
  body="$src/$name.md"
  [[ -f "$body" ]] || { echo "⚠️  subagent body missing for $name; skipping" >&2; continue; }
  [[ "$reseed" == "1" || ! -f "$dst/$name.md" ]] || continue

  out="---"$'\n'"# composed at runtime from $src/$name.md"$'\n'
  out+="$(cat "$yaml")"$'\n'"---"$'\n'$'\n'
  while IFS= read -r line || [[ -n "$line" ]]; do
   for i in "${!keys[@]}"; do
    line="${line//\{\{${keys[$i]}\}\}/${vals[$i]}}"
   done
   out+="$line"$'\n'
  done < "$body"

  if printf '%s' "$out" > "$dst/$name.md" 2>/dev/null; then
   seeded+=("$name.md")
  fi
 done

 if (( ${#seeded[@]} > 0 )); then
  echo "🌱 Composed $harness subagents into $dst: ${seeded[*]}"
 fi
}

_proveo_agent_home() { printf '%s' "${PROVEO_HOME:-${HOME:-}}"; }

_proveo_scan_root() { printf '%s' "${1:-${PROVEO_WORKDIR:-$PWD}}"; }

_proveo_volume_mounts() {
 local home; home="$(_proveo_agent_home)"
 [[ -n "$home" && -r /proc/mounts ]] || return 0
 awk -v h="$home/" 'index($2, h) == 1 && index($1, "/dev/") == 1 { print $2 }' /proc/mounts
}

_proveo_volume_state_dirs() {
 local d
 while read -r d; do
  [[ -n "$d" ]] || continue
  case "${d##*/}" in
  statsig | shell-snapshots) continue ;;
  esac
  printf '%s\n' "$d"
 done < <(_proveo_volume_mounts)
}

# SPEC: _spec/_plans/config-seeding-and-persistence.puml
_proveo_config_entries() {
 [[ -n "${PROVEO_CONFIG_DIRS:-}" ]] || return 0
 printf '%s\n' "$PROVEO_CONFIG_DIRS" | tr ';' '\n'
}

_proveo_config_file_names() {
 [[ -n "${PROVEO_CONFIG_FILES:-}" ]] || return 0
 printf '%s\n' "$PROVEO_CONFIG_FILES" | tr ';' '\n'
}

_proveo_config_skips() {
 local arel="$1" deny="$2" home vol d
 home="$(_proveo_agent_home)"
 while read -r vol; do
  [[ -n "$vol" ]] || continue
  case "$vol" in
  "$home/$arel/"*) printf '%s\n' "${vol#"$home/$arel/"}" ;;
  esac
 done < <(_proveo_volume_mounts)
 local oldifs="$IFS"
 IFS=','
 for d in $deny; do
  d="$(printf '%s' "$d" | tr -d '[:space:]')"
  [[ -n "$d" ]] && printf '%s\n' "$d"
 done
 IFS="$oldifs"
}

_proveo_config_tree() {
 local src="$1" dst="$2" arel="$3" deny="$4" rel tmp failed=0 sub
 [[ -d "$src" ]] || return 0
 mkdir -p "$dst" 2>/dev/null || return 1

 local -a prune=()
 while read -r sub; do
  [[ -n "$sub" ]] || continue
  prune+=(-path "./$sub" -prune -o)
 done < <(_proveo_config_skips "$arel" "$deny")

 while IFS= read -r rel; do
  rel="${rel#./}"
  [[ -n "$rel" && "$rel" != "." ]] || continue
  if [[ -d "$src/$rel" ]]; then
   mkdir -p "$dst/$rel" 2>/dev/null || failed=1
   continue
  fi
  if [[ ! -e "$dst/$rel" ]]; then
   mkdir -p "$(dirname "$dst/$rel")" 2>/dev/null
   cp -a "$src/$rel" "$dst/$rel" 2>/dev/null || failed=1
   continue
  fi
  [[ "$src/$rel" -nt "$dst/$rel" ]] || continue
  tmp="$dst/$rel.proveo-sync.$$"
  if cp -a "$src/$rel" "$tmp" 2>/dev/null && mv -f "$tmp" "$dst/$rel" 2>/dev/null; then
   continue
  fi
  rm -f "$tmp" 2>/dev/null
  failed=1
 done < <(cd "$src" 2>/dev/null && find . ${prune[@]+"${prune[@]}"} \
  \( -type f -o -type d \) -print 2>/dev/null)
 return "$failed"
}

_proveo_config_file() {
 local src="$1" dst="$2" tmp
 [[ -f "$src" ]] || return 0
 [[ ! -e "$dst" || "$src" -nt "$dst" ]] || return 0
 mkdir -p "$(dirname "$dst")" 2>/dev/null || return 1
 tmp="$dst.proveo-sync.$$"
 if cp -a "$src" "$tmp" 2>/dev/null && mv -f "$tmp" "$dst" 2>/dev/null; then
  return 0
 fi
 rm -f "$tmp" 2>/dev/null
 return 1
}

proveo_sync_config() {
 local mode="${1:-}" host="${PROVEO_STATE_HOME:-}" home entry hrel arel deny src dst lock rc=0
 case "$mode" in
 restore | save) ;;
 *) return 2 ;;
 esac
 case "$(printf '%s' "${PROVEO_CONFIG_SYNC:-on}" | tr '[:upper:]' '[:lower:]')" in
 off | false | 0 | no | disable | disabled) return 0 ;;
 esac
 [[ -n "$host" && -d "$host" ]] || return 0
 home="$(_proveo_agent_home)"
 [[ -n "$home" ]] || return 0

 lock="$home/.proveo-config.lock"
 if ! _proveo_sync_lock "$lock"; then
  printf 'proveo: config %s skipped — another sync still holds the lock\n' "$mode" >&2
  return 1
 fi

 while IFS= read -r entry; do
  [[ -n "$entry" ]] || continue
  hrel="${entry%%|*}"
  arel="${entry#*|}"
  deny="${arel#*|}"
  arel="${arel%%|*}"
  [[ -n "$hrel" && -n "$arel" ]] || continue
  case "$mode" in
  restore) src="$host/$hrel" dst="$home/$arel" ;;
  save) src="$home/$arel" dst="$host/$hrel" ;;
  esac
  _proveo_config_tree "$src" "$dst" "$arel" "$deny" || rc=1
 done < <(_proveo_config_entries)

 while IFS= read -r entry; do
  [[ -n "$entry" ]] || continue
  case "$mode" in
  restore) src="$host/$entry" dst="$home/$entry" ;;
  save) src="$home/$entry" dst="$host/$entry" ;;
  esac
  _proveo_config_file "$src" "$dst" || rc=1
 done < <(_proveo_config_file_names)

 rm -rf "$lock" 2>/dev/null
 ((rc == 0)) || printf 'proveo: config %s completed with copy errors\n' "$mode" >&2
 return "$rc"
}

_proveo_sync_lock() {
 local dir="$1" waited=0 pid
 while ! mkdir "$dir" 2>/dev/null; do
  pid="$(cat "$dir/pid" 2>/dev/null)"
  if [[ -n "$pid" ]] && ! kill -0 "$pid" 2>/dev/null; then
   rm -rf "$dir" 2>/dev/null
   continue
  fi
  waited=$((waited + 1))
  ((waited > 600)) && return 1 # 60s, an order of magnitude over any measured sync
  sleep 0.1
 done
 printf '%s\n' "$$" >"$dir/pid" 2>/dev/null
 return 0
}

_proveo_changed_files() {
 local src="$1" dst="$2" cutoff
 cutoff=$(($(date +%s) - 5))
 {
  (cd "$src" 2>/dev/null && find . -type f -printf 'S %s %T@ %P\n' 2>/dev/null)
  (cd "$dst" 2>/dev/null && find . -type f -printf 'D %s %T@ %P\n' 2>/dev/null)
 } | awk -v cutoff="$cutoff" '{
    key = $4; for (i = 5; i <= NF; i++) key = key " " $i
    stamp = $2 " " $3
    if ($1 == "S") { s[key] = stamp; at[key] = $3 + 0 } else d[key] = stamp
  }
  END {
    for (k in s)
      if ((k in d) && (s[k] != d[k] || at[k] >= cutoff)) print k
  }'
}

_proveo_sync_tree() {
 local src="$1" dst="$2" rel tmp failed=0
 [[ -d "$src" ]] || return 0
 mkdir -p "$dst" 2>/dev/null || return 1
 cp -an "$src/." "$dst/" 2>/dev/null || failed=1
 while IFS= read -r rel; do
  [[ -n "$rel" ]] || continue
  tmp="$dst/$rel.proveo-sync.$$"
  if cp -a "$src/$rel" "$tmp" 2>/dev/null && mv -f "$tmp" "$dst/$rel" 2>/dev/null; then
   continue
  fi
  rm -f "$tmp" 2>/dev/null
  failed=1
 done < <(_proveo_changed_files "$src" "$dst")
 return "$failed"
}

proveo_sync_state() {
 local mode="${1:-}" host="${PROVEO_STATE_HOME:-}" home dir rel src dst lock rc=0
 [[ -n "$host" && -d "$host" ]] || return 0
 case "$mode" in
 restore | save) ;;
 *) return 2 ;;
 esac
 home="$(_proveo_agent_home)"
 [[ -n "$home" ]] || return 0
 lock="$home/.proveo-sync.lock"
 if ! _proveo_sync_lock "$lock"; then
  printf 'proveo: state %s skipped — another sync still holds the lock\n' "$mode" >&2
  return 1
 fi
 while read -r dir; do
  [[ -n "$dir" ]] || continue
  rel="${dir#"$home"/}"
  case "$mode" in
  restore) src="$host/$rel" dst="$dir" ;;
  save) src="$dir" dst="$host/$rel" ;;
  esac
  [[ -d "$src" ]] || continue
  [[ -n "$(ls -A "$src" 2>/dev/null)" ]] || continue
  _proveo_sync_tree "$src" "$dst" || rc=1
 done < <(_proveo_volume_state_dirs)
 rm -rf "$lock" 2>/dev/null
 ((rc == 0)) || printf 'proveo: state %s completed with copy errors\n' "$mode" >&2
 return "$rc"
}

proveo_provision_toolchain() {
 local scan; scan="$(_proveo_scan_root "${1:-}")"
 [[ -d "$scan" ]] || return 0
 local prev="$PWD"
 cd "$scan" 2>/dev/null && ensure_project_tools
 cd "$prev" 2>/dev/null || true
 ensure_node_toolchain "$scan"
 ensure_python_env "$scan"
 ensure_dependency_trees "$scan"
 ensure_language_servers "$scan"
 _proveo_persist_tool_env
}

readonly PROVEO_RC_START="# >>> proveo toolchain (generated — edits are overwritten) >>>"
readonly PROVEO_RC_END="# <<< proveo toolchain <<<"

_proveo_write_block() {
  local path="$1" start="$2" end="$3" tmp
  mkdir -p "$(dirname "$path")" 2>/dev/null || return 0
  tmp="$(mktemp)" || return 0
  if [[ -f "$path" ]]; then
    awk -v s="$start" -v e="$end" '
      $0 == s { skip = 1 } !skip { print } $0 == e { skip = 0 }' "$path" > "$tmp" 2>/dev/null || cp "$path" "$tmp"
  fi
  { printf '%s\n' "$start"; cat; printf '%s\n' "$end"; } >> "$tmp"
  mv "$tmp" "$path" 2>/dev/null || rm -f "$tmp"
}

_proveo_persist_tool_env() {
 local home tool; home="$(_proveo_agent_home)"; tool="$(_proveo_tool_home)"
 [[ -n "$home" ]] || return 0
 {
   printf 'export PATH="%s/.local/bin:%s/.local/share/mise/shims:$PATH"\n' "$tool" "$tool"
   printf 'export MISE_DATA_DIR="%s/.local/share/mise"\n' "$tool"
   printf 'export MISE_CONFIG_DIR="%s/.config/mise"\n' "$tool"
   [[ -n "${GOPATH:-}" ]] && printf 'export GOPATH="%s"\nexport PATH="%s/bin:$PATH"\n' "$GOPATH" "$GOPATH"
   if [[ -n "${VIRTUAL_ENV:-}" && -x "${VIRTUAL_ENV}/bin/python" ]]; then
     printf 'export VIRTUAL_ENV="%s"\nexport PATH="%s/bin:$PATH"\n' "$VIRTUAL_ENV" "$VIRTUAL_ENV"
   fi
 } | _proveo_write_block "$home/.bashrc" "$PROVEO_RC_START" "$PROVEO_RC_END"
}

proveo_seed() {
 local target="${1:-${PROVEO_TARGET:-}}"
 local home; home="$(_proveo_agent_home)"
 [[ -n "$target" && -n "$home" ]] || return 0

 proveo_sync_state restore || true

 proveo_sync_config restore || true

 case "$target" in
 claudecode) render_subagents claudecode "$home/.claude/agents" "${CLAUDECODE_RESEED:-0}" ;;
 cursor) render_subagents cursor "$home/.cursor/agents" "${CURSOR_RESEED:-0}" ;;
 cecli) render_subagents cecli "${CECLI_HOME:-$home/.cecli}/agents" "${CECLI_RESEED:-0}" ;;
 opencode) render_subagents opencode "$home/.config/opencode/agents" "${OPENCODE_RESEED:-0}" ;;
 esac

 accept_workspace_trust "$(_proveo_scan_root)"

 proveo_sync_tools restore || true

 proveo_provision_toolchain

 proveo_wire_config "$target"

 proveo_compose_house_rules "$target"
 proveo_apply_ui_defaults "$target"
 proveo_install_claude_hooks "$target"
 proveo_seed_browser_skills "$target"

 # PROVEO_CHROME_BRIDGE. SPEC: _spec/defs/claudecode/chrome-bridge.puml
 proveo_chrome_bridge "$target"
}
