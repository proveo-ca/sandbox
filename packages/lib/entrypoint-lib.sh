#!/usr/bin/env bash
# SPEC: _spec/packages/lib/steps.puml, _spec/packages/lib/language-server-provisioning.puml, _spec/_paradigms/runtime-user-boundary.puml, _spec/cmd/proveo-entrypoint/prep-process-boundary.puml, _spec/_runtimes/toolchain-provisioning.puml, _spec/internal/entrypoint/model-alias-bridges.puml
# Shared entrypoint functions for Proveo coding harnesses

# ── 0. Make an Arbitrary Run-As UID Usable (root-free) ──────
# Wrappers launch containers with `--user $(id -u):$(id -g)`; give that uid a
# passwd entry and a writable HOME without root. Call first in every entrypoint.
ensure_runtime_user() {
 local uid gid
 uid="$(id -u)"; gid="$(id -g)"

 # Synthesize a passwd entry so getpwuid-based tooling doesn't choke on
 # "I have no name!"; only possible when /etc/passwd is writable.
 if ! getent passwd "$uid" >/dev/null 2>&1 && [[ -w /etc/passwd ]]; then
 printf 'agent:x:%s:%s:agent:%s:/bin/bash\n' "$uid" "$gid" "${HOME:-/tmp}" >> /etc/passwd
 fi

 # Guarantee a writable HOME. The baked home (owned by the build user) is not
 # writable by a different uid until the deferred chmod lands, so fall back.
 if [[ -z "${HOME:-}" || ! -w "${HOME:-/}" ]]; then
 export HOME=/tmp
 fi
}

if [[ -z "${HOME:-}" || ! -w "${HOME:-/}" ]]; then
 export HOME=/tmp
fi

# ── 1. Set Working Directory ────────────────────────────────
# PROVEO_WORKDIR wins over the caller's default, because on the sbx backend the
# workspace is mounted at its OWN host path and /app holds nothing. Launching the
# agent there made it report "not a git repository" and then block on a trust
# dialog for a folder that is not the project — a prompt no automation answers, and
# the run died with the sandbox 30s later. proveo-entrypoint chdirs correctly but
# cannot move its parent shell (_spec/cmd/proveo-entrypoint/prep-process-boundary.puml),
# so the shell has to make the same choice for itself.
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

# Claude Code asks the operator to confirm a folder it has not seen before. That
# prompt cannot be answered by a run that is not being watched, so the workspace is
# marked trusted up front — the same thing sbx's own claude kit does from
# setup.install. The file is MERGED, never rewritten: it is the operator's real
# ~/.claude.json, mounted in from the proveo home.
accept_workspace_trust() {
 local dir="${1:-$PWD}" home="${HOME:-}"
 [[ -n "$home" && -d "$home" ]] || return 0
 command -v node >/dev/null 2>&1 || return 0
 PROVEO_TRUST_DIR="$dir" node -e '
   const fs = require("fs"), path = process.env.HOME + "/.claude.json";
   let j = {};
   try { j = JSON.parse(fs.readFileSync(path, "utf8")) || {}; } catch (e) {}
   j.projects = j.projects || {};
   const d = process.env.PROVEO_TRUST_DIR;
   j.projects[d] = Object.assign({}, j.projects[d], { hasTrustDialogAccepted: true });
   fs.writeFileSync(path, JSON.stringify(j, null, 2));
 ' 2>/dev/null || true
}

