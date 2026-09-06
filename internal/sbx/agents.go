// SPEC: _spec/internal/sbx/sbx-kit-contract.puml, _spec/components.puml
package sbx

import (
	"os"
	"sort"
	"strings"
)

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
	return ShellAgent, ShellLaunch(target, nil)
}

// ShellLaunch is how a def with NO built-in sbx agent gets launched, and the
// leading "-c" is the whole point of it.
//
// sbx's shell agent runs `bash -l`, and documents what it does with the words
// after `--`: a first word that BEGINS WITH A DASH is appended to `bash -l`,
// and a bare word REPLACES the `-l` entirely. proveo used to pass the bare
// target name, so `-- cecli` became `bash cecli` — and `bash <file>` searches
// PATH, opens /opt/cecli/bin/cecli, and reads it AS A SHELL SCRIPT. cecli is a
// Python console script, so bash reached its second line and said
// `import: not found`. Nothing was missing: the shebang was simply never
// consulted, because a script named as bash's argument is not exec'd.
//
// A flag-leading command keeps the login shell AND restores the shebang, since
// `exec` does honour it. The def's own entrypoint is preferred over the bare
// binary: sbx's shell agent replaces the image ENTRYPOINT, so without this the
// harness would start with none of the rule, evidence or house-rule arguments
// the def declares — a green rung that silently dropped the def's contract.
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

// EnvAgentKit opts a def with no built-in sbx agent into being declared as a
// COMPLETE AGENT (`kind: sandbox`) instead of borrowing sbx's `shell`.
//
// It is a gate rather than a switch because nothing about it is measured yet.
// The shell path works — it was fixed and negative-checked — and replacing a
// measured fix with a read-from-the-docs alternative is the move this tree has
// already paid for twice. The ladder can climb both; whichever survives becomes
// the default. SPEC: _spec/_experiments/sbx-kit-capabilities.puml
const EnvAgentKit = "PROVEO_SBX_AGENT_KIT"

func AgentKitEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvAgentKit))) {
	case "on", "1", "yes", "true", "enable", "enabled":
		return true
	}
	return false
}

// DeclaresOwnAgent reports whether THIS run should render a sandbox Kit naming
// its own agent. Built-in targets never do: sbx refuses a Kit that shadows one
// ("built-in agents cannot be overridden by a kit").
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
