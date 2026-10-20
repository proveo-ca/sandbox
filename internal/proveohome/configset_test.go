// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml
package proveohome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestConfigSetEncodesTheManifestsOwnDeclaration(t *testing.T) {
	t.Parallel()
	h := manifest.Home{
		Enabled: true,
		Mounts: []manifest.HomeMount{
			{Host: ".cursor", Container: "/proveo-home/.cursor", Mode: "rw", Deny: []string{"auth.json"}},
			{Host: "opencode/v2/config", Container: "/proveo-home/.config/opencode", Mode: "rw"},
		},
	}
	want := ".cursor|.cursor|auth.json;opencode/v2/config|.config/opencode|"
	if got := ConfigSet(h); got != want {
		t.Errorf("ConfigSet() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestConfigSetDropsWhatItCannotExpress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		mount manifest.HomeMount
	}{
		{"container outside proveo home", manifest.HomeMount{Host: ".x", Container: "/etc/x"}},
		{"container IS proveo home", manifest.HomeMount{Host: ".x", Container: ContainerHome}},
		{"no container", manifest.HomeMount{Host: ".x"}},
		{"no host", manifest.HomeMount{Container: "/proveo-home/.x"}},
		{"separator in a path", manifest.HomeMount{Host: "a;b", Container: "/proveo-home/.x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ConfigSet(manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{tc.mount}}); got != "" {
				t.Errorf("ConfigSet() = %q, want it dropped", got)
			}
		})
	}
	if got := ConfigSet(manifest.Home{}); got != "" {
		t.Errorf("ConfigSet() = %q for an inactive home, want empty", got)
	}
}

// The shell's skip set and the host's scrub must agree on which names are
// credentials, or a file the host strips is a file the sandbox copies back out.
func TestConfigSetDenyMatchesScrubDeny(t *testing.T) {
	t.Parallel()
	got := ConfigSet(manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{
		Host: ".claude", Container: "/proveo-home/.claude",
		// scrubDeny ignores everything but a bare name; so must the encoding.
		Deny: []string{"auth.json", " .credentials.json ", "", ".", "..", "nested/path", `back\slash`, "has,comma"},
	}}})
	deny := got[strings.LastIndex(got, "|")+1:]
	if deny != "auth.json,.credentials.json" {
		t.Errorf("deny list = %q, want only the bare names scrubDeny acts on", deny)
	}
}

func TestConfigSetCarriesTransientPathsTheScrubNeverTouches(t *testing.T) {
	t.Parallel()
	mount := manifest.HomeMount{Host: ".claude", Container: "/proveo-home/.claude",
		Deny: []string{"auth.json"}, Transient: []string{"jobs/*/tmp", "../escape", "a,b"}}
	home := manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{mount}}
	if got, want := ConfigSet(home), ".claude|.claude|auth.json,jobs/*/tmp"; got != want {
		t.Errorf("ConfigSet() = %q, want %q", got, want)
	}

	root := t.TempDir()
	scratch := filepath.Join(root, ".claude", "jobs", "j1", "tmp", "probe.go")
	if err := os.MkdirAll(filepath.Dir(scratch), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scratch, []byte("package probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(home, func(k string) string {
		if k == "PROVEO_HOME" {
			return root
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("Prepare() removed transient scratch %s: %v — transient is a sync skip, never a host delete", scratch, err)
	}
}
