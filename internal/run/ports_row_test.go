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

func TestPortsPastFourOpenFromMore(t *testing.T) {
	t.Parallel()
	found := []devports.Candidate{
		{Port: 3000, Tool: "next", Command: "dev", Source: "a/package.json"},
		{Port: 5173, Tool: "vite", Command: "dev", Source: "b/package.json"},
		{Port: 8080, Tool: "web", Command: "dev", Source: "c/package.json"},
		{Port: 4000, Tool: "jekyll", Command: "serve", Source: "d/package.json"},
		{Port: 1313, Tool: "hugo", Command: "server", Source: "e/package.json"},
	}
	r, ok := portsRow(found, found[3:4])
	if !ok {
		t.Fatal("five ports drew no row")
	}
	if diff := cmp.Diff([]string{"3000 next", "5173 vite", "8080 web", portsMore}, r.Options); diff != "" {
		t.Errorf("options mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int{0, 1, 2, 3}, r.Cols); diff != "" {
		t.Errorf("cols mismatch (-want +got):\n%s", diff)
	}
	if r.More == nil {
		t.Fatal("...More has no list")
	}
	if diff := cmp.Diff([]string{"4000 jekyll", "1313 hugo"}, r.More.Options); diff != "" {
		t.Errorf("dropdown mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]bool{false, false, false, true}, r.On); diff != "" {
		t.Errorf("row ticks mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]bool{true, false}, r.More.On); diff != "" {
		t.Errorf("dropdown ticks mismatch (-want +got):\n%s", diff)
	}
	f := &choiceui.Form{Rows: []choiceui.Row{r}}
	if diff := cmp.Diff(found[3:4], selectedPorts(f, found)); diff != "" {
		t.Errorf("selectedPorts mismatch (-want +got):\n%s", diff)
	}
}

func TestFourPortsStayOnTheRow(t *testing.T) {
	t.Parallel()
	found := []devports.Candidate{
		{Port: 1, Tool: "a", Source: "a"},
		{Port: 2, Tool: "b", Source: "b"},
		{Port: 3, Tool: "c", Source: "c"},
		{Port: 4, Tool: "d", Source: "d"},
	}
	r, ok := portsRow(found, nil)
	if !ok || r.More != nil || len(r.Options) != 4 {
		t.Fatalf("options %v more %v ok %v, want four cells and no dropdown", r.Options, r.More, ok)
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

func TestInterfaceRowIsNamedDevicesUnderTheInterfaceHeading(t *testing.T) {
	t.Parallel()
	if rowInterface != "devices" || addonHeading[rowInterface] != "interface" {
		t.Errorf("interface row = {Label %q Heading %q}, want {devices interface}", rowInterface, addonHeading[rowInterface])
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

var foundApps = []devports.AndroidApp{
	{Module: ":app", AppID: "ca.proveo.hello", Source: "app/build.gradle.kts"},
	{Module: ":wear", AppID: "ca.proveo.wear", Source: "wear/build.gradle"},
}

func TestAppsRowIsGreyedUntilAndroidIsTicked(t *testing.T) {
	t.Parallel()
	apps, ok := appsRow(foundApps, foundApps[:1])
	if !ok {
		t.Fatal("appsRow(2 found) drew no row")
	}
	if diff := cmp.Diff([]bool{true, false}, apps.On); diff != "" {
		t.Errorf("appsRow defaults mismatch (-want +got):\n%s", diff)
	}
	iface := choiceui.Row{Label: rowInterface, Multi: true, Options: []string{addonTUI, addonAndroid}, On: []bool{true, false}}
	f := &choiceui.Form{Rows: []choiceui.Row{apps, iface}}
	gateApps(f)
	if got := selectedApps(f, foundApps); len(got) != 0 {
		t.Errorf("selectedApps with android unticked = %v, want none", got)
	}
	if f.Rows[0].Reason != appsNeedAndroid {
		t.Errorf("apps row reason = %q, want %q", f.Rows[0].Reason, appsNeedAndroid)
	}
	f.Rows[1].On[1] = true
	gateApps(f)
	if diff := cmp.Diff(foundApps[:1], selectedApps(f, foundApps)); diff != "" {
		t.Errorf("selectedApps with android ticked mismatch (-want +got):\n%s", diff)
	}
}

func TestRememberedAppsFollowThisRunsDiscovery(t *testing.T) {
	t.Parallel()
	saved := []agentsettings.App{{Module: ":wear", AppID: "ca.proveo.wear"}, {Module: ":gone", AppID: "x.y"}}
	if diff := cmp.Diff(foundApps[1:], rememberedApps(saved, foundApps)); diff != "" {
		t.Errorf("rememberedApps mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(foundApps, rememberedApps(appsToRemember(foundApps), foundApps)); diff != "" {
		t.Errorf("remember → recall mismatch (-want +got):\n%s", diff)
	}
}

func TestAppsInstallOnlyWithTheAndroidAddon(t *testing.T) {
	t.Parallel()
	p := &Params{Apps: foundApps}
	if got := androidApps(p); got != nil {
		t.Errorf("androidApps without android = %v, want none", got)
	}
	p.Addons = []string{addonAndroid}
	if diff := cmp.Diff(foundApps, androidApps(p)); diff != "" {
		t.Errorf("androidApps with android mismatch (-want +got):\n%s", diff)
	}
}
