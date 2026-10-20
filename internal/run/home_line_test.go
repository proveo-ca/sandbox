// SPEC: _spec/internal/proveohome/proveo-home-components.puml
package run

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestTheHomeLineNamesWhatTheSandboxSees(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		home manifest.Home
		want string
	}{
		{"declared dirs", manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{Host: "opencode/v2/config"}, {Host: "/.cursor/"}}},
			"proveo home: /h/.proveo (the sandbox sees opencode/v2/config, .cursor, toolchains at their host paths)"},
		{"nothing declared", manifest.Home{Enabled: true},
			"proveo home: /h/.proveo (the sandbox sees toolchains at its host path)"},
	} {
		if got := homeLine("/h/.proveo", c.home); got != c.want {
			t.Errorf("homeLine(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}