# ── 2. Find and Load .env ───────────────────────────────────
find_env_file() {
 # 1. Check current working directory
 if [[ -f .env ]]; then
 printf '%s/.env' "$(pwd)"
 return 0
 fi

 # 2. Check git root via git command (if available)
 if command -v git >/dev/null 2>&1; then
 local git_root
 git_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
 if [[ -n "$git_root" && -f "$git_root/.env" ]]; then
 printf '%s' "$git_root/.env"
 return 0
 fi
 fi

 # 3. Check git root via directory traversal (pure Bash fallback)
 local dir; dir="$(pwd)"
 while [[ "$dir" != "/" ]]; do
 if [[ -d "$dir/.git" && -f "$dir/.env" ]]; then
 printf '%s/.env' "$dir"
 return 0
 fi
 dir="$(dirname "$dir")"
 done

 # 4. Check for any .env inside any subdirectories (maxdepth 5)
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
 proxy|firewall)
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

 # Bridge Google/Gemini API key aliases
 if [[ -z "${GOOGLE_GENERATIVE_AI_API_KEY:-}" ]]; then
 if [[ -n "${GEMINI_API_KEY:-}" ]]; then
 export GOOGLE_GENERATIVE_AI_API_KEY="$GEMINI_API_KEY"
 elif [[ -n "${GOOGLE_API_KEY:-}" ]]; then
 export GOOGLE_GENERATIVE_AI_API_KEY="$GOOGLE_API_KEY"
 fi
 fi

 # Reverse bridge for tools expecting GEMINI_API_KEY or GOOGLE_API_KEY
 if [[ -n "${GOOGLE_GENERATIVE_AI_API_KEY:-}" ]]; then
 export GEMINI_API_KEY="${GEMINI_API_KEY:-$GOOGLE_GENERATIVE_AI_API_KEY}"
 export GOOGLE_API_KEY="${GOOGLE_API_KEY:-$GOOGLE_GENERATIVE_AI_API_KEY}"
 fi

 apply_broker_sentinel
}

# Rewrite brokered credential env vars to a sentinel so the agent process never
# holds the real key (MITM injects the real secret). firewall mode only.
apply_broker_sentinel() {
 case "$(printf '%s' "${PROVEO_EGRESS_MODE:-}" | tr '[:upper:]' '[:lower:]')" in
 firewall) ;;
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

# ── 2b. Git Identity from Environment ───────────────────────
# Bridge GIT_AUTHOR_*/GIT_COMMITTER_* env into git's config-env (GIT_CONFIG_*) so
# config reads resolve file-free; existing identity wins. Optional arg: repo dir.
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

# ── 2c. Git Context Report ──────────────────────────────────
# Read-only startup report: repo/remote status, commit identity, gh session.
# Call after load_env/bridge_git_identity. Optional arg: directory to inspect.
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
 # `gh auth status` validates GH_TOKEN/config sessions over the network;
 # cap it so locked-down egress modes can't stall startup.
 if timeout 5s gh auth status >/dev/null 2>&1; then
 echo "✅ gh session authenticated"
 else
 echo "🔎 gh session not authenticated (set GH_TOKEN or GITHUB_TOKEN)"
 fi
 fi
}

# ── 2d. Git Access (safe.directory + scoped worktree) ───────
# SPEC: _spec/internal/workspace/git-mount-by-scope.puml
# Both run in the ENTRYPOINT SHELL, not the Go prelude: `proveo-entrypoint prep`
# is a subprocess, so anything it exports dies with it. Only the shell that execs
# the agent can hand it environment.
#
# Under a subdir scope /app is the image's own directory, so its owner differs
# from the run-as uid and git refuses everything with "dubious ownership".
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

# Only under a subdir scope (PROVEO_SCOPE_REL): the container's worktree root is
# /app but only part of the repo is mounted there, so git measures the whole-repo
# index against a partial tree. Point git at a COPY of the index and mark the
# unmounted paths skip-worktree, so nothing is written into the host's .git.
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

  # Absent-at-STARTUP means "not mounted", never "the agent deleted it" — the
  # agent has not run yet. Deletions it makes later still show normally.
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

# ── 3. Attach RTK Repository ────────────────────────────────
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

# ── 4. Smoke Test Mode ──────────────────────────────────────
run_smoke_test() {
 local target_name="$1"
 if [[ "${PROVEO_SMOKE_TEST:-0}" == "1" ]]; then
 echo "✅ PROVEO_SMOKE_READY ${PROVEO_SMOKE_TARGET:-$target_name}"
 exec sleep infinity
 fi
}


