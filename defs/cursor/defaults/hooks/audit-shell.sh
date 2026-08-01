#!/usr/bin/env bash
# SPEC: _spec/defs/cursor/cursor-topology.puml, _spec/defs/cursor/cursor-paradigm.puml
# Enterprise-layer beforeShellExecution hook: append the shell request payload
# (JSON on stdin: command, cwd, conversation_id, ...) to an NDJSON audit log,
# then allow. This hook is AUDIT ONLY and fail-open by design — enforcement
# belongs to permissions.deny (survives --force) and the network egress layer.
log="${PROVEO_CURSOR_AUDIT_LOG:-${HOME:-/tmp}/.cursor/audit-shell.ndjson}"
mkdir -p "$(dirname "$log")" 2>/dev/null || true
payload="$(cat)"
printf '%s\n' "$payload" >>"$log" 2>/dev/null || true
printf '{"permission":"allow"}\n'
