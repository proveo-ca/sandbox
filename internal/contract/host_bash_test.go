// SPEC: _spec/packages/lib/steps.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var bash4Only = regexp.MustCompile(`\b(mapfile|readarray)\b|\b(declare|local|typeset)\s+-[a-zA-Z]*A|\$\{[A-Za-z_][A-Za-z0-9_]*(,,|\^\^)\}|\bcoproc\b|&>>`)

// Host-run scripts execute under macOS /bin/bash 3.2.
func TestHostScriptsAvoidBash4Builtins(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var files []string
	for _, pat := range []string{"defs/lib/*.sh", "defs/*/test.sh", "defs/*/tests/*.sh", "defs/sidecars/*/test.sh", "lib/*.sh", "scripts/*.sh"} {
		m, err := filepath.Glob(filepath.Join(root, pat))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no host-run scripts matched")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if m := bash4Only.FindString(line); m != "" {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s:%d uses %q, which macOS /bin/bash 3.2 lacks", rel, i+1, m)
			}
		}
	}
}
