// SPEC: _spec/internal/sbx/launch-env.puml, _spec/packages/lib/github-ssh-hosts.puml
package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestGitSafeDirectoryEnvDeclaresOnlyTheRepoRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	got := gitSafeDirectoryEnv(root)
	wantRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		wantRoot = r
	}
	want := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=" + wantRoot,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("gitSafeDirectoryEnv mismatch (-want +got):\n%s", diff)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "insteadOf") {
		t.Errorf("launchEnv must not rewrite git@github.com to HTTPS; SSH host keys are the fix: %q", got)
	}
}

func TestGitSafeDirectoryEnvEmptyRepoRootIsSilent(t *testing.T) {
	t.Parallel()
	if got := gitSafeDirectoryEnv(""); got != nil {
		t.Errorf("empty repoRoot = %q, want nil", got)
	}
}
