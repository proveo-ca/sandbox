#!/usr/bin/env bash
# Thin wrapper → proveo-entrypoint verify (Go: internal/verify).
# Kept as detect-verify.sh so baked image paths (/opt/proveo/lib/) keep working.
# SPEC: _spec/defs/cursor/cursor-paradigm.puml

detect_verify_commands() {
  local root="${1:-$(pwd)}"
  if command -v proveo-entrypoint >/dev/null 2>&1; then
    proveo-entrypoint verify "$root"
    return 0
  fi
  # Dev fallback when the binary is not baked yet (host contract tests).
  if command -v go >/dev/null 2>&1; then
    local dir repo=""
    dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    while [[ "$dir" != "/" ]]; do
      if [[ -f "$dir/go.mod" ]]; then
        repo="$dir"
        break
      fi
      dir="$(dirname "$dir")"
    done
    if [[ -n "$repo" ]]; then
      ( cd "$repo" && go run ./cmd/proveo-entrypoint verify "$root" )
      return 0
    fi
  fi
  echo "detect_verify_commands: proveo-entrypoint not found" >&2
  return 1
}
