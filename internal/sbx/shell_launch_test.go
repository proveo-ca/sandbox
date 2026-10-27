// SPEC: _spec/_paradigms/capability-ladder.puml
package sbx

import (
	"os/exec"
	"strings"
	"testing"
)

func TestShellLaunchIsFlagLeading(t *testing.T) {
	cmd := ShellLaunch("hermes", nil)
	if len(cmd) == 0 {
		t.Fatal("a bash launch needs a COMMAND — without one bash opens a bare login shell")
	}
	if !strings.HasPrefix(cmd[0], "-") {
		t.Fatalf("command %q is a bare word: sbx would drop `-l` and run `bash %s`, "+
			"reading the launcher AS A SHELL SCRIPT instead of exec'ing it", cmd, cmd[0])
	}
	if cmd[0] != "-c" {
		t.Fatalf("the launch has to be a -c script so the shebang is honoured, got %q", cmd[0])
	}
}

// The shebang is only consulted on exec, so a launch that merely NAMES the
// binary reintroduces the bug in a different spelling.
func TestShellLaunchExecsRatherThanSourcing(t *testing.T) {
	script := ShellLaunch("hermes", nil)[1]
	if !strings.Contains(script, "exec ") {
		t.Fatalf("launch script never execs, so the interpreter line is still ignored: %q", script)
	}
	if !strings.Contains(script, "'hermes-entrypoint'") {
		t.Fatalf("launch skips the def's entrypoint, so the harness starts without the rule and "+
			"evidence arguments the def declares — a green rung that dropped the contract: %q", script)
	}
}

// bash -c takes the word after the script as $0. Without a placeholder the
// first real argument is swallowed, which is silent rather than loud.
func TestShellLaunchKeepsExtraArguments(t *testing.T) {
	cmd := ShellLaunch("hermes", []string{"--version", "--help"})
	if len(cmd) != 5 {
		t.Fatalf("want -c, script, $0, then both extras; got %q", cmd)
	}
	if cmd[2] == "--version" {
		t.Fatalf("no $0 placeholder — bash would consume %q as the shell name", cmd[2])
	}
	if got := cmd[len(cmd)-2:]; got[0] != "--version" || got[1] != "--help" {
		t.Fatalf("extras did not survive: %q", cmd)
	}
}

func TestShellQuoteClosesTheWord(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("quoting would break the script: %q", got)
	}
}

func TestShellJoinRoundTripsThroughAShell(t *testing.T) {
	argv := []string{"create", "-e", "GIT_AUTHOR_NAME=Ada Lovelace", "-e", "PROVEO_CONFIG_DIRS=.claude|.claude|jobs/*/tmp",
		"-e", "Q=it's", "-e", "PLAIN=/a/b:c", "/w"}
	line := ShellJoin(argv)
	if strings.Contains(line, "'PLAIN=/a/b:c'") || strings.Contains(line, "'/w'") {
		t.Errorf("plain words were quoted: %s", line)
	}
	out, err := exec.Command("sh", "-c", "set -- "+line+`; for a in "$@"; do printf '%s\n' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if strings.Join(got, "\x00") != strings.Join(argv, "\x00") {
		t.Errorf("a shell reads %q back as %q", line, got)
	}
}
