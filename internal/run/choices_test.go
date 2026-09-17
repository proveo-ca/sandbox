package run

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

// SPEC: _spec/_plans/init-credential-provisioning.puml
func TestForwardStaysSelectableOnSbxWithItsCostStated(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward", "broker"}},
	}
	row := credentialsRow(man, "forward", true)

	for i, o := range row.Options {
		if i < len(row.Off) && row.Off[i] {
			t.Errorf("%q is gated off on sbx; brokering covers only the hosts its proxy sees, "+
				"so removing the complete route leaves an agent no way to authenticate the rest", o)
		}
	}
	if row.Reason == "" {
		t.Error("no reason given, so the operator cannot tell what either route costs")
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
