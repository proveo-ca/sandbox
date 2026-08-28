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

// TestExecRedirectFailureDoesNotEndTheShell pins the measurement that disproved
// C1 in _spec/_plans/restore-green-e2e.puml. That plan claimed a failed `exec`
// redirection is fatal to a non-interactive shell, making the `|| return 0` in
// _proveo_lock_installs dead code, and blamed it for the entrypoint dying. It is
// not: bash only exits there in POSIX mode, which the entrypoint never sets.
//
// The real cause was _node_version_file, above. This test exists so nobody has
// to re-derive the disproof from a trace that ends on `+ exec`.
func TestExecRedirectFailureDoesNotEndTheShell(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	// A DIRECTORY cannot be opened for writing, by root or anyone — unlike a
	// permission bit, which root ignores and which made the first probe void.
	blocked := filepath.Join(t.TempDir(), "install.lock")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	script := `set -euo pipefail
guard() { exec 9>"$1" 2>/dev/null || return 0; return 0; }
guard "$1"
echo SURVIVED`
	out, err := exec.Command(bash, "-c", script, "bash", blocked).CombinedOutput()
	if err != nil {
		t.Fatalf("the shell exited on a failed exec redirect — C1's mechanism would be real "+
			"after all, and _proveo_lock_installs would need its guard rewritten: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "SURVIVED") {
		t.Errorf("expected the guard's `|| return 0` to fire and the shell to continue, got:\n%s", out)
	}
}