# ── 5. Tool Sourcing & Command Version Helpers ──────────────
# Cecli style command version check (fallback cmd [args])
command_version_cecli() {
 local fallback="$1"; shift
 timeout 5s "$@" 2>/dev/null || echo "$fallback"
}

# Opencode style command version check (cmd fallback [args])
command_version_opencode() {
 local name="$1"; shift
 local fallback="${1:-n/a}"; shift || true
 if ! command -v "$name" >/dev/null 2>&1; then
 echo "$fallback"
 return 0
 fi
 timeout 5s "$name" "$@" 2>/dev/null || echo "$fallback"
}

# ── 6. Declarative Env Var Bridges Mapping ──────────────────

# _normalize_model prefixes a bare model id with its provider (mirrors the
# opencode model registry); an id that already contains "/" is returned as-is.
_normalize_model() {
 local m="$1" lower
 [[ -n "$m" ]] || return 0
 case "$m" in */*) printf '%s' "$m"; return 0 ;; esac
 lower="$(printf '%s' "$m" | tr '[:upper:]' '[:lower:]')"
 # OpenAI reasoning ids are "o" followed by a digit (o1, o3, o4-mini, …).
 case "$lower" in
 gpt-* | chatgpt-* | o[0-9]*) printf 'openai/%s' "$m" ;;
 claude-*) printf 'anthropic/%s' "$m" ;;
 grok-*) printf 'xai/%s' "$m" ;;
 gemini-*) printf 'google/%s' "$m" ;;
 deepseek-*) printf 'deepseek/%s' "$m" ;;
 *) printf '%s' "$m" ;;
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
 # Provider key aliases only. Model bridges are declared in defs/bridges/<harness>.tsv
 # and applied by apply_model_bridges; keeping them here too is what let the opencode
 # mapping drift between this file and internal/entrypoint.
 _apply_env_bridge GEMINI_API_KEY GOOGLE_GENERATIVE_AI_API_KEY "" "" ""
 _apply_env_bridge GOOGLE_API_KEY GOOGLE_GENERATIVE_AI_API_KEY "" "" ""
}

# ── Model bridges — declared once in defs/bridges/<harness>.tsv ──────────────
# The same table drives the prompt header on the host (internal/provider reads it
# embedded), so what an operator is shown before launch is what the container sets.
# Shell has to be the executor: proveo-entrypoint prep cannot export into its parent
# (_spec/cmd/proveo-entrypoint/prep-process-boundary.puml).
_apply_model_bridge() {
 local targets="$1" roles="$2" default="$3" transform="$4"
 local val="" r t
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
 case "$transform" in
 normalize) val="$(_normalize_model "$val")" ;;
 bare) val="${val##*/}" ;;
 esac
 IFS=',' read -ra target_list <<< "$targets"
 for t in "${target_list[@]}"; do
  # An explicit tool-specific value already in the environment always wins.
  printenv "$t" >/dev/null 2>&1 && continue
  export "$t=$val"
 done
}

apply_model_bridges() {
 local harness="$1"
 local file="${PROVEO_BRIDGES_DIR:-/opt/proveo/bridges}/$harness.tsv"
 [[ -f "$file" ]] || return 0
 local slot targets roles default transform
 # Row order is load-bearing: a "$VAR" default must run after the row that sets VAR.
 while IFS=$'\t' read -r slot targets roles default transform; do
  [[ -n "$slot" && "$slot" != \#* ]] || continue
  _apply_model_bridge "$targets" "$roles" "$default" "$transform"
 done < "$file"
}

# ── 7. Automatic Project-Level Tools Installer ──────────────

_proveo_auto_install_enabled() {
  case "$(printf '%s' "${PROVEO_AUTO_INSTALL_TOOLS:-true}" | tr '[:upper:]' '[:lower:]')" in
    false|0|no|off|disable|disabled) return 1 ;;
  esac
  return 0
}

_proveo_tool_path() {
  mkdir -p "${HOME}/.local/bin"
  case ":${PATH}:" in
    *":${HOME}/.local/bin:"*) ;;
    *) export PATH="${HOME}/.local/bin:${PATH}" ;;
  esac
  case ":${PATH}:" in
    *":${HOME}/.local/share/mise/shims:"*) ;;
    *) export PATH="${HOME}/.local/share/mise/shims:${PATH}" ;;
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
  _proveo_bounded 5 gh auth token 2>/dev/null | head -n1
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

