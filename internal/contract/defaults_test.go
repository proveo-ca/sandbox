// SPEC: _spec/tests/20-contract.puml, _spec/defs/cursor/cursor-paradigm.puml
package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorDenyBaseline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "defs/cursor/defaults/cli-config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("parse cli-config.json: %v", err)
	}
	has := map[string]bool{}
	for _, d := range cfg.Permissions.Deny {
		has[d] = true
	}
	for _, need := range []string{"Shell(sudo)", "Read(.env*)"} {
		if !has[need] {
			t.Errorf("cli-config deny missing %q; got %v", need, cfg.Permissions.Deny)
		}
	}
}

func TestCursorSubagentsReadonly(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "defs/cursor/defaults/agents")
	for _, name := range []string{"adversarial-reviewer.md", "security-reviewer.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !strings.Contains(string(b), "readonly: true") {
			t.Errorf("%s must declare readonly: true", name)
		}
	}
}

func TestCursorAuditHookFailOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "defs/cursor/defaults/hooks/audit-shell.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `{"permission":"allow"}`) {
		t.Error("audit hook must fail-open with permission allow")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("audit-shell.sh must be executable, mode=%o", fi.Mode())
	}
}

func TestCursorHooksWireShellAudit(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "defs/cursor/defaults/hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "beforeShellExecution") {
		t.Error("hooks.json must wire beforeShellExecution")
	}
}

func TestCursorLoopRule(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "defs/cursor/defaults/rules/proveo-loop.mdc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Verification Commands") {
		t.Error("proveo-loop.mdc must mention Verification Commands")
	}
}

func TestOpenCodeDefaultsExist(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "defs/opencode/defaults")
	for _, rel := range []string{
		"AGENTS.md",
		"opencode.json",
		"agents/spec-keeper.md",
		"agents/adversarial-reviewer.md",
		"agents/security-reviewer.md",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("missing opencode default %s: %v", rel, err)
		}
	}
}

func TestDeadHostLibsRemoved(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"defs/lib/env-mount.sh",
		"defs/lib/git-identity.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("%s should be deleted (logic lives in Go)", rel)
		}
	}
	// detect-verify.sh remains as a thin wrapper to proveo-entrypoint verify.
	b, err := os.ReadFile(filepath.Join(root, "defs/lib/detect-verify.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "proveo-entrypoint verify") {
		t.Error("detect-verify.sh must delegate to proveo-entrypoint verify")
	}
}
