// SPEC: _spec/internal/sbx/clone-workspace.puml
package choiceui

import (
	"strings"
	"testing"
	"time"
)

func TestFetchPushedCopyNamesTheBranchesAndTheWait(t *testing.T) {
	t.Parallel()
	got := strings.Join(FetchPushedCopy([]string{"origin/feat"}, 15*time.Second), "\n")
	for _, want := range []string{"origin/feat", "15s with no answer leaves them"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