_install_go() {
  local version="$1"
  if curl -fsSL --connect-timeout 5 --max-time 120 \
       -o "${HOME}/.local/bin/g" \
       https://github.com/stefanmaric/g/releases/latest/download/g; then
    chmod +x "${HOME}/.local/bin/g"
    "${HOME}/.local/bin/g" install -y "$version" >/dev/null 2>&1 \
      || echo "WARN: g could not install Go ${version}"
  elif command -v mise >/dev/null 2>&1; then
    mise use -g "go@${version}" >/dev/null 2>&1 \
      || echo "WARN: mise could not install Go ${version}"
  else
    echo "WARN: failed to fetch g; Go not installed"
  fi
}

_proveo_lock_installs() {
  command -v flock >/dev/null 2>&1 || return 0
  local dir="${HOME}/.local/share/proveo"
  mkdir -p "$dir" 2>/dev/null || return 0
  exec 9>"${dir}/install.lock" 2>/dev/null || return 0
  if ! flock -w "${PROVEO_INSTALL_LOCK_WAIT:-300}" 9; then
    echo "⏳ another proveo run is provisioning tools under ${HOME}; skipping installs this run"
    _proveo_unlock_installs
    return 1
  fi
  return 0
}

_proveo_unlock_installs() { exec 9>&- 2>/dev/null || true; }

