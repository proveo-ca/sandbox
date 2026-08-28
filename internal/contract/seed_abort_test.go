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
// C1 in _spec/internal/sbx/seed-node-version-abort.puml. That plan claimed a failed `exec`
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

// TestStderrSurvivesTheInstallLock is the one that matters most for debugging.
//
// _proveo_lock_installs used to take its lock with `exec 9>"$lock" 2>/dev/null`.
// On a bare `exec` — one with no command — the redirections are applied to the
// SHELL and are permanent, so that `2>/dev/null` did not scope stderr to the
// exec: it discarded stderr for the whole entrypoint and for the agent it goes
// on to exec. Every `set -x` trace ended on `+ exec`, which was read for a long
// time as the shell dying there, and every later failure presented as a silent
// death with no message. Both readings were wrong; the output was simply gone.
func TestStderrSurvivesTheInstallLock(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)

	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export HOME="$2"
_proveo_lock_installs || true
echo "AFTER-LOCK" >&2
_proveo_unlock_installs
echo "AFTER-UNLOCK" >&2`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("lock/unlock ended the shell: %v\n%s", err, out)
	}
	for _, want := range []string{"AFTER-LOCK", "AFTER-UNLOCK"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("stderr written after the install lock was discarded (%q missing) — a bare "+
				"`exec` applies its redirections to the shell permanently, so diagnostics for the "+
				"rest of the run go to /dev/null:\n%s", want, out)
		}
	}
}

// TestCommandSubstitutedHelpersTreatAbsenceAsSuccess covers the whole family the
// seed abort belongs to, rather than the one member that happened to break.
//
// Every one of these is assigned as `x="$(helper ...)"` in a shell running under
// `set -euo pipefail`, so ANY non-zero status ends the run. "Nothing found" and
// "the interpreter for this language is not in this image" are both ordinary
// states: cecli ships python and no node, and _node_json_field returned 127.
func TestCommandSubstitutedHelpersTreatAbsenceAsSuccess(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// An EMPTY PATH is the point: these helpers reach for node, go, gh and mise,
	// and the guards must fire on `command -v` rather than on a 127.
	for _, c := range []struct{ name, call string }{
		{"_node_json_field", `_node_json_field "$3/package.json" engines.node`},
		{"_node_version_file", `_node_version_file "$3"`},
		{"_node_nearest_pkg", `_node_nearest_pkg "$3"`},
		{"_go_current_version", `_go_current_version`},
		{"_proveo_github_token", `_proveo_github_token`},
		{"_proveo_agent_home", `_proveo_agent_home`},
	} {
		t.Run(c.name, func(t *testing.T) {
			script := `set -uo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export HOME="$2"
unset GITHUB_TOKEN GH_TOKEN
( PATH=""; v="$(` + c.call + `)"; echo "rc=$?" )`
			out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), t.TempDir(), work).CombinedOutput()
			got := strings.TrimSpace(string(out))
			if err != nil || !strings.HasSuffix(got, "rc=0") {
				t.Errorf("%s with nothing on PATH: %v / %q, want rc=0\n"+
					"an absent interpreter is not a failure, and the caller assigns this in a "+
					"command substitution under `set -e`", c.name, err, got)
			}
		})
	}
}
