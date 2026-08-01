#!/usr/bin/env bash
# Thin shim → proveo run. Fat docker/egress/dind logic lives in Go.
# SPEC: _spec/cmd/proveo/usage.puml, _spec/components.puml
set -euo pipefail

# Prefer PROVEO_BIN, else proveo on PATH.
if [[ -z "${PROVEO_BIN:-}" ]]; then
 if command -v proveo >/dev/null 2>&1; then
 PROVEO_BIN="$(command -v proveo)"
 else
 PROVEO_BIN="proveo"
 fi
fi

TARGET="opencode"
ARGS=
while [[ $# -gt 0 ]]; do
 case "$1" in
 --image)
 [[ $# -ge 2 ]] || { echo "--image requires a value" >&2; exit 1; }
 ARGS+=(--image "$2"); shift 2 ;;
 --input-dir)
 [[ $# -ge 2 ]] || { echo "--input-dir requires a value" >&2; exit 1; }
 ARGS+=(--input "$2"); shift 2 ;;
 --repo-root)
 # proveo run resolves git root itself; accept and ignore for flag parity.
 [[ $# -ge 2 ]] || { echo "--repo-root requires a value" >&2; exit 1; }
 shift 2 ;;
 --egress-mode|--local-model)
 [[ $# -ge 2 ]] || { echo "$1 requires a value" >&2; exit 1; }
 ARGS+=("$1" "$2"); shift 2 ;;
 --shell|--print)
 ARGS+=("$1"); shift ;;
 -h|--help)
 exec "$PROVEO_BIN" run --help ;;
 --)
 shift; ARGS+=(-- "$@"); break ;;
 *)
 ARGS+=("$1"); shift ;;
 esac
done

exec "$PROVEO_BIN" run "$TARGET" ${ARGS[@]+"${ARGS[@]}"}
