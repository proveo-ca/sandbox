#!/usr/bin/env bash
# SPEC: _spec/cmd/proveo/usage.puml, _spec/components.puml, _spec/defs/claudecode/claudecode-topology.puml, _spec/defs/claudecode/claudecode-egress-topology.puml
set -euo pipefail

if [[ -z "${PROVEO_BIN:-}" ]]; then
 if command -v proveo >/dev/null 2>&1; then
 PROVEO_BIN="$(command -v proveo)"
 else
 PROVEO_BIN="proveo"
 fi
fi

VARIANT="mcp"
ARGS=()
while [[ $# -gt 0 ]]; do
 case "$1" in
 --variant)
 [[ $# -ge 2 ]] || { echo "--variant requires a value" >&2; exit 1; }
 VARIANT="$2"; shift 2 ;;
 --image)
 [[ $# -ge 2 ]] || { echo "--image requires a value" >&2; exit 1; }
 ARGS+=(--image "$2"); shift 2 ;;
 --input-dir)
 [[ $# -ge 2 ]] || { echo "--input-dir requires a value" >&2; exit 1; }
 ARGS+=(--input "$2"); shift 2 ;;
 --output-dir)
 [[ $# -ge 2 ]] || { echo "--output-dir requires a value" >&2; exit 1; }
 ARGS+=(--output "$2"); shift 2 ;;
 --data-dir)
 [[ $# -ge 2 ]] || { echo "--data-dir requires a value" >&2; exit 1; }
 ARGS+=(--data-dir "$2"); shift 2 ;;
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

case "$VARIANT" in
 mcp) TARGET="claudecode" ;;
 solidity) TARGET="claudecode-solidity" ;;
 *)
 echo "Unknown --variant '$VARIANT' (expected: mcp, solidity)" >&2
 exit 1
 ;;
esac

exec "$PROVEO_BIN" run "$TARGET" ${ARGS[@]+"${ARGS[@]}"}
