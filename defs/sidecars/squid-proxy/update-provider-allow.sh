#!/usr/bin/env bash
# SPEC: _spec/internal/provider/provider-registry.puml
set -euo pipefail

# Keep the provider allowlist fresh by reconciling it against a public, maintained
# model registry — the FireHOL-updater pattern, applied to LLM endpoints.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_URL="${PROVEO_PROVIDER_SOURCE_URL:-https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json}"
CACHED=""
OUTPUT_DIR="${PROVEO_PROVIDER_OUTPUT_DIR:-$(pwd)}"

usage() {
  cat <<'EOF'
Usage:
  update-provider-allow.sh [--cached <file>] [--output-dir <dir>]

Reconciles the egress provider allowlist against the LiteLLM model registry.

Options:
  --cached <file>     Use a local JSON snapshot instead of fetching (offline).
  --output-dir <dir>  Where to write provider-coverage.txt (default: cwd).

Environment:
  PROVEO_PROVIDER_SOURCE_URL   Override the registry URL.
  PROVEO_EGRESS_BIN            Path to the proveo-egress binary (else PATH / go run).

Notes:
  - Non-destructive: reports drift; the provider map is curated in Go
    (internal/provider), read here via `proveo-egress providers`.
  - Endpoint *names* are mechanical (pulled here); *trust* is curated by hand.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cached)     CACHED="${2:?--cached needs a file}"; shift 2 ;;
    --output-dir) OUTPUT_DIR="${2:?--output-dir needs a dir}"; shift 2 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

mkdir -p "$OUTPUT_DIR"
report="$OUTPUT_DIR/provider-coverage.txt"

# Normalize LiteLLM provider names to the short names egress.sh uses, so the diff
# doesn't flag pure naming differences (vertex_ai→vertex, gemini→google, etc.).
normalize_provider() {
  case "$1" in
    vertex_ai|vertex_ai_beta) echo vertex ;;
    together_ai)              echo together ;;
    fireworks_ai)             echo fireworks ;;
    gemini)                   echo google ;;
    text-completion-openai|openai_like) echo openai ;;
    *)                        echo "$1" ;;
  esac
}

# 1. Obtain the registry JSON (cached file or fetch; degrade gracefully).
json="$(mktemp)"; trap 'rm -f "$json"' EXIT
if [[ -n "$CACHED" ]]; then
  [[ -f "$CACHED" ]] || { echo "❌ cached file not found: $CACHED" >&2; exit 1; }
  cp "$CACHED" "$json"
  echo "ℹ️  using cached registry: $CACHED"
elif curl -fsSL --max-time 30 "$SOURCE_URL" -o "$json" 2>/dev/null; then
  echo "ℹ️  fetched registry: $SOURCE_URL"
else
  echo "⚠️  could not fetch $SOURCE_URL and no --cached given; nothing to reconcile." >&2
  echo "    (Provider map in egress.sh remains the offline fallback.)" >&2
  exit 0
fi

# 2. Extract upstream provider names (jq if present, else grep fallback).
upstream="$(mktemp)"; trap 'rm -f "$json" "$upstream"' EXIT
if command -v jq >/dev/null 2>&1; then
  jq -r '[.[]? | .litellm_provider? // empty] | unique[]' "$json" 2>/dev/null \
    | while IFS= read -r p; do normalize_provider "$p"; done | sort -u >"$upstream"
else
  grep -o '"litellm_provider"[[:space:]]*:[[:space:]]*"[^"]*"' "$json" \
    | sed 's/.*"\([^"]*\)"$/\1/' \
    | while IFS= read -r p; do normalize_provider "$p"; done | sort -u >"$upstream"
fi

# 3. Extract the providers the Go registry maps (single source: internal/provider).
mapped="$(mktemp)"; trap 'rm -f "$json" "$upstream" "$mapped"' EXIT
proveo_egress_providers() {
  if [[ -n "${PROVEO_EGRESS_BIN:-}" && -x "${PROVEO_EGRESS_BIN}" ]]; then "$PROVEO_EGRESS_BIN" providers; return; fi
  if command -v proveo-egress >/dev/null 2>&1; then proveo-egress providers; return; fi
  ( cd "$SCRIPT_DIR/../../.." && go run ./cmd/proveo-egress providers )
}
proveo_egress_providers | sort -u >"$mapped"

# 4. Report drift.
missing="$(comm -23 "$upstream" "$mapped" || true)"   # upstream has, we don't map
stale="$(comm -13 "$upstream" "$mapped" || true)"     # we map, upstream doesn't list

{
  echo "# Provider allowlist coverage vs $SOURCE_URL"
  echo "# Generated at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# Mapped in the Go provider registry: $(tr '\n' ' ' <"$mapped")"
  echo
  echo "## Upstream providers NOT yet mapped (review for trust, then add to egress.sh):"
  # shellcheck disable=SC2086  # intentional split: one printf arg per provider
  if [[ -n "$missing" ]]; then printf '  - %s\n' $missing; else echo "  (none)"; fi
  echo
  echo "## Mapped providers NOT in upstream registry (possibly stale/renamed):"
  # shellcheck disable=SC2086  # intentional split: one printf arg per provider
  if [[ -n "$stale" ]]; then printf '  - %s\n' $stale; else echo "  (none)"; fi
  echo
  echo "## Best-effort api_base hostnames found in registry (verify before trusting):"
  grep -o '"api_base"[[:space:]]*:[[:space:]]*"[^"]*"' "$json" 2>/dev/null \
    | sed 's/.*"\(https\{0,1\}:\/\/[^"]*\)".*/\1/' \
    | sed -E 's#^https?://([^/]+).*#\1#' | sort -u | sed 's/^/  /' || true
} >"$report"

cat "$report"
echo
echo "📝 Wrote $report"
[[ -n "$missing" ]] && echo "⚠️  ${missing//$'\n'/, } not mapped — review trust tier before adding." >&2
exit 0
