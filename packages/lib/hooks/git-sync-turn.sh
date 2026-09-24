#!/usr/bin/env bash
# SPEC: _spec/packages/lib/git-sync-turn.puml
set -u
export GIT_TERMINAL_PROMPT=0
[[ "${PROVEO_GIT_SYNC_MSG_INFLIGHT:-}" == 1 ]] && exit 0
payload=""
if [ ! -t 0 ]; then
  payload="$(cat 2>/dev/null || true)"
fi

_json_str() {
  local s="$1"
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$s"
    return
  fi
  if command -v jq >/dev/null 2>&1; then
    jq -n --arg s "$s" '$s'
    return
  fi
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\r'/\\r}
  printf '"%s"' "$s"
}

_json_get() {
  local key="$1"
  [[ -n "$payload" ]] || return 0
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$payload" | python3 -c '
import json,sys
key=sys.argv[1]
try:
    j=json.load(sys.stdin)
except Exception:
    raise SystemExit(0)
v=j.get(key)
if v is None:
    raise SystemExit(0)
if isinstance(v, bool):
    print("true" if v else "false")
else:
    print(v)
' "$key" 2>/dev/null || true
    return
  fi
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$payload" | jq -r --arg k "$key" 'if .[$k] == null then empty elif (.[$k]|type)=="boolean" then (if .[$k] then "true" else "false" end) else (.[$k]|tostring) end' 2>/dev/null || true
  fi
}

_git_sync_trace() {
  local kind="$1" error="${2:-}" path="${PROVEO_GIT_SYNC_TRACE:-}"
  [[ "$path" == off ]] && return 0
  if [[ -z "$path" ]]; then
    [[ -n "${git_dir:-}" ]] || return 0
    path="$git_dir/proveo-git-sync.ndjson"
  fi
  printf '{"ts":%s,"dialect":%s,"event":%s,"result":%s,"error":%s}\n' \
    "$(date +%s)" "$(_json_str "$dialect")" "$(_json_str "${event:-}")" \
    "$(_json_str "$kind")" "$(_json_str "$error")" >>"$path" 2>/dev/null || true
}

_git_sync_emit() {
  local kind="$1" reason="${2:-}"
  if (( ${#reason} > 2000 )); then
    reason="${reason:0:2000}…"
  fi
  _git_sync_trace "$kind" "${reason:-$err_msg}"
  case "$dialect" in
    cursor)
      if [[ "$kind" == block && -n "$reason" ]]; then
        printf '{"followup_message":%s}\n' "$(_json_str "$reason")"
      else
        printf '{}\n'
      fi
      ;;
    stop)
      if [[ "$kind" == block && -n "$reason" ]]; then
        printf '{"decision":"block","reason":%s}\n' "$(_json_str "$reason")"
      fi
      ;;
    *)
      [[ "$kind" == block && -n "$reason" ]] && printf '%s\n' "$reason" >&2
      ;;
  esac
}

