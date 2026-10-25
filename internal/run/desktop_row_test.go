// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package run

import (
	"slices"
	"testing"

	proveo "github.com/proveo-ca/proveo"
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
