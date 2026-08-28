// SPEC: _spec/internal/sbx/sbx-kit-contract.puml, _spec/components.puml
package sbx

import (
	"os"
	"sort"
	"strings"
)

var builtinAgent = map[string]string{
	"claudecode": "claude",
	"codex":      "codex",
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
	return ShellAgent, ShellLaunch(target, nil)
}

// ShellLaunch is how a def with NO built-in sbx agent gets launched, and the
// leading "-c" is the whole point of it.
// SPEC: _spec/_paradigms/capability-ladder.puml
func ShellLaunch(target string, extra []string) []string {
	if target == "" {
		return nil
	}
	q, ep := shellQuote(target), shellQuote(target+"-entrypoint")
	script := strings.Join([]string{
		"if command -v -- " + ep + " >/dev/null 2>&1; then exec " + ep + ` "$@"; fi`,
		`if [ -x /entrypoint.sh ]; then exec /entrypoint.sh "$@"; fi`,
		"exec " + q + ` "$@"`,
	}, "; ")
	// bash -c reads the word after the script as $0, so a placeholder has to sit
	// there or the first real argument is swallowed.
	return append([]string{"-c", script, "proveo-" + target}, extra...)
}

// IsShellLaunch reports whether cmd is a launch ShellLaunch built, which is what
// lets callers tell "proveo chose this" from "the user passed a bare command".
func IsShellLaunch(cmd []string) bool { return len(cmd) > 0 && cmd[0] == "-c" }

// shellQuote makes one POSIX single-quoted word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func BuiltinAgent(target string) string { return builtinAgent[target] }

// EnvAgentKit is now an OPT-OUT. A def with no built-in sbx agent declares
// itself a COMPLETE AGENT (`kind: sandbox`) rather than borrowing sbx's
// `shell`, and PROVEO_SBX_AGENT_KIT=0 restores the borrowed path.
// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
const EnvAgentKit = "PROVEO_SBX_AGENT_KIT"

func AgentKitEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvAgentKit))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

// DeclaresOwnAgent reports whether THIS run should render a sandbox Kit
// naming its own agent.
func DeclaresOwnAgent(target string) bool {
	return target != "" && BuiltinAgent(target) == "" && AgentKitEnabled()
}

func SbxTargets() []string {
	out := make([]string, 0, len(builtinAgent))
	for k := range builtinAgent {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
