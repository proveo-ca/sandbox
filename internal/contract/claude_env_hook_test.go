// SPEC: _spec/internal/sbx/host-android-adb.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func libRun(t *testing.T, home, script string, env ...string) string {
	t.Helper()
	cmd := exec.Command(bashOrSkip(t), "-c", `source "$1/packages/lib/entrypoint-lib.sh"; `+script, "bash", repoRoot(t))
	cmd.Env = append(append(os.Environ(), "PROVEO_HOME=", "HOME="+home), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return string(out)
}

func sessionStartCommands(t *testing.T, home string) []string {
	t.Helper()
	var j struct {
		Hooks struct {
			SessionStart []struct {
				Hooks []struct{ Command string } `json:"hooks"`
			} `json:"SessionStart"`
		} `json:"hooks"`
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range j.Hooks.SessionStart {
		for _, h := range g.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

func TestClaudeEnvHookIsInstalledOnceAndKeepsOtherSettings(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"theme":"light"}`), 0o644)
	libRun(t, home, `proveo_install_claude_env_hook claudecode; proveo_install_claude_env_hook claudecode`)
	cmds := sessionStartCommands(t, home)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "CLAUDE_ENV_FILE") || !strings.Contains(cmds[0], ".proveo-tool-env.sh") {
		t.Errorf("SessionStart = %q; want one hook pointing CLAUDE_ENV_FILE at the toolchain env", cmds)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); !strings.Contains(string(b), `"theme": "light"`) {
		t.Errorf("the operator's theme was lost:\n%s", b)
	}
}

func TestClaudeEnvHookSourcesToolsWrittenAfterLaunch(t *testing.T) {
	home := t.TempDir()
	libRun(t, home, `proveo_install_claude_env_hook claudecode`)
	envFile := filepath.Join(t.TempDir(), "claude-env")
	cmd := exec.Command(bashOrSkip(t), "-c", sessionStartCommands(t, home)[0])
	cmd.Env = append(os.Environ(), "HOME="+home, "CLAUDE_ENV_FILE="+envFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(home, ".proveo-tool-env.sh"), []byte("export LATE_TOOL=gradle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := exec.Command(bashOrSkip(t), "-c", `. "$1"; echo "LATE_TOOL=${LATE_TOOL:-unset}"`, "bash", envFile)
	src.Env = append(os.Environ(), "HOME="+home)
	if out, _ := src.CombinedOutput(); !strings.Contains(string(out), "LATE_TOOL=gradle") {
		t.Errorf("sourcing CLAUDE_ENV_FILE after the toolchain landed ⇒ %q", out)
	}
}

func TestClaudeEnvHookIsClaudeCodeOnly(t *testing.T) {
	home := t.TempDir()
	libRun(t, home, `proveo_install_claude_env_hook opencode`)
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
		t.Error("opencode got a Claude Code settings file")
	}
}

func TestPersistedToolEnvIsAlsoAFile(t *testing.T) {
	home := t.TempDir()
	libRun(t, home, `_PROVEO_TOOL_HOME="$HOME/tools" ANDROID_HOME="$HOME" _proveo_persist_tool_env`)
	b, err := os.ReadFile(filepath.Join(home, ".proveo-tool-env.sh"))
	if err != nil || !strings.Contains(string(b), "MISE_DATA_DIR") || !strings.Contains(string(b), "ANDROID_HOME") {
		t.Errorf("~/.proveo-tool-env.sh = %q, %v; want the same exports as the .bashrc block", b, err)
	}
	rc, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	if !strings.Contains(string(rc), "MISE_DATA_DIR") {
		t.Errorf(".bashrc lost its block:\n%s", rc)
	}
}
