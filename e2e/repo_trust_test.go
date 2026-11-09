//go:build e2e

// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/packages/lib/seed-and-launch.puml
package e2e

import (
	"slices"
	"testing"
)

func TestRepoTrustChoiceReachesThePrintedWorkload(t *testing.T) {
	for _, answer := range []string{"off", "on"} {
		t.Run(answer, func(t *testing.T) {
			_, create := proveoWorkload(t, t.TempDir(), "codex", append(isolatedHome(t), "PROVEO_MISE_TRUST="+answer))
			if !slices.Contains(create, "PROVEO_MISE_TRUST="+answer) {
				t.Errorf("create args %v lack the selected repo trust answer %s", create, answer)
			}
		})
	}
}
