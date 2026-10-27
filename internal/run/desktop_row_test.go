// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package run

import (
	"slices"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestOnlyHermesOffersTheDesktopDevice(t *testing.T) {
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		got := slices.Contains(interfaceOptions(m), addonDesktop)
		if want := m.Name == "hermes"; got != want {
			t.Errorf("%s offers desktop = %v, want %v — only Hermes ships a desktop client that attaches over SSH", m.Name, got, want)
		}
	}
	if addonHelp[addonDesktop] == "" {
		t.Error("the desktop option has no help line")
	}
}

func TestNoHarnessLeavesAGapInItsDevicesRow(t *testing.T) {
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		r := layoutInterface(choiceui.Row{Label: rowInterface, Multi: true, Options: interfaceOptions(m)})
		for i, c := range r.Cols {
			if c != i {
				t.Errorf("%s devices %v cols %v: column %d holds option %d — an empty cell sits where an absent option would go", m.Name, r.Options, r.Cols, c, i)
				break
			}
		}
		if slices.Contains(r.Options, addonDesktop) != (len(r.RadioSet) > 0) {
			t.Errorf("%s: radio set %v without desktop, or desktop without the radio", m.Name, r.RadioSet)
		}
		t.Logf("%-10s %v", m.Name, r.Options)
	}
}
