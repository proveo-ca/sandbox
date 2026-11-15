// SPEC: _spec/_conventions/spec-conventions.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// neutralHomes are the macOS home names a tracked file may quote.
var neutralHomes = map[string]bool{"": true, "a": true, "dev": true, "me": true, "op": true, "other": true, "p": true, "u": true, "x": true}

// homeName matches a macOS home path and its Claude Code projects/ key form.
var homeName = regexp.MustCompile(`/Users/([A-Za-z0-9._-]*)|-Users-([A-Za-z0-9]+)-`)

func TestNoTrackedFileNamesARealHome(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range trackedFiles(t) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		for _, m := range homeName.FindAllSubmatch(b, -1) {
			if !neutralHomes[string(m[1])+string(m[2])] {
				t.Errorf("%s quotes the home %q; use a neutral one such as /Users/dev", rel, m[0])
				break
			}
		}
	}
}
