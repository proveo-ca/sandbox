#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# Egress contract tests — orchestration covered by Go golden/integration tests.
# This suite keeps Squid policy static checks + provider detect/allow via proveo-egress.
set -euo pipefail

if [[ -z "${PROJECT_ROOT:-}" ]]; then
 SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
 PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
 # shellcheck source=helpers.sh
 source "$SCRIPT_DIR/helpers.sh"
fi

egress_pass() {
 local desc="$1"
 TESTS_RUN=$((TESTS_RUN + 1))
 TESTS_PASSED=$((TESTS_PASSED + 1))
 printf "${GREEN}PASS${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
}

egress_fail() {
 local desc="$1" detail="${2:-}"
 TESTS_RUN=$((TESTS_RUN + 1))
 TESTS_FAILED=$((TESTS_FAILED + 1))
 FAILURES+=("$desc")
 printf "${RED}FAIL${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
 [[ -n "$detail" ]] && printf " %s\n" "$detail"
}

assert_file_contains() {
 local desc="$1" file="$2" expected="$3"
 if grep -qF -- "$expected" "$file"; then
 egress_pass "$desc"
 else
 egress_fail "$desc" "Expected '$expected' in $file"
 fi
}

assert_file_not_contains() {
 local desc="$1" file="$2" unexpected="$3"
 if grep -qF -- "$unexpected" "$file"; then
 egress_fail "$desc" "Did not expect '$unexpected' in $file"
 else
 egress_pass "$desc"
 fi
}

assert_provider_allowlist_contracts() {
 local tmp f lib; tmp="$(mktemp -d)"; f="$tmp/provider-allow.conf"
 lib="$PROJECT_ROOT/../lib/egress.sh"
 # shellcheck source=/dev/null
 source "$lib"

 local d
 d="$(unset PROVEO_EGRESS_PROVIDER; ANTHROPIC_API_KEY=sk-x bash -c "source '$lib'; proveo_egress_detect_providers")"
 [[ "$d" == *anthropic* ]] && egress_pass "[provider] ANTHROPIC_API_KEY auto-detects anthropic" || egress_fail "[provider] ANTHROPIC_API_KEY auto-detects anthropic" "got: $d"
 d="$(unset PROVEO_EGRESS_PROVIDER ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN; AWS_ACCESS_KEY_ID=AKIA bash -c "source '$lib'; proveo_egress_detect_providers")"
 [[ "$d" == *bedrock* ]] && egress_pass "[provider] AWS key auto-detects bedrock" || egress_fail "[provider] AWS key auto-detects bedrock" "got: $d"
 d="$(unset PROVEO_EGRESS_PROVIDER ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN; GMI_API_KEY=gmi bash -c "source '$lib'; proveo_egress_detect_providers")"
 [[ "$d" == *gmi* ]] && egress_pass "[provider] GMI_API_KEY auto-detects gmi" || egress_fail "[provider] GMI_API_KEY auto-detects gmi" "got: $d"

 ( unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN; PROVEO_EGRESS_PROVIDER=together bash -c "source '$lib'; proveo_egress_write_provider_allow '$f'" ) >/dev/null 2>&1
 assert_file_contains "[provider] together pins inference writes to its endpoint" "$f" "acl provider_allow dstdomain .together.xyz"
 assert_file_contains "[provider] write-pin allows unsafe methods to provider only" "$f" "http_access allow unsafe_methods provider_allow"
 assert_file_not_contains "[provider] reads stay open — no deny-all (scraping preserved)" "$f" "http_access deny all"

 ( PROVEO_EGRESS_PROVIDER=gmi bash -c "source '$lib'; proveo_egress_write_provider_allow '$f'" ) >/dev/null 2>&1
 assert_file_contains "[provider] gmi pins to api.gmi-serving.com" "$f" ".gmi-serving.com"

 ( PROVEO_EGRESS_PROVIDER=bedrock bash -c "source '$lib'; proveo_egress_write_provider_allow '$f'" ) >/dev/null 2>&1
 assert_file_contains "[provider] bedrock scoped to bedrock-runtime, not all of AWS" "$f" "bedrock-runtime"
 assert_file_not_contains "[provider] bedrock does not allow all of .amazonaws.com" "$f" "dstdomain .amazonaws.com"

 ( unset PROVEO_EGRESS_PROVIDER ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN; OPENAI_API_KEY=sk bash -c "source '$lib'; proveo_egress_write_provider_allow '$f'" ) >/dev/null 2>&1
 assert_file_contains "[provider] auto-detected openai key pins openai endpoint" "$f" ".openai.com"

 rm -rf "$tmp"
}

