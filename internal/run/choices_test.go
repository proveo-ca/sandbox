package run

import (
	"slices"
	"strings"
	"testing"

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
	row := credentialsRow(man, "forward", true)
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
}

func TestBothModesStayOnTheDockerRendering(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "claudecode",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward", "broker"}},
	}
	row := credentialsRow(man, "forward", false)
	for i, o := range row.Options {
		if i < len(row.Off) && row.Off[i] {
			t.Errorf("%q is gated off the docker rendering, where a sidecar does the injecting", o)
		}
	}
}

func TestAForwardOnlyHarnessKeepsTheRowOnDockerByGatingBroker(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}},
	}
	row := credentialsRow(man, "broker", false)
	if len(row.Options) < 2 {
		t.Fatalf("options = %v, want both modes so the row is drawn", row.Options)
	}
	i := slices.Index(row.Options, "broker")
	if i < 0 || i >= len(row.Off) || !row.Off[i] {
		t.Error("broker must stay visible and gated on docker when the harness cannot intercept TLS")
	}
	if row.Options[row.Selected] != "forward" {
		t.Errorf("selected %q, want forward — the only route this harness can take on docker", row.Options[row.Selected])
	}
}

func TestFormSelectedBrokerSurvivesAForwardOnlyManifestOnSbx(t *testing.T) {
	t.Parallel()
	p := Params{Target: "cursor", Credentials: "broker"}
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}, Egress: []string{"open"}},
	}
	if err := p.applyCapabilitiesAt(man, true); err != nil {
		t.Fatalf("form-selected broker was refused: %v", err)
	}
	if p.Credentials != "broker" {
		t.Errorf("credentials = %q, want broker kept", p.Credentials)
	}
}

func TestAnEmptyDefaultStillRewritesOntoAForwardOnlyManifest(t *testing.T) {
	t.Parallel()
	p := Params{Target: "cursor"}
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}, Egress: []string{"open"}},
	}
	if err := p.applyCapabilitiesAt(man, true); err != nil {
		t.Fatalf("empty default was refused: %v", err)
	}
	if p.Credentials != "forward" {
		t.Errorf("credentials = %q, want the empty default rewritten to the declared mode", p.Credentials)
	}
}

func TestAnExplicitBrokerFlagIsStillRefusedOnAForwardOnlyManifest(t *testing.T) {
	t.Parallel()
	p := Params{Target: "cursor", Credentials: "broker", CredsSet: true}
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}, Egress: []string{"open"}},
	}
	err := p.applyCapabilitiesAt(man, true)
	if err == nil {
		t.Fatal("expected --credentials broker to stay refused when the operator named it")
	}
	if !strings.Contains(err.Error(), "does not support --credentials broker") {
		t.Errorf("error = %v, want the capability refusal", err)
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
