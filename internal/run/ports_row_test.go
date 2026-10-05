// SPEC: _spec/internal/devports/dev-ports.puml
package run

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/devports"
)

var foundPorts = []devports.Candidate{
	{Port: 3000, Command: "dev", Tool: "next", Source: "apps/web/package.json"},
	{Port: 6006, Command: "storybook", Tool: "storybook", Source: "apps/docs/package.json", Explicit: true},
}

func TestPortsRowOffersEveryPortUnticked(t *testing.T) {
	t.Parallel()
	r, ok := portsRow(foundPorts, nil)
	if !ok {
		t.Fatalf("portsRow(%d found) drew no row", len(foundPorts))
	}
	if diff := cmp.Diff([]string{"3000 next", "6006 storybook"}, r.Options); diff != "" {
		t.Errorf("portsRow options mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]bool{false, false}, r.On); diff != "" {
		t.Errorf("portsRow defaults mismatch (-want +got):\n%s", diff)
	}
	if r.Label != rowPorts || !r.Multi || r.Divider {
		t.Errorf("portsRow = {Label %q Multi %v Divider %v}, want {%q true false}", r.Label, r.Multi, r.Divider, rowPorts)
	}
	for _, opt := range r.Options {
		if r.Help[opt] == "" {
			t.Errorf("portsRow option %q has no help", opt)
		}
	}
}

func TestPortsRowKeepsAnEarlierChoice(t *testing.T) {
	t.Parallel()
	r, _ := portsRow(foundPorts, foundPorts[1:])
	if diff := cmp.Diff([]bool{false, true}, r.On); diff != "" {
		t.Errorf("portsRow(chosen 6006) defaults mismatch (-want +got):\n%s", diff)
	}
}

func TestPortsRowHidesWhenNothingIsFound(t *testing.T) {
	t.Parallel()
	if _, ok := portsRow(nil, nil); ok {
		t.Error("portsRow(nil) drew a row with no ports")
	}
}

func TestSelectedPortsReadsTheTickedBoxes(t *testing.T) {
	t.Parallel()
	r, _ := portsRow(foundPorts, nil)
	r.On = []bool{true, true}
	f := &choiceui.Form{Rows: []choiceui.Row{r}}
	if diff := cmp.Diff(foundPorts, selectedPorts(f, foundPorts)); diff != "" {
		t.Errorf("selectedPorts mismatch (-want +got):\n%s", diff)
	}
}

func TestExecutionRowIsNamedOSUnderTheExecutionHeading(t *testing.T) {
	t.Parallel()
	if rowExecution != "OS" || addonHeading[rowExecution] != "execution" {
		t.Errorf("execution row = {Label %q Heading %q}, want {OS execution}", rowExecution, addonHeading[rowExecution])
	}
}

func TestRememberedPortsFollowThisRunsDiscovery(t *testing.T) {
	t.Parallel()
	saved := []agentsettings.Port{
		{Port: 6006, Command: "storybook", Source: "apps/docs/package.json"},
		{Port: 9000, Command: "dev", Source: "gone/package.json"},
	}
	if diff := cmp.Diff(foundPorts[1:], rememberedPorts(saved, foundPorts)); diff != "" {
		t.Errorf("rememberedPorts mismatch (-want +got):\n%s", diff)
	}
}

func TestPortsRoundTripThroughTheStore(t *testing.T) {
	t.Parallel()
	if diff := cmp.Diff(foundPorts, rememberedPorts(portsToRemember(foundPorts), foundPorts)); diff != "" {
		t.Errorf("remember → recall mismatch (-want +got):\n%s", diff)
	}
	if got := portsToRemember(nil); got == nil || len(got) != 0 {
		t.Errorf("portsToRemember(nil) = %#v, want an empty answer", got)
	}
}