echo "Testing egress mode contracts..."
echo "Level 1: static policy contracts"
SQUID_CONF="$PROJECT_ROOT/../sidecars/squid-proxy/squid.conf"
assert_file_contains "[policy] Squid documents HTTP/HTTPS-only protocol allowlist" "$SQUID_CONF" "Protocol allowlist: HTTP and HTTPS only"
assert_file_contains "[policy] Squid allows HTTP port 80" "$SQUID_CONF" "acl Safe_ports port 80"
assert_file_contains "[policy] Squid allows HTTPS port 443" "$SQUID_CONF" "acl Safe_ports port 443"
assert_file_contains "[policy] Squid blocks non-web/raw protocols by denying non-safe ports" "$SQUID_CONF" "http_access deny !Safe_ports"
assert_file_contains "[policy] Squid allows only read-oriented visible HTTP methods by default" "$SQUID_CONF" "acl read_methods method GET HEAD OPTIONS"
assert_file_not_contains "[policy] FTP is not in the allowed protocol set" "$SQUID_CONF" "acl Safe_ports port 21"
assert_file_contains "[policy] Squid includes FireHOL-informed reserved destination defaults" "$SQUID_CONF" "include /etc/squid/firehol-blocked-nets.conf"
assert_file_contains "[policy] Squid supports optional generated FireHOL ipset ACLs" "$SQUID_CONF" "include /etc/squid/firehol-ipset.conf"
assert_file_contains "[policy] reserved defaults block cloud metadata SSRF range" "$PROJECT_ROOT/../sidecars/squid-proxy/firehol-blocked-nets.conf" "169.254.0.0/16"
assert_file_contains "[policy] reserved defaults block private RFC1918 ranges" "$PROJECT_ROOT/../sidecars/squid-proxy/firehol-blocked-nets.conf" "10.0.0.0/8"
assert_file_contains "[policy] optional FireHOL updater defaults to firehol_level1" "$PROJECT_ROOT/../sidecars/squid-proxy/update-firehol-ipsets.sh" "firehol_level1"
assert_file_contains "[policy] optional FireHOL updater generates Squid ACLs" "$PROJECT_ROOT/../sidecars/squid-proxy/update-firehol-ipsets.sh" "acl firehol_ipset dst"
assert_file_contains "[policy] any HTTPS documentation/search destination is allowed by protocol" "$SQUID_CONF" "http_access allow CONNECT SSL_ports"
assert_file_contains "[policy] any visible HTTP documentation/search read is allowed by method" "$SQUID_CONF" "http_access allow read_methods"
assert_file_contains "[policy] docs/search access is intentionally generic, not host-specific" "$SQUID_CONF" "any documentation site, search engine"
assert_file_not_contains "[policy] Pinecone docs are not hardcoded as a special case" "$SQUID_CONF" "docs.pinecone.io"
assert_file_not_contains "[policy] Google search is not hardcoded as a special case" "$SQUID_CONF" ".google.com"

echo "Level 2: provider allowlist + key auto-detection (proveo-egress)"
assert_provider_allowlist_contracts

echo "Level 3: topology/integration — covered by Go tests"
echo " internal/egress plan golden + PROVEO_EGRESS_INTEGRATION=1 go test ./internal/egress/"
egress_pass "[go] orchestration contracts live in internal/egress (not bash prepare)"

echo
printf 'egress contracts — failed: %d\n' "${TESTS_FAILED:-0}"
if (( ${TESTS_FAILED:-0} > 0 )); then
 printf 'Failed egress contracts:\n'
 for _f in ${FAILURES[@]+"${FAILURES[@]}"}; do printf ' - %s\n' "$_f"; done
 exit 1
fi
