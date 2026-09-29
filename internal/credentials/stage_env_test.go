// SPEC: _spec/internal/sbx/clone-workspace.puml
package credentials

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStageProjectEnvDropsOnlyTheBrokeredKeys(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), ".env")
	body := "# db\nDB_URL=postgres://x\nexport ANTHROPIC_API_KEY=sk-ant\nOPENAI_API_KEY = sk-o\nFEATURE=on\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "project-env")
	path, dropped, err := StageProjectEnv(src, dir, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "# db\nDB_URL=postgres://x\nFEATURE=on\n" {
		t.Errorf("staged:\n%s", b)
	}
	if !slices.Equal(dropped, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}) {
		t.Errorf("dropped = %v", dropped)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v; the staged copy still holds project secrets", fi.Mode().Perm())
	}
	if _, kept, _ := StageProjectEnv(src, dir, nil); len(kept) != 0 {
		t.Errorf("forward mode drops nothing, got %v", kept)
	}
}
