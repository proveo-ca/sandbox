// SPEC: _spec/internal/sbx/seed-node-version-abort.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bashOrSkip(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	return bash
}

func TestNodeVersionFileTreatsAbsenceAsSuccess(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	empty := t.TempDir()

	script := `source "$1/packages/lib/entrypoint-lib.sh"
_node_version_file "$2" >/dev/null
echo "rc=$?"`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), empty).CombinedOutput()
	if err != nil {
		t.Fatalf("sourcing failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); !strings.HasSuffix(got, "rc=0") {
		t.Errorf("_node_version_file with no .nvmrc/.node-version: %s, want rc=0\n"+
			"a workspace without a version file is the ORDINARY case, not an error — and the caller "+
			"assigns this into a variable, so a non-zero status aborts proveo_seed under `set -euo pipefail`", got)
	}
}

func TestSeedSurvivesAWorkspaceWithNoNodeVersionPin(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	ws := t.TempDir()
	// package.json with NO engines.node, and no .nvmrc / .node-version. That is the
	// exact shape that aborted every run: engines.node is what short-circuits the
	// `||` on the caller line, so a repo carrying it never reached the bug.
	if err := os.WriteFile(filepath.Join(ws, "package.json"), []byte(`{"name":"p","version":"0.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
want_node="$(_node_json_field "$2/package.json" engines.node)"
[[ -n "$want_node" ]] || want_node="$(_node_version_file "$2")"
echo SEED_REACHED_THE_END`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), ws).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "SEED_REACHED_THE_END") {
		t.Fatalf("the seed aborted on a package.json without engines.node (%v)\n%s\n"+
			"this is entrypoint-lib.sh's node-version lookup: proveo-seed runs under `set -euo pipefail`, "+
			"so a helper that reports \"nothing found\" as non-zero kills the whole startup command — "+
			"the container never starts, or the agent dies mid-session", err, out)
	}
}
