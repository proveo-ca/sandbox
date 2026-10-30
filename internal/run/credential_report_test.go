// SPEC: _spec/internal/credentials/credential-decisions.puml
package run

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestASatisfiedSubscriptionCredentialIsNotAlsoReportedMissing(t *testing.T) {
	man := manifest.Manifest{Name: "claudecode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "ANTHROPIC_API_KEY", Description: "a Console key"}}}
	for _, c := range []struct {
		name       string
		creds      CredentialSpec
		wantUse    string
		wantNotSet bool
	}{
		{"a login persisted in the proveo home", CredentialSpec{FileLogin: true}, "using the login persisted in the proveo home", false},
		{"a credential sbx's store holds", CredentialSpec{StoreHeld: []string{"anthropic"}}, "", false},
		{"nothing", CredentialSpec{}, "starting without a credential", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var sb strings.Builder
			prev := ui.Default
			ui.Default = ui.New(&sb)
			defer func() { ui.Default = prev }()

			c.creds.Lookup = func(string) string { return "" }
			rs := &Spec{Man: man, Creds: c.creds}
			reportCredentialGaps(rs, &Params{Target: "claudecode"}, Deps{})
			out := sb.String()
			if c.wantUse == "" && strings.TrimSpace(out) != "" {
				t.Errorf("reportCredentialGaps(%s) printed %q; want nothing — sbx reports what it presents", c.name, out)
			}
			if !strings.Contains(out, c.wantUse) {
				t.Errorf("reportCredentialGaps(%s) never says %q:\n%s", c.name, c.wantUse, out)
			}
			if got := strings.Contains(out, "ANTHROPIC_API_KEY not set"); got != c.wantNotSet {
				t.Errorf("reportCredentialGaps(%s) warns \"not set\" = %v, want %v — one credential, one verdict:\n%s", c.name, got, c.wantNotSet, out)
			}
		})
	}
}
