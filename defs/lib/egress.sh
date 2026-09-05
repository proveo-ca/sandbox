#!/usr/bin/env bash
# SPEC: _spec/components.puml

proveo_egress_defs_dir() {
 if [[ -n "${PROVEO_DEFS_DIR:-}" ]]; then
 printf '%s\n' "$PROVEO_DEFS_DIR"
 return 0
 fi
 cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

proveo_egress_run_bin() {
 if [[ -n "${PROVEO_EGRESS_BIN:-}" && -x "${PROVEO_EGRESS_BIN}" ]]; then
 "$PROVEO_EGRESS_BIN" "$@"; return
 fi
 if command -v proveo-egress >/dev/null 2>&1; then
 proveo-egress "$@"; return
 fi
 local repo_root; repo_root="$(cd "$(proveo_egress_defs_dir)/.." && pwd)"
 if command -v go >/dev/null 2>&1 && [[ -f "$repo_root/go.mod" ]]; then
 ( cd "$repo_root" && go run ./cmd/proveo-egress "$@" ); return
 fi
 echo "❌ proveo-egress not found: set PROVEO_EGRESS_BIN or build it" >&2
 return 127
}

proveo_egress_detect_providers() {
 proveo_egress_run_bin detect
}

proveo_egress_write_provider_allow() {
 local out="${1:?output path}"
 proveo_egress_run_bin provider-allow >"$out"
}

proveo_egress_providers() {
 proveo_egress_run_bin providers
}
