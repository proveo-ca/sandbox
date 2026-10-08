// SPEC: _spec/_paradigms/credential-boundary.puml, _spec/internal/sbx/state-sync.puml
package contract_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOpenCodeCredentialSnapshotFixtures(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	cmd := exec.Command(python, "-B", filepath.Join(repoRoot(t), "packages", "lib", "test_opencode_credentials.py"), "-v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode credential snapshot regression failed: %v\n%s", err, out)
	}
}
