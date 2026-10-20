// SPEC: _spec/packages/lib/opencode-seed-progress.puml
package contract_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeSeedProgressFixtures(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.Command(python, "-B", filepath.Join(repoRoot(t), "packages/lib/test_opencode_startup.py"), "-v")
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode seed progress: %v\n%s", err, out)
	}
}

func TestOpenCodeImageEnablesSeedProgressOnlyForOpenCode(t *testing.T) {
	t.Parallel()
	dockerfile := readRepoFile(t, "defs/opencode/Dockerfile")
	if !strings.Contains(dockerfile, "PROVEO_SEED_PROGRESS=1") {
		t.Error("OpenCode image lacks PROVEO_SEED_PROGRESS=1")
	}
	for _, path := range []string{"defs/claudecode/mcp/Dockerfile", "defs/codex/Dockerfile", "defs/cursor/Dockerfile", "defs/cecli/Dockerfile"} {
		if strings.Contains(readRepoFile(t, path), "PROVEO_SEED_PROGRESS=1") {
			t.Errorf("OpenCode-only progress enabled in %s", path)
		}
	}
}
