#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml, _spec/tests/00-testing-overview.puml
# Unit/contract coverage via -test.gocoverdir; merge + report via go tool covdata.
# Stage 0b (containerized proveo-egress GOCOVERDIR) is documented but not required for v1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

COV="${PROVEO_COV_DIR:-$ROOT/cov}"
UNIT_DIR="$COV/unit"
MERGED_DIR="$COV/merged"
PROFILE="$COV/coverage.out"

mode="${1:-unit}"

need_go() {
  if ! command -v go >/dev/null 2>&1; then
    echo "go toolchain required" >&2
    exit 127
  fi
}

run_unit() {
  need_go
  rm -rf "$UNIT_DIR"
  mkdir -p "$UNIT_DIR"
  # Default tags only — integration/e2e suites are excluded by //go:build.
  # Prefer -race when the toolchain supports it (needs cgo); else coverage-only.
  if go test -race -cover -covermode=atomic ./internal/runner -c -o /dev/null >/dev/null 2>&1; then
    go test -race -cover -covermode=atomic ./... -args -test.gocoverdir="$UNIT_DIR"
  else
    echo "⚠️  -race unavailable; running without race detector" >&2
    go test -cover -covermode=atomic ./... -args -test.gocoverdir="$UNIT_DIR"
  fi
  echo "unit coverage data → $UNIT_DIR"
}

run_merge() {
  need_go
  if [[ ! -d "$UNIT_DIR" ]] || [[ -z "$(ls -A "$UNIT_DIR" 2>/dev/null || true)" ]]; then
    echo "no unit coverage in $UNIT_DIR — run: $0 unit" >&2
    exit 1
  fi
  rm -rf "$MERGED_DIR"
  mkdir -p "$MERGED_DIR"
  # v1: unit lane only (includes in-process egressproxy/broker). Extra dirs merge when present.
  inputs="$UNIT_DIR"
  if [[ -d "$COV/integration" ]] && [[ -n "$(ls -A "$COV/integration" 2>/dev/null || true)" ]]; then
    inputs="$UNIT_DIR,$COV/integration"
  fi
  go tool covdata merge -i="$inputs" -o="$MERGED_DIR"
  go tool covdata percent -i="$MERGED_DIR"
  go tool covdata textfmt -i="$MERGED_DIR" -o="$PROFILE"
  echo "merged profile → $PROFILE"
  echo "html: go tool cover -html=$PROFILE"
}

run_integration() {
  need_go
  if [[ "${PROVEO_EGRESS_INTEGRATION:-}" != "1" ]]; then
    echo "set PROVEO_EGRESS_INTEGRATION=1 to run Layer 3" >&2
    exit 1
  fi
  go test -tags=integration -race ./internal/egress/ -count=1 -timeout 120s "$@"
}

run_e2e() {
  need_go
  # No env gate: -tags=e2e IS the opt-in. Each test skips itself when its own
  # prerequisites (tmux, docker, harness image, credential) are absent.
  #
  # The binary timeout has to clear the SUM of the suite's own per-test budgets,
  # not one of them: every subtest that drives a real agent polls until
  # PROVEO_TEST_TIMEOUT (default 8m each), and hello-world alone runs three
  # harnesses. Under a shorter cap the go runtime panics mid-suite and every
  # result is lost — including the passes and the diagnostics of the failures,
  # which is strictly worse than a slow lane. Lower it per invocation with
  # PROVEO_TEST_GO_TIMEOUT when running a single -run selection.
  go test -tags=e2e ./e2e/ -count=1 -timeout "${PROVEO_TEST_GO_TIMEOUT:-45m}" "$@"
}

case "$mode" in
  unit) run_unit ;;
  merge|coverage) run_merge ;;
  integration) shift || true; run_integration "$@" ;;
  e2e) shift || true; run_e2e "$@" ;;
  all)
    run_unit
    run_merge
    ;;
  *)
    echo "usage: $0 {unit|merge|coverage|integration|e2e|all}" >&2
    exit 2
    ;;
esac