ensure_project_tools() {
 _proveo_tool_path
 _proveo_auto_install_enabled || return 0
 _proveo_lock_installs || return 0

 # Bounded network so a blackholed egress can't hang the container at startup.
 local -a npm_net=(--fetch-timeout=60000 --fetch-retries=1)

 # 1. NX Detection & Installation
 if [[ -f nx.json ]]; then
 if ! command -v nx >/dev/null 2>&1; then
 echo "📦 Detected nx.json. Dynamically installing nx..."
 npm install -g "${npm_net[@]}" --prefix "${HOME}/.local" nx@latest || echo "⚠️ Failed to dynamically install nx"
 fi
 fi

 # 2. Turbo Detection & Installation
 if [[ -f turbo.json ]]; then
 if ! command -v turbo >/dev/null 2>&1; then
 echo "📦 Detected turbo.json. Dynamically installing turbo..."
 npm install -g "${npm_net[@]}" --prefix "${HOME}/.local" turbo@latest || echo "⚠️ Failed to dynamically install turbo"
 fi
 fi

 # 3. Mise Detection & Installation
 if [[ -f mise.toml || -f mise.local.toml || -f .mise.toml || -f .mise.local.toml || -d mise || -d .mise || -f .tool-versions ]]; then
 if ! command -v mise >/dev/null 2>&1; then
 echo "📦 Detected mise config or .tool-versions. Dynamically installing mise..."
 # Download first so a blocked/timed-out fetch is detected via curl's own
 # exit status (piping straight to sh masks it: an empty body exits 0).
 local mise_installer
 mise_installer="$(mktemp)"
 if curl -fsSL --connect-timeout 5 --max-time 120 https://mise.run -o "$mise_installer"; then
 MISE_INSTALL_PATH="${HOME}/.local/bin/mise" sh "$mise_installer" || echo "⚠️ mise install script failed"
 else
 npm install -g "${npm_net[@]}" --prefix "${HOME}/.local" @jdx/mise@latest || echo "⚠️ Failed to dynamically install mise"
 fi
 rm -f "$mise_installer"
 fi
 fi

 # 4. Go Detection & Installation
 if [[ -f go.mod || -f go.work ]] || compgen -G "*.go" >/dev/null 2>&1; then
 export GOROOT="${GOROOT:-${HOME}/.go}"
 export GOPATH="${GOPATH:-${HOME}/go}"
 export PATH="${GOROOT}/bin:${GOPATH}/bin:${PATH}"

 local go_version="latest" pinned="" current=""
 if [[ -f go.mod ]]; then
 pinned="$(sed -n 's/^toolchain go\([0-9][^ ]*\).*/\1/p' go.mod | head -n1)"
 [[ -n "$pinned" ]] && go_version="$pinned"
 fi
 current="$(_go_current_version)"

 if [[ -z "$current" ]]; then
 echo "Detected a Go project. Dynamically installing Go ${go_version} via g..."
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

# ── 7c. Python Environment (detect → provision → activate) ──
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

# Presence is not capability: python3-minimal satisfies `command -v python3`
# while `python3 -m venv` fails, which is how a decoy interpreter gets mistaken
# for a usable one.
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

ensure_python_env() {
  _proveo_tool_path
  local scan="${1:-$(pwd)}" kind ver env_dir spec
  kind="$(_py_project_kind "$scan")"
  [[ -n "$kind" ]] || return 0
  _proveo_auto_install_enabled || return 0
  case "$(printf '%s' "${PROVEO_PYTHON_ENV:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) return 0 ;;
  esac
  command -v mise >/dev/null 2>&1 || { echo "ℹ️  mise not on PATH — skipping Python environment"; return 0; }

  _proveo_lock_installs || return 0
  ver="$(_py_requested_version "$scan")"
  env_dir="$(_py_env_dir "$scan")"

  if ! _py_venv_capable; then
    echo "🐍 Provisioning Python ${ver} (the image's python3 cannot create a venv)..."
    _mise_install "python@${ver}" "$(_proveo_github_token)" >/dev/null 2>&1 \
      || { echo "⚠️  Could not provision Python ${ver}; skipping environment setup"; _proveo_unlock_installs; return 0; }
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

  _py_build_env "$kind" "$scan" "$env_dir"
  _proveo_unlock_installs
  _py_activate "$env_dir" "$scan"
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
  [[ $rc -eq 0 ]] || echo "⚠️  Python dependency install did not complete; the environment may be partial"
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
  echo "🐍 Python: $("$env_dir/bin/python" --version 2>&1) · env ${env_dir}"
}

# ── 8. Workspace LSP Detection (shared) ─────────────────────

# LSP maps as case-statement lookups (bash-3.2-safe: no associative arrays).
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
  local gh_token="${1:-}" home="${HOME}/.local/share/proveo/jdtls" tarball rc
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

  mkdir -p "${HOME}/.local/bin"
  cat > "${HOME}/.local/bin/jdtls" <<PROVEO_JDTLS
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
  chmod +x "${HOME}/.local/bin/jdtls"
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
  done < <(find "$scan_root" \
             \( -name .git -o -name node_modules -o -name vendor -o -name target \
                -o -name dist -o -name build -o -name .next -o -name .nx \
                -o -name .turbo -o -name .cache -o -name .gradle -o -name .tox \
                -o -name .venv -o -name venv -o -name __pycache__ \
                -o -name .mypy_cache -o -name .pytest_cache -o -name .ruff_cache \
                -o -name .terraform -o -name Pods \
                -o -name .pnpm-store -o -name .npm -o -name .yarn \) -prune \
             -o -type f -print0 2>/dev/null)
}

# SPEC: _spec/packages/lib/language-server-provisioning.puml
# Knobs: PROVEO_AUTO_INSTALL_TOOLS · PROVEO_LSP_INSTALL=off|<csv> ·
# PROVEO_LSP_MIN_FILES · PROVEO_LSP_INSTALL_TIMEOUT · GITHUB_TOKEN.
# Call directly, never through a pipe: it exports PATH.
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

# detect_workspace_lsps prints "lang|count|cmd|arg…|ext1,ext2" per language whose
# LSP server is installed, ranked by file count desc, then popularity, then name.
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
    # Deterministic (sorted, unique) extension list regardless of filesystem order.
    extcsv="$(printf '%s' "$extcsv" | tr ',' '\n' | sort -u | paste -sd, -)"
    printf '%s|%s|%s|%s\n' "$lang" "$cnt" "${server// /|}" "$extcsv"
  done
}

