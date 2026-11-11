// SPEC: _spec/internal/sbx/host-android-adb.puml
package run

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/hostcdp"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
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
	gateAddons(f)
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
	env, fact, items, err := startHostADB(&Params{Addons: []string{addonTUI}})
	if env != nil || fact.Value != "" || items != nil || err != nil {
		t.Errorf("no android add-on ⇒ %v, %+v, %v, %v; want nothing", env, fact, items, err)
	}
}

func TestAPrintedAddonIsOneFactAndWarnsUnderInterface(t *testing.T) {
	env, fact, items, err := startHostADB(&Params{Addons: []string{addonAndroid}, PrintOnly: true})
	if err != nil || len(env) == 0 {
		t.Fatalf("startHostADB(--print) = %v, %v", env, err)
	}
	if fact.Key != addonAndroid || fact.Value != "not started in print mode" || len(fact.Detail) != 1 || !strings.HasPrefix(fact.Detail[0], "adb server 127.0.0.1:") {
		t.Errorf("fact = %+v, want the print-mode android fact with its adb server dim under it", fact)
	}
	if len(items) != 1 || !items[0].Warn || !strings.Contains(items[0].Text, "the agent controls every device on that adb server") {
		t.Errorf("items = %+v, want the ⚠ reach item alone", items)
	}
}

func TestEachHostAddOnFactNamesWhatTheRunLeftUp(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for _, c := range []struct {
		name string
		got  ui.Fact
		want ui.Fact
	}{
		{"a booted AVD", androidFact(hostadb.Started{Serial: "emulator-5554", AVD: "Pixel_9"}, 5037),
			ui.Fact{Role: ui.RoleHost, Key: "android", Value: "emulator-5554 (AVD Pixel_9)", Detail: []string{"adb server 127.0.0.1:5037"}}},
		{"a reused device", androidFact(hostadb.Started{Serial: "emulator-5556"}, 5037),
			ui.Fact{Role: ui.RoleHost, Key: "android", Value: "emulator-5556 (reused)", Detail: []string{"adb server 127.0.0.1:5037"}}},
		{"an opened browser", chromeFact(hostcdp.Opened{Browser: "Google Chrome", Profile: filepath.Join(home, "p", "claudecode")}, 9333),
			ui.Fact{Role: ui.RoleHost, Key: "chrome", Value: "Google Chrome · CDP 127.0.0.1:9333", Detail: []string{"profile ~/p/claudecode"}}},
		{"a reused browser", chromeFact(hostcdp.Opened{}, 9333),
			ui.Fact{Role: ui.RoleHost, Key: "chrome", Value: "CDP 127.0.0.1:9333 (reused)"}},
		{"an installed desktop", desktopFact(false), ui.Fact{Role: ui.RoleHost, Key: "desktop", Value: "Hermes Desktop"}},
		{"a desktop this run installed", desktopFact(true),
			ui.Fact{Role: ui.RoleHost, Key: "desktop", Value: "Hermes Desktop " + hermesdesktop.Tag + " (installed now)"}},
	} {
		if diff := cmp.Diff(c.want, c.got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
		}
	}
}

func TestAnAddonsProgressReachesOnlyTheRunLog(t *testing.T) {
	var screen, log strings.Builder
	prev := ui.Default
	ui.Default = ui.New(&screen)
	restore := ui.TeeTo(&log)
	t.Cleanup(func() { restore(); ui.Default = prev })
	err := startAddon("android", func(report func(string, ...any)) error {
		report("android: booting AVD %s (adb server 127.0.0.1:%d)", "Pixel_9", 5037)
		report("android: %s booted", "emulator-5554")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := screen.String(); got != "" {
		t.Errorf("a plain run printed the add-on's progress on screen: %q", got)
	}
	for _, want := range []string{"android — starting", "android: booting AVD Pixel_9 (adb server 127.0.0.1:5037)", "android: emulator-5554 booted"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the run log lacks %q:\n%s", want, log.String())
		}
	}
}
