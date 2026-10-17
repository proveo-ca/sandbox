// SPEC: _spec/internal/sbx/host-android-adb.puml
package contract_test

import (
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
