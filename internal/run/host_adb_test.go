// SPEC: _spec/internal/sbx/host-android-adb.puml
package run

import (
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
)

func devices(kinds ...string) manifest.Manifest {
	return manifest.Manifest{Capabilities: manifest.Capabilities{HostDevices: kinds}}
}

func TestDeviceAddonsAreOfferedByCapability(t *testing.T) {
	t.Parallel()
	if opts := interfaceOptions(manifest.Manifest{}); slices.Contains(opts, addonAndroid) || slices.Contains(opts, addonIOS) {
		t.Errorf("no hostDevices ⇒ %v; want neither device row", opts)
	}
	opts := interfaceOptions(devices(manifest.HostDeviceAndroid, manifest.HostDeviceIOS))
	if i, j := slices.Index(opts, addonAndroid), slices.Index(opts, addonIOS); i < 0 || j < i {
		t.Errorf("hostDevices [android, ios] ⇒ %v; want android then ios", opts)
	}
}

func TestIOSIsAlwaysGreyedComingSoon(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowInterface, Options: []string{addonTUI, addonAndroid, addonIOS}, Multi: true, On: []bool{true, false, true}},
	}}
	gateAddons(f, "allowlist", "inject", "", "")
	r := f.Rows[0]
	if !r.Off[2] || r.On[2] {
		t.Errorf("ios must be greyed and unticked: off=%v on=%v", r.Off, r.On)
	}
	if !strings.Contains(r.OffWhy[addonIOS], "coming soon") || !strings.Contains(r.Reason, "coming soon") {
		t.Errorf("ios must explain itself as coming soon: why=%q reason=%q", r.OffWhy[addonIOS], r.Reason)
	}
	if r.Off[1] {
		t.Errorf("with sbx android must be live: reason=%q", r.Reason)
	}
}

func TestAndroidNeedsSbx(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowInterface, Options: []string{addonAndroid}, Multi: true, On: []bool{true}},
	}}
	gateAddons(f, "allowlist", "inject", "no sbx", "")
	if r := f.Rows[0]; !r.Off[0] || r.On[0] || !strings.Contains(r.Reason, "adb server") {
		t.Errorf("without sbx android must grey+untick: off=%v on=%v reason=%q", r.Off, r.On, r.Reason)
	}
}

func TestDeviceAddonFlags(t *testing.T) {
	t.Parallel()
	man := devices(manifest.HostDeviceAndroid, manifest.HostDeviceIOS)
	p := Params{Target: "claudecode", Addons: []string{"android"}, AddonsSet: true}
	if err := p.resolveAddonFlags(man); err != nil || !slices.Equal(p.Addons, []string{addonAndroid}) {
		t.Errorf("--addon android ⇒ %v, %v", p.Addons, err)
	}
	ios := Params{Target: "claudecode", Addons: []string{"ios"}, AddonsSet: true}
	if err := ios.resolveAddonFlags(man); err == nil || !strings.Contains(err.Error(), "coming soon") {
		t.Errorf("--addon ios must be refused as coming soon: %v", err)
	}
}

func TestTopologyNamesTheAndroidInterface(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowInterface, Options: []string{addonTUI, addonAndroid}, Multi: true, On: []bool{true, true}},
	}}
	if got := interfaceOf(f); got != "tui + android" {
		t.Errorf("interfaceOf = %q", got)
	}
}

func TestStartHostADBIsInertWithoutTheAddon(t *testing.T) {
	t.Parallel()
	env, err := startHostADB(&Params{Addons: []string{addonTUI}})
	if env != nil || err != nil {
		t.Errorf("no android add-on ⇒ %v, %v; want nothing", env, err)
	}
}
