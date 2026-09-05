// SPEC: _spec/internal/sbx/sbx-kit-contract.puml, _spec/components.puml
//
// SPEC: _spec/internal/sbx/sbx-kit-contract.puml, _spec/components.puml
package sbx

import "sort"

var builtinAgent = map[string]string{
	"claudecode": "claude",
	"cursor":     "cursor",
	"opencode":   "opencode",
}

// ShellAgent is sbx's own shell agent, and it carries two jobs.
const ShellAgent = "shell"

func AgentFor(target string) (agent string, command []string) {
	if a := builtinAgent[target]; a != "" {
		return a, nil
	}
	if target == "" {
		return "", nil
	}
	return ShellAgent, []string{target}
}

func BuiltinAgent(target string) string { return builtinAgent[target] }

func SbxTargets() []string {
	out := make([]string, 0, len(builtinAgent))
	for k := range builtinAgent {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
