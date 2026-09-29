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

// cursor's manifest declares forward, the only mode the docker-egress tiers
// cannot break; on sbx the proxy brokers it natively, so broker wins there
// whether it came from the default or the operator (decided 2026-09-29).
func TestAnEmptyDefaultBrokersOnSbxAndForwardsOnDocker(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}, Egress: []string{"open"}},
	}
	for sbxOn, want := range map[bool]string{true: "", false: "forward"} {
		p := Params{Target: "cursor"}
		if err := p.applyCapabilitiesAt(man, sbxOn); err != nil {
			t.Fatalf("sbx=%v: empty default was refused: %v", sbxOn, err)
		}
		if p.Credentials != want {
			t.Errorf("sbx=%v: credentials = %q, want %q (empty means the broker default)", sbxOn, p.Credentials, want)
		}
	}
}

func TestAnExplicitBrokerFlagBrokersOnSbxAndIsRefusedOnDocker(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}, Egress: []string{"open"}},
	}
	p := Params{Target: "cursor", Credentials: "broker", CredsSet: true}
	if err := p.applyCapabilitiesAt(man, true); err != nil || p.Credentials != "broker" {
		t.Errorf("sbx: --credentials broker = %q, %v; sbx's proxy brokers cursor natively", p.Credentials, err)
	}
	p = Params{Target: "cursor", Credentials: "broker", CredsSet: true}
	err := p.applyCapabilitiesAt(man, false)
	if err == nil || !strings.Contains(err.Error(), "does not support --credentials broker") {
		t.Errorf("docker: err = %v — an intercepting broker breaks cursor's pinned TLS, so naming it stays refused", err)
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
	r := credentialsRow(cursor, "broker", true)
	if !strings.Contains(r.Help["broker"], "cursor always brokers credentials natively") {
		t.Errorf("broker help = %q", r.Help["broker"])
	}
	for i, o := range r.Options {
		if o == "forward" && (r.Off == nil || !r.Off[i]) {
			t.Error("forward must stay disabled for cursor on sbx")
		}
	}
	if r := credentialsRow(manifest.Manifest{Name: "codex"}, "broker", true); r.Help["broker"] != "" {
		t.Errorf("a harness that declares no credentials capability gets no cursor note: %q", r.Help["broker"])
	}
}
