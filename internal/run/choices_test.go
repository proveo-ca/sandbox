package run

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

// SPEC: _spec/_plans/init-credential-provisioning.puml
// On sbx the credential lives in the proxy, so `forward` — which puts the value
// in the agent's own environment — is drawn and gated rather than dropped: an
// option that vanishes is one an operator cannot find out about.
func TestForwardIsGatedOffWhenSbxRunsTheAgent(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "claudecode",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward", "broker"}},
	}
	row := credentialsRow(man, "forward", true)

	var forwardAt = -1
	for i, o := range row.Options {
		if o == "forward" {
			forwardAt = i
		}
	}
	if forwardAt < 0 {
		t.Fatalf("forward is gone from %v — a withdrawn option answers no question", row.Options)
	}
	if forwardAt >= len(row.Off) || !row.Off[forwardAt] {
		t.Error("forward is selectable on sbx, where it would hand the agent a spendable credential")
	}
	if got := row.Options[row.Selected]; got != "broker" {
		t.Errorf("selected %q, want broker: it is the only mode sbx can honour", got)
	}
	if row.Reason == "" {
		t.Error("no reason given; a greyed option that says nothing is what teaches an operator to distrust the form")
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
