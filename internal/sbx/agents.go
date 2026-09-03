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
	"cursor":     "cursor",
	"opencode":   "opencode",
}

// ShellAgent is sbx's own shell agent, and it carries two jobs.
//
// The first is `--shell`, which cannot be expressed as a command on this backend:
// launch-shaped work belongs to the built-in agent, so passing "bash" as the
// trailing command left sbx starting the harness's agent and handing our word to
// it as an argument. Selecting the agent sbx ships for the purpose is the only way
// to open a shell here, and "shell" is on its closed list.
//
// The second is every harness sbx has no name for. cecli is aider, and nothing on
// the closed list is aider — but a sandbox is a sandbox, so the harness runs under
// the shell agent with its OWN launch command trailing. See AgentFor.
const ShellAgent = "shell"

// AgentFor resolves how a target starts on this backend: sbx's own agent for it
// when one exists, otherwise the shell agent carrying the harness's launch
// command. The second return is the trailing `-- ...` command, empty for a
// built-in (which owns its own launch).
//
// This is what lets `docker: sbx` mean "runs in a sandbox" for EVERY harness
// rather than only for the ones sbx happens to have a built-in for. Before it,
// a target with no counterpart resolved to the empty agent name, and sbx read the
// first workspace path as an agent — "is not a sandbox or known agent".
//
// The launch command is the target's own name because that is what every harness
// image puts on PATH and what the def's entrypoint execs by default (cecli's
// `CMD ["cecli"]`). Nothing harness-specific is encoded here.
func AgentFor(target string) (agent string, command []string) {
	if a := builtinAgent[target]; a != "" {
		return a, nil
	}
	if target == "" {
		return "", nil
	}
	return ShellAgent, []string{target}
}

// BuiltinAgent returns the sbx agent name for a target, or "" when sbx ships no
// agent of that name. An empty result is not a lookup failure and no longer means
// "docker only": AgentFor puts such a target on the shell agent instead. Callers
// that need the name sbx will actually be given must ask AgentFor.
func BuiltinAgent(target string) string { return builtinAgent[target] }

// SbxTargets lists the targets sbx has a built-in agent for, in a stable order.
func SbxTargets() []string {
	out := make([]string, 0, len(builtinAgent))
	for k := range builtinAgent {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
