// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceTrustMergesUpstreamsFlagsIntoTheRestoredClaudeJSON(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	home := t.TempDir()
	restored := `{"mcpServers":{"serena":{"command":"serena"}},"projects":{"/w":{"allowedTools":["Bash"]}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(restored), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", `source "$1/packages/lib/entrypoint-lib.sh"; accept_workspace_trust /w`, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "HOME="+home, "PROVEO_HOME=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("accept_workspace_trust: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Bypass     bool                      `json:"bypassPermissionsModeAccepted"`
		Onboarded  bool                      `json:"hasCompletedOnboarding"`
		MCPServers map[string]any            `json:"mcpServers"`
		Projects   map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("~/.claude.json is not JSON: %v\n%s", err, b)
	}
	if !j.Bypass || !j.Onboarded {
		t.Errorf("flags = bypass %v onboarded %v; upstream's skipped hook set both", j.Bypass, j.Onboarded)
	}
	for _, d := range []string{"/", "/w"} {
		if j.Projects[d]["hasTrustDialogAccepted"] != true {
			t.Errorf("project %s not trusted: %s", d, b)
		}
	}
	if j.MCPServers["serena"] == nil || j.Projects["/w"]["allowedTools"] == nil {
		t.Errorf("the merge dropped restored keys (P6's loss): %s", b)
	}
}

func TestTheSeedAcceptsTrustAfterItRestoresConfig(t *testing.T) {
	t.Parallel()
	calls, _, _ := runSeedPhase(t, bashOrSkip(t), nil, "claudecode", "release")
	restore, trust := strings.Index(calls, "CALL proveo_sync_config restore"), strings.Index(calls, "CALL accept_workspace_trust")
	if restore < 0 || trust < restore {
		t.Errorf("restore at %d, trust at %d; trust must merge into the restored ~/.claude.json\n%s", restore, trust, calls)
	}
}
