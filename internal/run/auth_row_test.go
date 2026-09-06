// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/internal/credentials/credential-decisions.puml
package run

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func opencodeMan() manifest.Manifest {
	return manifest.Manifest{
		Name: "opencode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}},
	}
}

func cursorMan() manifest.Manifest {
	return manifest.Manifest{
		Name: "cursor", Subscription: true, Provider: "cursor",
		Env:          []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"cursor"}},
	}
}

// Every harness with a plan draws the SAME three options in the same order:
// riskier (metered per token) on the left, safest (nothing billed, no credential
// to leak) on the right. The row is an axis and the prompt labels it as one.
func TestAuthRowDrawsTheWholeAxisRiskierFirst(t *testing.T) {
	t.Parallel()
	for _, man := range []manifest.Manifest{opencodeMan(), cursorMan()} {
		r, ok := authRow(man, env(map[string]string{
			"OPENCODE_API_KEY": "zen", "CURSOR_API_KEY": "cur", "ANTHROPIC_API_KEY": "sk",
		}), man.Name, "", "", "")
		if !ok {
			t.Fatalf("%s: no auth row", man.Name)
		}
		want := []string{credentials.AuthUsage, credentials.AuthSubscription, credentials.AuthLocal}
		if !slices.Equal(r.Options, want) {
			t.Errorf("%s options = %v, want %v", man.Name, r.Options, want)
		}
	}
}

// The local model is the safe end of the axis and is not wired up: drawn so the
// operator knows it exists, gated so it cannot be picked.
func TestLocalModelIsDrawnAndGated(t *testing.T) {
	t.Parallel()
	r, ok := authRow(opencodeMan(), env(map[string]string{
		"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk",
	}), "opencode", "", "", "")
	if !ok {
		t.Fatal("no auth row")
	}
	i := slices.Index(r.Options, credentials.AuthLocal)
	if i != len(r.Options)-1 {
		t.Errorf("local model at %d of %v, want last — it is the safest end", i, r.Options)
	}
	if !r.Off[i] {
		t.Error("the local model is selectable, but nothing routes this row to it yet")
	}
	if r.Selected == i {
		t.Error("the row opened on the option that cannot be chosen")
	}
	if !strings.Contains(r.Help[credentials.AuthLocal], "nothing billed") {
		t.Errorf("local help = %q, want it to say what makes it the safe end",
			r.Help[credentials.AuthLocal])
	}
}

// opencode's two live sides, both selectable, each hint naming the variables
// behind it — the row opencode never got.
func TestAuthRowOffersBothLiveSidesForOpenCode(t *testing.T) {
	t.Parallel()
	r, ok := authRow(opencodeMan(), env(map[string]string{
		"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk",
	}), "opencode", "", "", "")
	if !ok {
		t.Fatal("no auth row for a host holding both the plan key and a provider key")
	}
	for _, opt := range []string{credentials.AuthUsage, credentials.AuthSubscription} {
		if r.Off[slices.Index(r.Options, opt)] {
			t.Errorf("%q gated on a harness that can be billed either way", opt)
		}
	}
	if !strings.Contains(r.Help[credentials.AuthUsage], "ANTHROPIC_API_KEY") {
		t.Errorf("usage help = %q, want the key it would spend", r.Help[credentials.AuthUsage])
	}
	if !strings.Contains(r.Help[credentials.AuthSubscription], "OPENCODE_API_KEY") {
		t.Errorf("plan help = %q, want the credential it would spend",
			r.Help[credentials.AuthSubscription])
	}
}

// cursor gets the same row with usage gated and the reason attached. Dropping it
// is what left "cursor ignores my ANTHROPIC_API_KEY" as something the operator
// could only discover by running.
func TestAuthRowGatesUsageForAVendorPinnedHarness(t *testing.T) {
	t.Parallel()
	r, ok := authRow(cursorMan(), env(map[string]string{
		"CURSOR_API_KEY": "cur", "ANTHROPIC_API_KEY": "sk",
	}), "cursor", "", "", "")
	if !ok {
		t.Fatal("no auth row: the operator is never told why their provider key is ignored")
	}
	i := slices.Index(r.Options, credentials.AuthUsage)
	if !r.Off[i] {
		t.Error("usage credits selectable on a CLI with no bring-your-own-key path")
	}
	if r.Selected == i {
		t.Error("the row opened on the option that cannot be chosen")
	}
	if !strings.Contains(r.Reason+r.Help[credentials.AuthUsage], "cursor") {
		t.Errorf("nothing names the vendor everything transits: reason=%q help=%q",
			r.Reason, r.Help[credentials.AuthUsage])
	}
}

// The hint has to name the FILE when the key lives in one: "host env" sends the
// operator looking in the wrong place for the credential they are about to spend.
func TestAuthHelpNamesTheEnvFileAKeyActuallyLivesIn(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=sk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, ok := authRow(opencodeMan(), env(map[string]string{
		"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk",
	}), "opencode", "", envFile, "")
	if !ok {
		t.Fatal("no auth row")
	}
	if !strings.Contains(r.Help[credentials.AuthUsage], envFile) {
		t.Errorf("usage help = %q, want the .env the key lives in", r.Help[credentials.AuthUsage])
	}
}

// A gated option with no reason is a dead end, and this row gates two of three
// on an ordinary host.
func TestEveryGatedOptionCarriesItsReason(t *testing.T) {
	t.Parallel()
	r, ok := authRow(cursorMan(), env(map[string]string{"CURSOR_API_KEY": "cur"}), "cursor", "", "", "")
	if !ok {
		t.Fatal("no auth row")
	}
	for i, opt := range r.Options {
		if r.Off[i] && strings.TrimSpace(r.OffWhy[opt]) == "" && strings.TrimSpace(r.Reason) == "" {
			t.Errorf("%q is gated with no reason anywhere on the row", opt)
		}
	}
}

// cecli has no plan of its own, so there is no question to put to it.
func TestNoAuthRowForAHarnessWithNoPlan(t *testing.T) {
	t.Parallel()
	cecli := manifest.Manifest{Name: "cecli"}
	if _, ok := authRow(cecli, env(map[string]string{"ANTHROPIC_API_KEY": "sk"}), "cecli", "", "", ""); ok {
		t.Error("asked cecli a subscription question it has no answer to")
	}
}

// And no row when nothing authenticates at all: run.go says that in full, with
// the instructions, rather than as three gated options.
func TestNoAuthRowWithNothingToAuthenticateWith(t *testing.T) {
	t.Parallel()
	if _, ok := authRow(opencodeMan(), env(nil), "opencode", "", "", ""); ok {
		t.Error("rendered an auth row with no credential at all")
	}
}

// A remembered answer from before the row had classes is a variable NAME.
// Dropping it silently would re-open the run on the other side of the choice.
func TestARememberedVariableNameDoesNotSelectTheWrongSide(t *testing.T) {
	t.Parallel()
	available := []string{credentials.AuthUsage, credentials.AuthSubscription}
	if got := authAnswer(credentials.AuthVarLogin, available); got != credentials.AuthSubscription {
		t.Errorf("a remembered login answer = %q, want the plan", got)
	}
	if got := authAnswer(credentials.AuthSubscription, available); got != credentials.AuthSubscription {
		t.Errorf("a class answer = %q, want it kept", got)
	}
	if got := authAnswer("ANTHROPIC_API_KEY", available); got != "" {
		t.Errorf("a stale variable name = %q, want availability to decide instead", got)
	}
}
