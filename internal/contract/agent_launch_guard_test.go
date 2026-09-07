// SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/_paradigms/capability-ladder.puml
package contract_test

import (
	"regexp"
	"testing"
)

// calls matches a line that INVOKES the guard, not one that names it.
var calls = regexp.MustCompile(`(?m)^\s*proveo_exec_agent\s+\S`)

var agentEntrypoints = []string{
	"defs/cecli/entrypoint.sh",
	"defs/opencode/entrypoint.sh",
	"defs/cursor/entrypoint.sh",
	"defs/claudecode/mcp/entrypoint.sh",
}

// line reintroduces this silently, and the symptom appears three layers away as
// (_spec/defs/cursor/cursor-paradigm.puml). A guarded default launch beside a
func TestAgentEntrypointsDisambiguateTheirArguments(t *testing.T) {
	t.Parallel()
	for _, rel := range agentEntrypoints {
		body := readRepoFile(t, rel)
		if !calls.MatchString(instructionsOnly(body)) {
			t.Errorf("%s launches an agent without proveo_exec_agent — an sbx CMD arrives as \"$@\" "+
				"and is handed to the agent as a positional, which it reads as a prompt", rel)
		}
	}
}
