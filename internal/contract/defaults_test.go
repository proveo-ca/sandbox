// SPEC: _spec/tests/20-contract.puml, _spec/defs/cursor/cursor-paradigm.puml
package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorShipsNoDenyListDefault(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat(filepath.Join(repoRoot(t), "defs/cursor/defaults/cli-config.json")); err == nil {
		t.Error("defs/cursor/defaults/cli-config.json is back; the cursor kit seeds cli-config.json and proveo keeps no deny list (decision: \"Keep upstream, lose the deny list\")")
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
	if !strings.Contains(string(b), `"stop"`) {
		t.Error("hooks.json must wire stop so a turn persists before idle")
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
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("missing opencode default %s: %v", rel, err)
		}
	}
}

// The claudecode loop rule is what makes a seeded subagent actually run — the
// definitions alone are inert.
func TestClaudeCodeLoopDelegates(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "defs/claudecode/mcp/defaults/AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, need := range []string{
		"Verification Commands",
		"adversarial-reviewer",
		"security-reviewer",
		"architect",
		"monorepo-coordinator",
		"spec-keeper",
	} {
		if !strings.Contains(src, need) {
			t.Errorf("AGENTS.md must reference %q", need)
		}
	}
}

// frontmatterField returns the value of a top-level `key: value` line inside the
// leading YAML frontmatter block.
func frontmatterField(src, key string) (string, bool) {
	rest, ok := strings.CutPrefix(src, "---\n")
	if !ok {
		return "", false
	}
	fm, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return "", false
	}
	for _, line := range strings.Split(fm, "\n") {
		if v, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func TestDeadHostLibsRemoved(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{"defs/lib", "defs/cecli/proveo-lib"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("%s should be deleted (logic lives in Go)", rel)
		}
	}
}
