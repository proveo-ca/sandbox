// SPEC: _spec/internal/credentials/credential-decisions.puml
package run

import (
	"os"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
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
		{"a login persisted in the proveo home", CredentialSpec{FileLogin: true}, "login: persisted in the proveo home", false},
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
			facts, items := credentialGaps(rs, &Params{Target: "claudecode"}, Deps{})
			sandbox.PrintBlock(credentialsHeading, facts, items)
			out := sb.String()
			if c.wantUse == "" && strings.TrimSpace(out) != "" {
				t.Errorf("credentialGaps(%s) printed %q; want nothing — sbx reports what it presents", c.name, out)
			}
			if !strings.Contains(out, c.wantUse) {
				t.Errorf("credentialGaps(%s) never says %q:\n%s", c.name, c.wantUse, out)
			}
			if got := strings.Contains(out, "ANTHROPIC_API_KEY not set"); got != c.wantNotSet {
				t.Errorf("credentialGaps(%s) warns \"not set\" = %v, want %v — one credential, one verdict:\n%s", c.name, got, c.wantNotSet, out)
			}
		})
	}
}

func TestTheCredentialPromptPrintsUnderCredentials(t *testing.T) {
	const key = "PROVEO_TEST_PROMPTED_KEY"
	t.Setenv(key, "")
	var screen, log strings.Builder
	prev, prevAsk := ui.Default, askOnTerminal
	ui.Default = ui.New(&screen)
	restore := ui.TeeTo(&log)
	askOnTerminal = func() bool { return true }
	t.Cleanup(func() { restore(); ui.Default, askOnTerminal = prev, prevAsk })

	ui.Section(ui.SectionRun)
	ui.Notef("target: codex")
	rs := &Spec{Man: manifest.Manifest{Name: "codex", Env: []manifest.EnvVar{{Name: key, Secret: true}}},
		Creds: CredentialSpec{Lookup: os.Getenv}}
	prompt := func(_ string, missing []manifest.EnvVar) map[string]string {
		ui.Notef("%s (hidden):", missing[0].Name)
		return map[string]string{key: "sk-test"}
	}
	facts, items := credentialGaps(rs, &Params{Target: "codex"}, Deps{PromptEnv: prompt})
	sandbox.PrintBlock(credentialsHeading, facts, items)
	got := screen.String()
	heading, question := strings.Index(got, "------ credentials ------"), strings.Index(got, key+" (hidden):")
	if heading < 0 || question < heading {
		t.Errorf("the prompt prints under the credentials heading, not under run:\n%s", got)
	}
	if !strings.Contains(log.String(), key+" (hidden):") || strings.Contains(log.String(), "sk-test") {
		t.Errorf("the run log holds the question and never the value:\n%s", log.String())
	}
	if len(items) != 0 {
		t.Errorf("a prompted value leaves nothing missing: %+v", items)
	}
}
