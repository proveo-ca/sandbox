// SPEC: _spec/_plans/config-seeding-and-persistence.puml
package contract_test

import (
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
)

// Every shipped harness must be fully expressible as a config set, or its
// configuration silently stops persisting on the sandbox backend — the exact
// failure this whole mechanism exists to close.
func TestEveryHarnessHomeMountReachesTheConfigSet(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no manifests embedded")
	}
	saw := false
	for _, m := range ms {
		if !m.Home.Active() {
			continue
		}
		saw = true
		set := proveohome.ConfigSet(m.Home)
		if set == "" {
			t.Errorf("%s: declares durable home mounts but encodes to nothing", m.Name)
			continue
		}
		if got, want := strings.Count(set, ";")+1, len(m.Home.Mounts); got != want {
			t.Errorf("%s: encoded %d of %d home mounts (%q) — a dropped mount is config "+
				"that stops persisting on sbx", m.Name, got, want, set)
		}
		// Whatever the manifest denies must reach the shell's skip set, or a file
		// the host scrubs is a file the sandbox copies back out.
		for _, mt := range m.Home.Mounts {
			for _, d := range mt.Deny {
				if d = strings.TrimSpace(d); d != "" && !strings.Contains(set, d) {
					t.Errorf("%s: deny %q is not in the config set %q", m.Name, d, set)
				}
			}
		}
	}
	if !saw {
		t.Fatal("no harness declares a durable home — the fleet assertion is vacuous")
	}
}
