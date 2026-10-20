package run

import (
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
)

// SPEC: _spec/_plans/init-credential-provisioning.puml, _spec/internal/choiceui/wireframe.puml, _spec/internal/credentials/credential-decisions.puml, _spec/internal/sbx/credential-path.puml
func TestForwardIsGatedOnSbxAndTheRowStillHasBothModes(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}},
	}
	row := credentialsRow(man, "forward")
	if len(row.Options) < 2 {
		t.Fatalf("options = %v, want both modes so applicableRows cannot drop the row", row.Options)
	}
	i := slices.Index(row.Options, "forward")
	if i < 0 {
		t.Fatal("forward is missing, so the operator cannot see what is not on offer")
	}
	if i >= len(row.Off) || !row.Off[i] {
		t.Error("forward is still selectable on sbx; the proxy holds the value")
	}
	if row.Options[row.Selected] != "broker" {
		t.Errorf("selected %q, want broker — the only route sbx will honour", row.Options[row.Selected])
	}
	if row.Reason == "" {
		t.Error("no reason given, so a greyed forward looks like a defect")
	}
	if !row.Locked {
		t.Error("credentials is not locked, so the operator cannot hover both modes to read them")
	}
	if row.Options[row.Hover] != "broker" {
		t.Errorf("hover opens on %q, want broker — the mode in force", row.Options[row.Hover])
	}
}

func TestFirstChoiceIsTheFirstChangeableOption(t *testing.T) {
	t.Parallel()
	withBrowser := &choiceui.Row{Options: []string{addonTUI, addonBrowser, addonAndroid}}
	if got := firstChoice(withBrowser); got != addonBrowser {
		t.Errorf("firstChoice = %q, want browser", got)
	}
	noBrowser := &choiceui.Row{Options: []string{addonTUI, addonAndroid}}
	if got := firstChoice(noBrowser); got != addonAndroid {
		t.Errorf("firstChoice without browser = %q, want the first unfixed option", got)
	}
	onlyFixed := &choiceui.Row{Options: []string{addonTUI}}
	if got := firstChoice(onlyFixed); got != "" {
		t.Errorf("firstChoice with only compulsory options = %q, want none", got)
	}
}

func TestFormSelectedBrokerSurvivesAForwardOnlyManifestOnSbx(t *testing.T) {
	t.Parallel()
	p := Params{Target: "cursor", Credentials: "broker"}
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}},
	}
	if err := p.applyCapabilities(man); err != nil {
		t.Fatalf("form-selected broker was refused: %v", err)
	}
	if p.Credentials != "broker" {
		t.Errorf("credentials = %q, want broker kept", p.Credentials)
	}
}

// cursor's manifest declares forward; sbx's proxy brokers it natively, so broker wins
// whether it came from the default or the operator (decided 2026-09-29).
func TestAnEmptyDefaultOrAnExplicitBrokerBrokersOnSbx(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}},
	}
	p := Params{Target: "cursor"}
	if err := p.applyCapabilities(man); err != nil || p.Credentials != "" {
		t.Errorf("empty default: credentials = %q, %v; want the broker default kept", p.Credentials, err)
	}
	p = Params{Target: "cursor", Credentials: "broker", CredsSet: true}
	if err := p.applyCapabilities(man); err != nil || p.Credentials != "broker" {
		t.Errorf("--credentials broker = %q, %v; sbx's proxy brokers cursor natively", p.Credentials, err)
	}
}

func TestHeaderOmitsKeysPresentOnlyInLookup(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "")
	man := manifest.Manifest{
		Name: "opencode",
		Env:  []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}},
	}
	lookup := func(k string) string {
		if k == "OPENCODE_API_KEY" {
			return "from-dotenv"
		}
		return ""
	}
	h := buildHeader(man, lookup, provider.Roles{}, t.TempDir(), t.TempDir(), "")
	if joined := strings.Join(h, "\n"); strings.Contains(joined, "OPENCODE_API_KEY") {
		t.Errorf("header listed a key that is not in the process env:\n%s", joined)
	}
}

func TestCredentialsRowSaysCursorBrokersNativelyOnSbx(t *testing.T) {
	t.Parallel()
	cursor := manifest.Manifest{Name: "cursor", Capabilities: manifest.Capabilities{Credentials: []string{"forward"}}}
	r := credentialsRow(cursor, "broker")
	if !strings.Contains(r.Help["broker"], "cursor always brokers credentials natively") {
		t.Errorf("broker help = %q", r.Help["broker"])
	}
	for i, o := range r.Options {
		if o == "forward" && (r.Off == nil || !r.Off[i]) {
			t.Error("forward must stay disabled for cursor on sbx")
		}
	}
	if r := credentialsRow(manifest.Manifest{Name: "codex"}, "broker"); r.Help["broker"] != "" {
		t.Errorf("a harness that declares no credentials capability gets no cursor note: %q", r.Help["broker"])
	}
}
