// SPEC: _spec/defs/agent-definition-sharing.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// retiredAgentsDir matches the per-harness subagent directory that no image has
// written since the move to the shared tree: /opt/<harness>/defaults/agents.
var retiredAgentsDir = regexp.MustCompile(`/opt/[a-z-]+/defaults/agents\b`)

// Subagents live in ONE shared tree, /opt/proveo/subagents, with a per-harness
// _roster.json. Four test files went on asserting the retired per-harness path
// long after nothing wrote it, and the failures read as broken images rather than
// stale tests. Worse, several were assert_failure checks — `grep … | grep -q .`
// over a directory that does not exist FAILS, so they reported PASS having
// verified nothing.
//
// cecli's test.sh is the counter-example that never went stale: it reads the
// roster out of the image instead of restating it.
func TestNoTestAssertsTheRetiredSubagentDir(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	for _, sub := range []string{"defs", "e2e"} {
		err := filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sh") {
				return err
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			rel, _ := filepath.Rel(root, path)
			for i, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue // a comment may name the retired path to explain it
				}
				if retiredAgentsDir.MatchString(line) {
					t.Errorf("%s:%d asserts the retired per-harness subagent dir:\n\t%s\n"+
						"subagents are one shared tree at /opt/proveo/subagents with a "+
						"per-harness _roster.json — read the roster from the image rather "+
						"than restating it", rel, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
