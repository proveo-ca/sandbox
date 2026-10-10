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
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode seed progress: %v\n%s", err, out)
	}
}

func TestOpenCodeImageEnablesSeedProgressOnlyForOpenCode(t *testing.T) {
	t.Parallel()
	dockerfile := readRepoFile(t, "defs/opencode/Dockerfile")
	for _, required := range []string{
		"PROVEO_SEED_PROGRESS=1",
		"COPY packages/lib/opencode_startup.py /usr/local/bin/opencode_startup.py",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("OpenCode image lacks %q", required)
		}
	}
	for _, path := range []string{"defs/claudecode/mcp/Dockerfile", "defs/codex/Dockerfile", "defs/cursor/Dockerfile", "defs/cecli/Dockerfile"} {
		if strings.Contains(readRepoFile(t, path), "PROVEO_SEED_PROGRESS=1") {
			t.Errorf("OpenCode-only progress enabled in %s", path)
		}
	}
}
