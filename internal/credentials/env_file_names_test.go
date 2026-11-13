// SPEC: _spec/internal/choiceui/wireframe.puml
package credentials

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEnvFileNamesListsNamesAToZWithoutValues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	body := strings.Join([]string{
		"# a comment=not-a-name",
		"zeta=value-zeta-8f3",
		"export ALPHA='value-alpha-8f3'",
		"",
		"=value-nameless-8f3",
		"Beta=\"value-beta-8f3\"",
		"no equals sign",
		"ALPHA=value-alpha-again-8f3",
		"  gamma = value-gamma-8f3  ",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := EnvFileNames(path)
	if want := []string{"ALPHA", "Beta", "gamma", "zeta"}; !slices.Equal(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "value-") {
		t.Fatalf("a value reached the names: %q", got)
	}
	if got := EnvFileNames(filepath.Join(t.TempDir(), "missing")); len(got) != 0 {
		t.Fatalf("a missing file named %q", got)
	}
	if got := EnvFileNames(""); len(got) != 0 {
		t.Fatalf("no file named %q", got)
	}
}
