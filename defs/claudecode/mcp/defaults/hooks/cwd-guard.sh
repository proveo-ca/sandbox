#!/usr/bin/env bash
# SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
# PreToolUse(Bash) guard: name the one failure the Bash tool cannot name itself.
#
# In an sbx sandbox the workspace is a virtiofs passthrough of the host path, and
# its directory entry can be dropped inside the VM while the host directory lives
# on. Claude Code then records its working directory as "<path> (deleted)", and
# every Bash spawn fails — exit 1, no output, `true` included, subagents too —
# while Read/Edit/Write (absolute paths) keep working. Claude Code ≥ 2.1.119 has no
# fallback for that (anthropics/claude-code#52747), so the model retries, invents
# causes, and burns the session. Hooks DO still run: Claude Code falls back to the
# session start directory to spawn them. So this is the one place a sentence can
# reach the model at the moment it matters.
#
# Exit 2 blocks the call and hands stderr to the model. Everything else exits 0
# and says nothing — a guard that speaks on a healthy shell is noise. PROVEO_PROC
# points the /proc walk elsewhere so the logic is testable on a host without one.
set -u
payload="$(cat 2>/dev/null || true)"
proc="${PROVEO_PROC:-/proc}"

# 1. What Claude Code itself believes its cwd is — the JSON is its only channel.
cwd="$(printf '%s' "$payload" | sed -n 's/.*"cwd"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
gone=""
case "$cwd" in *" (deleted)") gone="$cwd" ;; esac

# 2. What the kernel says about the processes above us: the Claude Code process
#    holds the unlinked directory as its cwd, and /proc shows it as such.
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
