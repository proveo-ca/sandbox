package credentials

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

// TestNeedsSandboxLogin: every clause is a reason to stay silent, because the
// offer costs the operator a terminal and a browser.
func TestNeedsSandboxLogin(t *testing.T) {
	t.Parallel()
	sub := manifest.Manifest{
		Subscription: true,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
	}
	none := func(string) string { return "" }
	tests := []struct {
		name      string
		man       manifest.Manifest
		sbx, file bool
		stored    []string
		lookup    func(string) string
		want      bool
	}{
		{name: "sbx, subscription, nothing anywhere", man: sub, sbx: true, lookup: none, want: true},
		{name: "docker backend has its own paths", man: sub, sbx: false, lookup: none},
		{name: "a usable file login authenticates the run", man: sub, sbx: true, file: true, lookup: none},
		{
			name: "sbx's store already holds something",
			man:  sub, sbx: true, stored: []string{"anthropic"}, lookup: none,
		},
		{
			name: "a plan token in the env reaches nothing (R4), so the run still needs sbx's sign-in",
			man:  sub, sbx: true, want: true, lookup: func(k string) string {
				if k == "CLAUDE_CODE_OAUTH_TOKEN" {
					return "sk-ant-oat01-x"
				}
				return ""
			},
		},
		{
			name: "a key-authenticated harness has a key path",
			man:  manifest.Manifest{Env: sub.Env}, sbx: true, lookup: none,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NeedsSandboxLogin(tc.man, tc.sbx, tc.file, tc.stored, tc.lookup)
			if got != tc.want {
				t.Errorf("NeedsSandboxLogin = %v, want %v", got, tc.want)
			}
		})
	}
}
