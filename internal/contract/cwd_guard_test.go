// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cwdGuard(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "defs", "claudecode", "mcp", "defaults", "hooks", "cwd-guard.sh")
}

// runGuard feeds the hook a PreToolUse payload with the given cwd, pointing its
// /proc walk at a fake tree (or an empty one), and returns exit code and stderr.
func runGuard(t *testing.T, cwd, proc string) (int, string) {
	t.Helper()
	bash := bashOrSkip(t)
	payload := `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"` + cwd + `","tool_input":{"command":"true"}}`
	cmd := exec.Command(bash, cwdGuard(t))
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(os.Environ(), "PROVEO_PROC="+proc)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	return code, stderr.String()
}

func TestCwdGuardIsSilentOnAHealthyShell(t *testing.T) {
	t.Parallel()
	code, msg := runGuard(t, "/Users/op/repo", t.TempDir())
	if code != 0 || msg != "" {
		t.Errorf("healthy cwd: exit %d, stderr %q — a guard that speaks on a healthy shell is noise", code, msg)
	}
}

func TestCwdGuardNamesAVanishedCwdReportedByClaudeCode(t *testing.T) {
	t.Parallel()
	code, msg := runGuard(t, "/Users/op/repo (deleted)", t.TempDir())
	if code != 2 {
		t.Fatalf("exit %d, want 2 — only exit 2 puts stderr in front of the model", code)
	}
	for _, want := range []string{"/Users/op/repo (deleted)", "virtiofs", "exit 1", "Read, Edit and Write still work", "Do not retry Bash", "--continue", "52747"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestCwdGuardReadsTheProcessTreeWhenTheJSONLooksFine(t *testing.T) {
	t.Parallel()
	// A fake /proc: this test process is the hook's parent, and its cwd link
	// carries the kernel's "(deleted)" suffix. A symlink target is free text, so
	// the suffix can be planted without an unlinked directory.
	proc := t.TempDir()
	mkdirAll := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkdirAll(filepath.Join(proc, "self"))
	me := filepath.Join(proc, itoa(os.Getpid()))
	mkdirAll(me)
	if err := os.Symlink("/Users/op/worktrees/hotfix (deleted)", filepath.Join(me, "cwd")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(me, "status"), []byte("Name:\tgo\nPPid:\t1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, msg := runGuard(t, "/Users/op/worktrees/hotfix", proc)
	if code != 2 || !strings.Contains(msg, "hotfix (deleted)") {
		t.Errorf("exit %d, stderr %q — the process tree is the second witness when the JSON cwd looks intact", code, msg)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The seed registers the guard in the agent's user-level settings, merged and
// idempotent: the file is the operator's, so it gains one hook and keeps the rest.
func TestSeedRegistersTheCwdGuardOnce(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	// Operator content that must survive: a theme, and a hook of their own.
	if err := os.WriteFile(settings, []byte(`{"theme":"dark","hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"echo mine"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// $2 inside a function is the FUNCTION's second argument, so the home is
	// captured into a variable before the override is declared.
	script := `source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
export HOME="$2" PROVEO_CWD_GUARD_HOOK="$3"
_proveo_agent_home() { printf '%s' "$agent_home"; }
proveo_install_claude_hooks claudecode
proveo_install_claude_hooks claudecode
echo DONE`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, cwdGuard(t)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("seed step failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Theme string `json:"theme"`
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("settings.json is not JSON after the merge: %v\n%s", err, b)
	}
	if j.Theme != "dark" {
		t.Errorf("operator's theme was lost: %q", j.Theme)
	}
	guards, mine := 0, 0
	for _, g := range j.Hooks.PreToolUse {
		for _, h := range g.Hooks {
			switch {
			case strings.HasSuffix(h.Command, "cwd-guard.sh") && g.Matcher == "Bash" && h.Type == "command":
				guards++
				if h.Timeout == 0 {
					t.Error("the guard needs a timeout: a hung hook is worse than no hook")
				}
			case h.Command == "echo mine":
				mine++
			}
		}
	}
	if guards != 1 {
		t.Errorf("guard registered %d times after two seeds, want exactly 1:\n%s", guards, b)
	}
	if mine != 1 {
		t.Errorf("operator's own hook did not survive the merge:\n%s", b)
	}
}
