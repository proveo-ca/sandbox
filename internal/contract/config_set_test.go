// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml
package contract_test

import (
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
)

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

// A transient path reaches the shell's prune list; claudecode's job scratch is one.
func TestEveryTransientPathReachesTheConfigSet(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	sawJobs := false
	for _, m := range ms {
		set := proveohome.ConfigSet(m.Home)
		for _, mt := range m.Home.Mounts {
			for _, p := range mt.Transient {
				if !strings.Contains(set, p) {
					t.Errorf("%s: transient %q is not in the config set %q", m.Name, p, set)
				}
				if m.Name == "claudecode" && p == "jobs/*/tmp" {
					sawJobs = true
				}
			}
		}
	}
	if !sawJobs {
		t.Error("claudecode no longer marks jobs/*/tmp transient — one job's scratch cost 32s of every boot (measured 2026-10-13)")
	}
}
