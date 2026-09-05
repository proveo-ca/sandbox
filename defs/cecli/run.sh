#!/usr/bin/env bash
# SPEC: _spec/cmd/proveo/usage.puml, _spec/components.puml
set -euo pipefail

if [[ -z "${PROVEO_BIN:-}" ]]; then
 if command -v proveo >/dev/null 2>&1; then
 PROVEO_BIN="$(command -v proveo)"
 else
 PROVEO_BIN="proveo"
 fi
fi

TARGET="cecli"
ARGS=
while [[ $# -gt 0 ]]; do
 case "$1" in
 --python|--node) shift ;; # single cecli image now — variant flags accepted for back-compat
 --image)
 [[ $# -ge 2 ]] || { echo "--image requires a value" >&2; exit 1; }
 ARGS+=(--image "$2"); shift 2 ;;
 --input-dir)
 [[ $# -ge 2 ]] || { echo "--input-dir requires a value" >&2; exit 1; }
 ARGS+=(--input "$2"); shift 2 ;;
 --output-dir)
 [[ $# -ge 2 ]] || { echo "--output-dir requires a value" >&2; exit 1; }
 ARGS+=(--output "$2"); shift 2 ;;
 --repo-root)
 [[ $# -ge 2 ]] || { echo "--repo-root requires a value" >&2; exit 1; }
 shift 2 ;;
 --egress-mode|--local-model)
 [[ $# -ge 2 ]] || { echo "$1 requires a value" >&2; exit 1; }
 ARGS+=("$1" "$2"); shift 2 ;;
 --shell|--print)
 ARGS+=("$1"); shift ;;
 --read-only)
 shift ;;
 -h|--help)
 exec "$PROVEO_BIN" run --help ;;
 --)
 shift; ARGS+=(-- "$@"); break ;;
 *)
 ARGS+=("$1"); shift ;;
 esac
done

exec "$PROVEO_BIN" run "$TARGET" ${ARGS[@]+"${ARGS[@]}"}
