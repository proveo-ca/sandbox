#!/usr/bin/env bash
# SPEC: _spec/internal/egresspolicy/egress-policy-layers.puml
set -euo pipefail

FIREHOL_IPSET="${FIREHOL_IPSET:-firehol_level1}"
FIREHOL_REF="${FIREHOL_REF:-master}"
FIREHOL_SOURCE_URL="${FIREHOL_SOURCE_URL:-https://raw.githubusercontent.com/firehol/blocklist-ipsets/${FIREHOL_REF}/${FIREHOL_IPSET}.netset}"
FIREHOL_SHA256="${FIREHOL_SHA256:-}"
OUTPUT_DIR="${1:-${FIREHOL_OUTPUT_DIR:-$(pwd)/config}}"
OUTPUT_FILE="$OUTPUT_DIR/firehol-ipset.conf"

usage() {
  cat <<'EOF'
Usage:
  update-firehol-ipsets.sh [output-dir]

Fetches a FireHOL blocklist-ipsets netset and converts it into Squid ACLs.

Environment:
  FIREHOL_IPSET        FireHOL ipset name (default: firehol_level1)
  FIREHOL_SOURCE_URL   Override source URL
  FIREHOL_OUTPUT_DIR   Output directory when no argument is provided

Notes:
  - This does not install FireHOL or ipset kernel rules.
  - This is a Squid adaptation of a FireHOL IP list.
  - FireHOL docs warn that IP blocklists can have false positives; keep this
    optional and pair it with allowlists where needed.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

mkdir -p "$OUTPUT_DIR"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

if [[ "$FIREHOL_REF" == "master" && -z "$FIREHOL_SHA256" ]]; then
  echo "⚠️  fetching FireHOL from a mutable 'master' ref with no checksum — set FIREHOL_REF=<commit> and FIREHOL_SHA256=<sum> for a reproducible, verified fetch." >&2
fi

curl -fsSL "$FIREHOL_SOURCE_URL" -o "$tmp"

if [[ -n "$FIREHOL_SHA256" ]]; then
  if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "$tmp" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then actual="$(shasum -a 256 "$tmp" | awk '{print $1}')"
  else echo "❌ need sha256sum or shasum to verify FIREHOL_SHA256" >&2; exit 1
  fi
  if [[ "$actual" != "$FIREHOL_SHA256" ]]; then
    echo "❌ FireHOL netset checksum mismatch (expected $FIREHOL_SHA256, got $actual); refusing." >&2
    exit 1
  fi
fi

{
  printf '# Generated from %s\n' "$FIREHOL_SOURCE_URL"
  printf '# FireHOL ipset: %s\n' "$FIREHOL_IPSET"
  printf '# Generated at: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '# Review false-positive risk before enabling broad feeds.\n'
  while IFS= read -r line; do
    line="${line%%#*}"
    line="${line//[$'\t\r\n ']/}"
    [[ -n "$line" ]] || continue
    printf 'acl firehol_ipset dst %s\n' "$line"
  done <"$tmp"
  printf 'http_access deny firehol_ipset\n'
} >"$OUTPUT_FILE"

printf 'Wrote %s\n' "$OUTPUT_FILE"
