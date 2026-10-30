// SPEC: _spec/internal/secretref/secret-references.puml
package secretref

import (
	"strings"
	"testing"
)

func TestParseSchemes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw        string
		wantScheme string
		wantArg    string
		wantRef    bool
	}{
		{raw: "keychain:", wantScheme: SchemeKeychain, wantRef: true},
		{raw: "keychain:Claude Code-credentials", wantScheme: SchemeKeychain, wantArg: "Claude Code-credentials", wantRef: true},
		{raw: "cmd:op read op://Private/x/key", wantScheme: SchemeCmd, wantArg: "op read op://Private/x/key", wantRef: true},
		{raw: "pass:llm/anthropic", wantScheme: SchemePass, wantArg: "llm/anthropic", wantRef: true},
		{raw: "env:OTHER", wantScheme: SchemeEnv, wantArg: "OTHER", wantRef: true},
		{raw: "  keychain:svc  ", wantScheme: SchemeKeychain, wantArg: "svc", wantRef: true},
		// Literal secrets: a real key can carry a colon.
		{raw: "sk-ant-oat01-abc"},
		{raw: "sk-ant-api03-a:b:c"},
		{raw: "vault:secret/x"}, // an unknown scheme is a literal
		{raw: "https://example.com/token"},
		{raw: ""},
	}
	for _, tc := range tests {
		ref, ok := Parse(tc.raw)
		if ok != tc.wantRef {
			t.Errorf("Parse(%q) ok = %v, want %v", tc.raw, ok, tc.wantRef)
			continue
		}
		if ok && (ref.Scheme != tc.wantScheme || ref.Arg != tc.wantArg) {
			t.Errorf("Parse(%q) = %+v, want {%s %s}", tc.raw, ref, tc.wantScheme, tc.wantArg)
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R11)
func TestEveryReferenceBecomesAnSbxSourceProveoNeverResolves(t *testing.T) {
	t.Parallel()
	getenv := func(k string) string { return map[string]string{"OTHER": "from-other"}[k] }
	for _, tc := range []struct {
		raw, flags, stdin string
	}{
		{"keychain:anthropic-api", "--command /usr/bin/security find-generic-password -w -s 'anthropic-api'", ""},
		{"keychain:", "--command /usr/bin/security find-generic-password -w -s 'ANTHROPIC_API_KEY'", ""},
		{"pass:llm/anthropic", "--command pass show 'llm/anthropic' | head -n 1", ""},
		{"cmd:op read op://Private/x/key", "--command op read op://Private/x/key", ""},
		{"op://Private/Anthropic/api-key", "--ref op://Private/Anthropic/api-key", ""},
		{"arn:aws:secretsmanager:us-west-2:1:secret:k", "--ref arn:aws:secretsmanager:us-west-2:1:secret:k", ""},
		{"env:OTHER", "", "from-other"},
		{"sk-ant-api03-a:b:c", "", "sk-ant-api03-a:b:c"},
	} {
		flags, stdin := SbxSource("ANTHROPIC_API_KEY", tc.raw, getenv)
		if got := strings.Join(flags, " "); got != tc.flags || stdin != tc.stdin {
			t.Errorf("SbxSource(%q) = %q, stdin %q; want %q, stdin %q", tc.raw, got, stdin, tc.flags, tc.stdin)
		}
	}
	if !IsReference("op://x/y/z") || !IsReference("keychain:k") || IsReference("sk-live") {
		t.Error("IsReference must cover the four schemes plus sbx's own --ref sources, and nothing else")
	}
}

func TestAKeychainItemNameCannotBreakOutOfTheCommand(t *testing.T) {
	t.Parallel()
	flags, _ := SbxSource("K", "keychain:a'; rm -rf ~; echo '", func(string) string { return "" })
	want := `/usr/bin/security find-generic-password -w -s 'a'\''; rm -rf ~; echo '\'''`
	if flags[1] != want {
		t.Errorf("command = %q, want the item name quoted as one word %q", flags[1], want)
	}
}
