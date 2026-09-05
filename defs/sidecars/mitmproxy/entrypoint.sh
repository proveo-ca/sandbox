#!/usr/bin/env bash
# SPEC: _spec/defs/claudecode/claudecode-egress-topology.puml
set -euo pipefail

: "${PROVEO_MITM_PORT:=8888}"
: "${PROVEO_MITM_CONFDIR:=/mitmproxy-confdir}"
: "${PROVEO_MITM_FLOWS:=/flows}"
: "${PROVEO_MITM_UPSTREAM:=}"

if [[ "${PROVEO_SMOKE_TEST:-0}" == "1" ]]; then
  echo "✅ PROVEO_SMOKE_READY ${PROVEO_SMOKE_TARGET:-mitmproxy}"
  exec sleep infinity
fi

mkdir -p "$PROVEO_MITM_CONFDIR" "$PROVEO_MITM_FLOWS"

args=(
  --listen-host 0.0.0.0
  --listen-port "$PROVEO_MITM_PORT"
  --set "confdir=${PROVEO_MITM_CONFDIR}"
  --set "stream_large_bodies=1m"
  -s /addons/ndjson_dump.py
)

if [[ -n "$PROVEO_MITM_UPSTREAM" ]]; then
  args=(--mode "upstream:${PROVEO_MITM_UPSTREAM}" "${args[@]}")
  echo "🚀 mitmdump → upstream ${PROVEO_MITM_UPSTREAM} (HTTPS interception ON) on :${PROVEO_MITM_PORT}"
else
  echo "🚀 mitmdump direct proxy (HTTPS interception ON) on :${PROVEO_MITM_PORT}"
fi

exec mitmdump "${args[@]}" "$@"
