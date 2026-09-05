#!/usr/bin/env bash
# SPEC: _spec/tests/20-contract.puml
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo 'Running Go Layer 2 contract tests...'
if ! command -v go >/dev/null 2>&1; then
  echo "go toolchain required for contract tests" >&2
  exit 127
fi
(
  cd "$ROOT"
  go test ./internal/contract/ ./internal/verify/ ./internal/runner/ ./internal/provider/ \
    ./internal/workspace/ ./internal/egress/ ./internal/entrypoint/ \
    ./cmd/proveo/ ./cmd/proveo-egress/ ./cmd/proveo-entrypoint/ -count=1
)
