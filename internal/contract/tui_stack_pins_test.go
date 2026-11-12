// SPEC: _spec/internal/choiceui/tui-stack.puml
package contract_test

import (
	"os/exec"
	"strings"
	"testing"
)

// tuiStackPins are the Charm modules proveo draws with and the one version of each it builds against.
var tuiStackPins = map[string]string{
	"charm.land/bubbletea/v2": "v2.1.1",
	"charm.land/bubbles/v2":   "v2.2.1",
	"charm.land/lipgloss/v2":  "v2.1.0",
}

func TestTheTUIStackResolvesToItsPinnedVersions(t *testing.T) {
	t.Parallel()
	for mod, want := range tuiStackPins {
		cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", mod)
		cmd.Dir = repoRoot(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s does not resolve: %v\n%s", mod, err, out)
			continue
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%s resolves to %s, want %s; move the pin on purpose, in go.mod and here", mod, got, want)
		}
	}
}

func TestGoModNamesTheTUIStackVersionsExplicitly(t *testing.T) {
	t.Parallel()
	mod := readRepoFile(t, "go.mod")
	for path, version := range tuiStackPins {
		if !strings.Contains(mod, "\t"+path+" "+version) {
			t.Errorf("go.mod has no `%s %s` line", path, version)
		}
	}
}