_gen_commit_subject() {
  local diff prompt subj model timeout_s="${PROVEO_GIT_SYNC_MSG_TIMEOUT:-20}"
  command -v timeout >/dev/null 2>&1 || return 1
  diff="$(git diff --cached -- . 2>/dev/null | head -c 4000)"
  [[ -n "$diff" ]] || return 1
  prompt="Write ONE git commit subject line (max 72 chars, imperative mood, no prefix, no quotes, no surrounding punctuation) summarizing this staged diff. Reply with only that line, nothing else.

$diff"
  if command -v claude >/dev/null 2>&1; then
    model="${PROVEO_GIT_SYNC_MODEL_CLAUDE:-claude-haiku-4-5-20251001}"
    subj="$(PROVEO_GIT_SYNC_MSG_INFLIGHT=1 timeout "$timeout_s" claude -p --model "$model" --max-turns 1 "$prompt" 2>/dev/null)"
  elif command -v codex >/dev/null 2>&1; then
    model="${PROVEO_GIT_SYNC_MODEL_CODEX:-gpt-5-nano}"
    subj="$(PROVEO_GIT_SYNC_MSG_INFLIGHT=1 timeout "$timeout_s" codex exec --model "$model" --sandbox read-only --ask-for-approval never "$prompt" 2>/dev/null)"
  elif command -v cursor-agent >/dev/null 2>&1; then
    model="${PROVEO_GIT_SYNC_MODEL_CURSOR:-}"
    [[ -n "$model" ]] || return 1
    subj="$(PROVEO_GIT_SYNC_MSG_INFLIGHT=1 timeout "$timeout_s" cursor-agent -p --force --sandbox disabled --trust --model "$model" "$prompt" 2>/dev/null)"
  elif command -v opencode >/dev/null 2>&1; then
    model="${PROVEO_GIT_SYNC_MODEL_OPENCODE:-anthropic/claude-haiku-4-5}"
    subj="$(PROVEO_GIT_SYNC_MSG_INFLIGHT=1 timeout "$timeout_s" opencode run -m "$model" "$prompt" 2>/dev/null)"
  else
    return 1
  fi
  subj="$(printf '%s' "$subj" | tr -d '\r' | sed -n '1p')"
  subj="${subj#"${subj%%[![:space:]]*}"}"
  subj="${subj%"${subj##*[![:space:]]}"}"
  subj="${subj#\"}"; subj="${subj%\"}"
  subj="${subj#\'}"; subj="${subj%\'}"
  [[ -n "$subj" ]] || return 1
  (( ${#subj} > 72 )) && subj="${subj:0:72}"
  printf '%s' "$subj"
}

err_msg=""
git_dir=""
event="$(_json_get hook_event_name)"
dialect="${PROVEO_GIT_SYNC_DIALECT:-}"
case "$dialect" in
  claude|codex) dialect=stop ;;
esac
if [[ -z "$dialect" ]]; then
  if [[ -n "$(_json_get loop_count)" ]]; then
    dialect=cursor
  elif [[ "$event" == Stop ]]; then
    dialect=stop
  elif [[ -n "$(_json_get stop_hook_active)" ]]; then
    dialect=stop
  else
    dialect=idle
  fi
fi

off="$(printf '%s' "${PROVEO_GIT_SYNC:-auto}" | tr '[:upper:]' '[:lower:]')"
case "$off" in off|false|0|no|disable|disabled)
  _git_sync_emit allow ""
  exit 0
  ;;
esac

status="$(_json_get status)"
if [[ "$status" == aborted ]]; then
  _git_sync_emit allow ""
  exit 0
fi

continued=0
case "$(_json_get stop_hook_active)" in true|1) continued=1 ;; esac
loop="$(_json_get loop_count)"
[[ -n "$loop" && "$loop" != 0 ]] && continued=1

fail() {
  local msg="$1"
  err_msg="$msg"
  if (( continued )) || [[ "$dialect" == idle ]]; then
    printf '%s\n' "$msg" >&2
    _git_sync_emit allow ""
    exit 0
  fi
  _git_sync_emit block "$msg"
  exit 0
}

start="$(_json_get cwd)"
[[ -n "$start" ]] || start="${PWD:-.}"
command -v git >/dev/null 2>&1 || { _git_sync_emit allow ""; exit 0; }
root="$(git -C "$start" rev-parse --show-toplevel 2>/dev/null)" || { _git_sync_emit allow ""; exit 0; }
cd "$root" || { _git_sync_emit allow ""; exit 0; }

git_dir="$(git rev-parse --git-dir 2>/dev/null)" || { _git_sync_emit allow ""; exit 0; }
if command -v flock >/dev/null 2>&1; then
  lock="$git_dir/proveo-git-sync.lock"
  exec 9>"$lock" 2>/dev/null && flock -w 60 9 2>/dev/null || true
fi

excludes=(
  ':(exclude,glob)**/.env'
  ':(exclude,glob)**/.env.*'
  ':(exclude,glob)**/*.pem'
  ':(exclude,glob)**/*.key'
  ':(exclude,glob)**/credentials.json'
  ':(exclude,glob)**/auth.json'
  ':(exclude,glob)**/id_rsa'
  ':(exclude,glob)**/id_ed25519'
)
if ! git add -A -- . "${excludes[@]}" >/dev/null 2>&1; then
  git add -A -- . >/dev/null 2>&1 || fail "git add failed"
  git reset -q -- '.env' '.env.*' '*.pem' '*.key' 'credentials.json' 'auth.json' 'id_rsa' 'id_ed25519' >/dev/null 2>&1 || true
fi

if ! git diff --cached --quiet 2>/dev/null; then
  body="$(git status --short | head -n 20 | tr -d '\r')"
  subject="persist turn"
  case "$(printf '%s' "${PROVEO_GIT_SYNC_MSG:-auto}" | tr '[:upper:]' '[:lower:]')" in
    off|false|0|no|disable|disabled) ;;
    *) gen="$(_gen_commit_subject)" && [[ -n "$gen" ]] && subject="$gen" ;;
  esac
  msg="$(printf '[proveo] %s\n\n%s\n' "$subject" "$body")"
  if ! err="$(git commit -m "$msg" 2>&1)"; then
    fail "git commit failed: $err"
  fi
fi

git rev-parse --verify HEAD >/dev/null 2>&1 || { _git_sync_emit allow ""; exit 0; }
branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
[[ "$branch" == HEAD || -z "$branch" ]] && { _git_sync_emit allow ""; exit 0; }

remote=""
if git remote get-url origin >/dev/null 2>&1; then
  remote=origin
else
  remote="$(git remote 2>/dev/null | head -n 1 || true)"
fi
[[ -n "$remote" ]] || { _git_sync_emit allow ""; exit 0; }
url="$(git remote get-url "$remote" 2>/dev/null || true)"
[[ -d "$url" && ! -w "$url" ]] && { _git_sync_emit allow ""; exit 0; }

if git rev-parse '@{u}' >/dev/null 2>&1; then
  ahead="$(git rev-list --count '@{u}..HEAD' 2>/dev/null || printf '0')"
  if (( ahead > 0 )); then
    if ! err="$(git push 2>&1)"; then
      fail "git push failed: $err"
    fi
  fi
else
  if ! err="$(git push -u "$remote" HEAD 2>&1)"; then
    fail "git push failed: $err"
  fi
fi

_git_sync_emit allow ""
exit 0