configure_claude_lsp() {
  command -v jq >/dev/null 2>&1 || return 0
  local scan="${1:-$(pwd)}" lsp_json plugdir="${HOME}/.claude/skills/proveo-lsp"
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
  [ "$lsp_json" = "{}" ] && return 0

  mkdir -p "$plugdir/.claude-plugin"
  printf '{"name":"proveo-lsp","description":"Workspace language servers (auto-detected)","version":"1.0.0"}\n' \
    > "$plugdir/.claude-plugin/plugin.json"
  printf '%s\n' "$lsp_json" > "$plugdir/.lsp.json"
  echo "🧠 LSP code intelligence (Claude Code plugin): $(printf '%s' "$lsp_json" | jq -r 'keys_unsorted | join(" ")')"
}

# ── 9. Agent Evidence (verbosity) ───────────────────────────
agent_evidence_verbose() {
 [[ "${PROVEO_AGENT_EVIDENCE:-verbose}" != "default" ]]
}

# report_agent_evidence <flag>... — one line naming what the level bought, so an
# operator reading the transcript can tell a quiet run from a suppressed one.
report_agent_evidence() {
 if (( $# > 0 )); then
 echo "🔎 agent evidence: verbose — $*"
 else
 echo "🔎 agent evidence: default (harness defaults, no extra narration)"
 fi
}

# ── Subagent definitions — composed at runtime, never duplicated on disk ──
# One body per agent lives in /opt/proveo/subagents/<name>.md with {{TOKEN}}
# placeholders; the frontmatter schema is per-harness because Claude Code declares
# a tools allowlist, opencode and cecli declare mode+permission, and cursor
# declares readonly. A symlink cannot express that split (it is all-or-nothing,
# and Docker COPY preserves the link so it dangles in the image), so the two
# halves are joined here, on first run, into the harness's own agents directory.
#
#   render_subagents <harness> <dest-dir> [reseed]
render_subagents() {
 local harness="$1" dst="$2" reseed="${3:-0}"
 local src="${PROVEO_SUBAGENTS_DIR:-/opt/proveo/subagents}"
 local fmdir="$src/_frontmatter/$harness" varfile="$src/_vars/$harness.env"

 [[ -d "$fmdir" ]] || return 0
 # set -e is on: a read-only home must degrade to "no subagents", not kill the run.
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

# ── The FILE-SHAPED half of harness setup, invoked identically by both backends ──
# Under sbx this runs from the Kit's setup.startup hook; on docker the entrypoint
# calls it before exec. It may ONLY do work that outlives its own process: a setup
# command runs in its own shell, so anything it exported would never reach the
# agent. Env-shaped work is resolved host-side and arrives as values.
#
# Idempotent by construction — every step is "create if absent" or a merge — because
# a re-attached sandbox runs the startup hook again.
proveo_seed() {
 local target="${1:-${PROVEO_TARGET:-}}"
 # PROVEO_HOME first: sbx runs this under `user: "1000"`, which resets HOME from
 # /etc/passwd, so $HOME here is NOT the home the agent will run with.
 local home="${PROVEO_HOME:-${HOME:-}}"
 [[ -n "$target" && -n "$home" ]] || return 0

 case "$target" in
 claudecode) render_subagents claudecode "$home/.claude/agents" "${CLAUDECODE_RESEED:-0}" ;;
 cursor) render_subagents cursor "$home/.cursor/agents" "${CURSOR_RESEED:-0}" ;;
 cecli) render_subagents cecli "$home/agents" "${CECLI_RESEED:-0}" ;;
 opencode) render_subagents opencode "$home/.config/opencode/agents" "${OPENCODE_RESEED:-0}" ;;
 esac

 accept_workspace_trust "${PROVEO_WORKDIR:-$PWD}"
}
