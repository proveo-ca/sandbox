// SPEC: _spec/internal/sbx/sbx-kit-contract.puml, _spec/components.puml
package sbx

import "sort"

// builtinAgent maps a proveo target to the sbx agent it runs under.
//
// sbx's agent list is CLOSED — `sbx run --help` prints it, a Kit cannot add to it,
// naming one after a built-in is refused, and publishing registers nothing. A run
// therefore has to name one of sbx's own agents; proveo contributes the image and
// the posture, never the agent identity. Naming an agent of our own is what left
// every run with a skipped binding gate and a session dropped seconds in.
var builtinAgent = map[string]string{
	"claudecode": "claude",
	"codex":      "codex",
	"cursor":     "cursor",
}

// ShellAgent is sbx's own shell agent. `--shell` cannot be expressed as a command
// on this backend: launch-shaped work belongs to the built-in agent, so passing
// "bash" as the trailing command left sbx starting the harness's agent and handing
// our word to it as an argument. Selecting the agent sbx ships for the purpose is
// the only way to open a shell here, and "shell" is on its closed list.
const ShellAgent = "shell"

// BuiltinAgent returns the sbx agent name for a target, or "" when the target has
// no sbx path. An empty result is a decision, not a lookup failure: cecli has no
// counterpart in sbx's list at all, so it belongs on the docker backend.
func BuiltinAgent(target string) string { return builtinAgent[target] }

// SbxTargets lists the targets that can run on this backend, in a stable order.
func SbxTargets() []string {
	out := make([]string, 0, len(builtinAgent))
	for k := range builtinAgent {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
