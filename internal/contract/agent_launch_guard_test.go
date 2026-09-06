// SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/_paradigms/capability-ladder.puml
package contract_test

import (
	"regexp"
	"testing"
)

// calls matches a line that INVOKES the guard, not one that names it.
var calls = regexp.MustCompile(`(?m)^\s*proveo_exec_agent\s+\S`)

// agentEntrypoints are the def entrypoints that LAUNCH AN AGENT. Sidecars and
// the echo test image are absent because they launch no agent and have no "$@"
// to disambiguate.
var agentEntrypoints = []string{
	"defs/opencode/entrypoint.sh",
	"defs/cursor/entrypoint.sh",
	"defs/claudecode/mcp/entrypoint.sh",
}

// "$@" is AMBIGUOUS and the two backends disagree about what it means.
//
//	docker  proveo passes the harness's own flags, which belong AFTER the binary
//	sbx     the built-in agent kit supplies the whole COMMAND in the CMD
//	        position, and proveo's ENTRYPOINT turns it into "$@"
//
// Appending it blindly hands the AGENT NAME to the agent as a positional, which
// every harness reads as a prompt or a path. opencode did exactly that —
// `opencode opencode` — and exited; the sbx session ended with it and the
// sandbox auto-stopped 30s later as "sandbox ... was stopped".
//
// e2e/ladder_test.go pinned it to this layer: rung 0 (stock sbx, stock image)
// held 48s, rung 1 (the proveo image, nothing else) died in 9s. cursor and
// claudecode already routed through proveo_exec_agent and both survived the
// same wait, which is the whole 2x2.
//
// So the guard is a contract, not a convention: a def that grows its own launch
// line reintroduces this silently, and the symptom appears three layers away as
// an infrastructure failure.
//
// It asserts PRESENCE and nothing narrower. A first attempt also rejected any
// bare `exec <agent> "$@"`, which flagged cursor's utility-subcommand
// passthrough — `login`, `logout`, `status` and friends exec straight through
// with none of the autonomy flags, deliberately and by spec
// (_spec/defs/cursor/cursor-paradigm.puml). A guarded default launch beside a
// deliberate passthrough is correct, and a check that cannot tell them apart
// costs more than it catches.
func TestAgentEntrypointsDisambiguateTheirArguments(t *testing.T) {
	t.Parallel()
	for _, rel := range agentEntrypoints {
		body := readRepoFile(t, rel)
		// An INVOCATION, not a mention. Checking for the bare string passed with
		// the call deleted and only the comment left behind — which is exactly the
		// state a careless edit leaves.
		if !calls.MatchString(instructionsOnly(body)) {
			t.Errorf("%s launches an agent without proveo_exec_agent — an sbx CMD arrives as \"$@\" "+
				"and is handed to the agent as a positional, which it reads as a prompt", rel)
		}
	}
}
