// SPEC: _spec/_plans/config-seeding-and-persistence.puml
package proveohome

import (
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
			{Host: "opencode/config", Container: "/proveo-home/.config/opencode", Mode: "rw"},
			{Host: "opencode/share", Container: "/proveo-home/.local/share/opencode", Mode: "rw", Deny: []string{"auth.json"}},
		},
	}
	want := ".cursor|.cursor|auth.json;opencode/config|.config/opencode|;opencode/share|.local/share/opencode|auth.json"
	if got := ConfigSet(h); got != want {
		t.Errorf("ConfigSet() =\n  %q\nwant\n  %q", got, want)
	}
}

// HOME is not redirected on the sandbox backend: the agent reads $HOME/.claude,
// never /proveo-home/.claude. A container path outside proveo home has no
// agent-relative form, and guessing one would write config where nothing looks.
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
