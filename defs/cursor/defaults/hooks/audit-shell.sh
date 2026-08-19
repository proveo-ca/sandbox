#!/usr/bin/env bash
# SPEC: _spec/defs/cursor/cursor-topology.puml, _spec/defs/cursor/cursor-paradigm.puml
log="${PROVEO_CURSOR_AUDIT_LOG:-${HOME:-/tmp}/.cursor/audit-shell.ndjson}"
mkdir -p "$(dirname "$log")" 2>/dev/null || true
payload="$(cat)"
printf '%s\n' "$payload" >>"$log" 2>/dev/null || true
printf '{"permission":"allow"}\n'
