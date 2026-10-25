// SPEC: _spec/internal/sbx/sbx-kit-contract.puml, _spec/components.puml, _spec/_paradigms/capability-ladder.puml, _spec/_experiments/sbx-kit-capabilities.puml
package sbx

import (
	"sort"
	"strings"
)

var builtinAgent = map[string]string{
	"claudecode": "claude",
	"codex":      "codex",
	"cursor":     "cursor",
	"opencode":   "opencode",
}

// ShellLaunch is how a def with NO built-in sbx agent gets launched, and the
// leading "-c" is the whole point of it.
func ShellLaunch(target string, extra []string) []string {
	if target == "" {
		return nil
	}
	script := strings.Join(append(defEntrypoint(target), "exec "+shellQuote(target)+` "$@"`), "; ")
	// bash -c reads the word after the script as $0, so a placeholder has to sit
	// there or the first real argument is swallowed.
	return append([]string{"-c", script, "proveo-" + target}, extra...)
}

// defEntrypoint execs the def's entrypoint: <target>-entrypoint on PATH, else /entrypoint.sh.
// SPEC: _spec/internal/sbx/startup-dispatch.puml
func defEntrypoint(target string) []string {
	ep := shellQuote(target + "-entrypoint")
	return []string{
		"if command -v -- " + ep + " >/dev/null 2>&1; then exec " + ep + ` "$@"; fi`,
		`if [ -x /entrypoint.sh ]; then exec /entrypoint.sh "$@"; fi`,
	}
}

// IsShellLaunch reports whether cmd is a launch ShellLaunch built, which is what
// lets callers tell "proveo chose this" from "the user passed a bare command".
func IsShellLaunch(cmd []string) bool { return len(cmd) > 0 && cmd[0] == "-c" }

// shellQuote makes one POSIX single-quoted word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
