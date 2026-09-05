#!/usr/bin/env bash
# SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
set -u
payload="$(cat 2>/dev/null || true)"
proc="${PROVEO_PROC:-/proc}"

cwd="$(printf '%s' "$payload" | sed -n 's/.*"cwd"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
gone=""
case "$cwd" in *" (deleted)") gone="$cwd" ;; esac

if [[ -z "$gone" && -d "$proc/self" ]]; then
  p="${PPID:-}"
  while [[ -n "$p" && "$p" != "0" && "$p" != "1" ]]; do
    link="$(readlink "$proc/$p/cwd" 2>/dev/null || true)"
    case "$link" in *" (deleted)") gone="$link"; break ;; esac
    p="$(awk '/^PPid:/{print $2}' "$proc/$p/status" 2>/dev/null || true)"
  done
fi
[[ -n "$gone" ]] || exit 0

cat >&2 <<EOF
proveo cwd-guard: the shell's working directory is gone from this sandbox's view — "${gone}".
The path still exists on the host; the sandbox's filesystem passthrough (virtiofs) dropped its directory entry under the running agent. Every Bash call will fail with exit 1 and no output until this session is restarted — Claude Code has no fallback for a vanished cwd (anthropics/claude-code#52747). Read, Edit and Write still work.
Do not retry Bash and do not diagnose the harness. Finish file-only work, report this state, and ask the operator to restart with \`proveo run <target> --continue\`. Clone mode, proveo's default, keeps the workspace off virtiofs so this cannot recur.
EOF
exit 2
