package sbx

import (
	"strings"
	"testing"
)

// SPEC: _spec/_paradigms/capability-ladder.puml
func TestShellAgentCommandIsFlagLeading(t *testing.T) {
	agent, cmd := AgentFor("cecli")
	if agent != ShellAgent {
		t.Fatalf("cecli has no built-in sbx agent, so it must run under %q, got %q", ShellAgent, agent)
	}
	if len(cmd) == 0 {
		t.Fatal("a shell-agent def needs a COMMAND — without one sbx opens a bare login shell")
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
	_, cmd := AgentFor("cecli")
	script := cmd[1]
	if !strings.Contains(script, "exec ") {
		t.Fatalf("launch script never execs, so the interpreter line is still ignored: %q", script)
	}
	if !strings.Contains(script, "'cecli-entrypoint'") {
		t.Fatalf("launch skips the def's entrypoint, so the harness starts without the rule and "+
			"evidence arguments the def declares — a green rung that dropped the contract: %q", script)
	}
}

// bash -c takes the word after the script as $0. Without a placeholder the
// first real argument is swallowed, which is silent rather than loud.
func TestShellLaunchKeepsExtraArguments(t *testing.T) {
	cmd := ShellLaunch("cecli", []string{"--version", "--help"})
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

// Built-in agents carry their own launch; adding a command there is what made
// opencode run `opencode opencode` and exit.
func TestBuiltinAgentsCarryNoCommand(t *testing.T) {
	for _, target := range []string{"claudecode", "cursor", "opencode"} {
		agent, cmd := AgentFor(target)
		if agent == ShellAgent || agent == "" {
			t.Fatalf("%s has a built-in sbx agent, got %q", target, agent)
		}
		if len(cmd) != 0 {
			t.Fatalf("%s: built-in agent already launches itself, extra command %q duplicates it", target, cmd)
		}
	}
}

func TestShellQuoteClosesTheWord(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("quoting would break the script: %q", got)
	}
}
