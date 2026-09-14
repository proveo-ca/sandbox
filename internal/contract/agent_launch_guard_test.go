// SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/_paradigms/capability-ladder.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// calls matches a line that INVOKES the guard, not one that names it.
var calls = regexp.MustCompile(`(?m)^\s*proveo_exec_agent\s+\S`)

var agentEntrypoints = []string{
	"defs/cecli/entrypoint.sh",
	"defs/codex/entrypoint.sh",
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

// execAgent runs proveo_exec_agent for real, with a fake agent first on PATH, and
// returns what actually got executed. The function ends in `exec`, so the bash it
// runs in IS the process under test — whatever replaces it prints the verdict.
func execAgent(t *testing.T, args ...string) string {
	t.Helper()
	bash := bashOrSkip(t)
	bin := t.TempDir()
	fake := filepath.Join(bin, "codex")
	if err := os.WriteFile(fake, []byte("#!/usr/bin/env bash\nprintf 'AGENT_ARGV=%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"
PATH="$2:$PATH"
shift 2
proveo_exec_agent codex "$@"`
	cmd := exec.Command(bash, append([]string{"-c", script, "bash", repoRoot(t), bin}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo_exec_agent failed: %v\n%s", err, out)
	}
	return string(out)
}

// THE SUBCOMMAND IS NOT A COMMAND. codex's headless form is `codex exec`, and
// `exec` is a bash keyword — so a probe that asks "can the shell run this word"
// says yes and hands the whole argv to `exec`, which has no file to execve. The
// run died before codex was reached, and the agent-evidence dial rewrites every
// verbose headless run into exactly this shape.
// SPEC: _spec/packages/lib/seed-and-launch.puml
func TestASubcommandNamedLikeAShellKeywordStillReachesTheAgent(t *testing.T) {
	t.Parallel()
	out := execAgent(t, "--dangerously-bypass-approvals-and-sandbox", "--", "exec", "--json", "do the thing")
	want := "AGENT_ARGV=--dangerously-bypass-approvals-and-sandbox exec --json do the thing"
	if !strings.Contains(out, want) {
		t.Errorf("`codex exec` did not reach the agent.\ngot:  %s\nwant: %s\n"+
			"the probe must ask for an executable FILE (type -P), not for anything the shell "+
			"can run (command -v) — a keyword in the CMD position is never a launcher command, "+
			"because sbx execve's that word rather than parsing it with a shell",
			strings.TrimSpace(out), want)
	}
}

// The guard on the guard: a launcher that really did supply its own command must
// still win, or "always launch the agent" passes the test above while silently
// breaking every sbx run — the failure this whole function exists to prevent.
func TestARealLauncherCommandStillReplacesTheAgent(t *testing.T) {
	t.Parallel()
	out := execAgent(t, "--dangerously-bypass-approvals-and-sandbox", "--", "bash", "-c", "printf 'LAUNCHER_RAN\\n'")
	if !strings.Contains(out, "LAUNCHER_RAN") || strings.Contains(out, "AGENT_ARGV=") {
		t.Errorf("an sbx CMD no longer replaces the agent (got: %s)\n"+
			"the Kit supplies the whole command in the CMD position; appending it to our own "+
			"launch line hands the agent its own name as a prompt, and the sandbox stops with it",
			strings.TrimSpace(out))
	}
}
