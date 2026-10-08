// SPEC: _spec/internal/sbx/clone-workspace.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type seedRun struct {
	out      string
	rc       int
	refused  string
	released bool
}

func runSeedShares(t *testing.T, workdir, shares string) seedRun {
	t.Helper()
	bash := bashOrSkip(t)
	shm := t.TempDir()
	refused, marker := filepath.Join(shm, "refused"), filepath.Join(shm, "released")
	script := `source "$1/packages/lib/entrypoint-lib.sh"
_proveo_agent_home() { return 0; }
proveo_seed claudecode
echo "rc=$?"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_WORKDIR="+workdir, "PROVEO_SHARES="+shares,
		"PROVEO_SEED_REFUSED="+refused, "PROVEO_INSTRUCTIONS_MARKER="+marker,
		"PROVEO_INSTRUCTIONS_DEFAULTS="+filepath.Join(shm, "absent"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seed script failed: %v\n%s", err, out)
	}
	r := seedRun{out: string(out), rc: -1}
	if i := strings.LastIndex(r.out, "rc="); i >= 0 && strings.TrimSpace(r.out[i+3:]) == "0" {
		r.rc = 0
	} else if i >= 0 {
		r.rc = 1
	}
	if b, err := os.ReadFile(refused); err == nil {
		r.refused = strings.TrimSpace(string(b))
	}
	_, err = os.Stat(marker)
	r.released = err == nil
	return r
}

func shareWorkdir(t *testing.T) string {
	t.Helper()
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, "_spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, ".env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestSeedReleasesTheAgentOnlyWhenSpecAndEnvAreShared(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes through any mode bit")
	}
	for _, tc := range []struct {
		name, shares, refused string
		breakIt               func(t *testing.T, wd string)
	}{
		{name: "both present and writable", shares: "_spec=rw|.env=rw"},
		{name: "no shares declared", shares: ""},
		{
			name: "_spec absent", shares: "_spec=rw|.env=rw", refused: "_spec is missing",
			breakIt: func(t *testing.T, wd string) { _ = os.RemoveAll(filepath.Join(wd, "_spec")) },
		},
		{
			name: ".env a dangling link", shares: "_spec=rw|.env=rw", refused: ".env is missing",
			breakIt: func(t *testing.T, wd string) {
				_ = os.Remove(filepath.Join(wd, ".env"))
				if err := os.Symlink(filepath.Join(wd, "nowhere"), filepath.Join(wd, ".env")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: ".env read-only where edits must replicate", shares: "_spec=rw|.env=rw",
			refused: ".env is read-only, so edits cannot reach the host",
			breakIt: func(t *testing.T, wd string) { _ = os.Chmod(filepath.Join(wd, ".env"), 0o400) },
		},
		{
			name: ".env read-only under a read-only workspace", shares: "_spec=ro|.env=ro",
			breakIt: func(t *testing.T, wd string) { _ = os.Chmod(filepath.Join(wd, ".env"), 0o400) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wd := shareWorkdir(t)
			if tc.breakIt != nil {
				tc.breakIt(t, wd)
			}
			r := runSeedShares(t, wd, tc.shares)
			if tc.refused == "" {
				if r.rc != 0 || !r.released || r.refused != "" {
					t.Errorf("rc=%d released=%v refused=%q; want the agent released\n%s", r.rc, r.released, r.refused, r.out)
				}
				return
			}
			if r.rc == 0 || r.released {
				t.Errorf("rc=%d released=%v; a missing share must hold the agent back\n%s", r.rc, r.released, r.out)
			}
			if !strings.Contains(r.refused, tc.refused) {
				t.Errorf("refusal %q lacks %q", r.refused, tc.refused)
			}
		})
	}
}

func TestAwaitSeedRefusesTheAgentButNotTheShell(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	refused := filepath.Join(t.TempDir(), "refused")
	if err := os.WriteFile(refused, []byte("_spec is missing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "SANDBOX_VM_ID=vm", "PROVEO_WORKDIR="+wd, "PROVEO_SEED_REFUSED="+refused,
		"PROVEO_INSTRUCTIONS_MARKER="+filepath.Join(wd, "never"), "PROVEO_INSTRUCTIONS_WAIT=1")
	script := filepath.Join(repoRoot(t), "packages", "lib", "proveo-await-seed")

	agent := exec.Command("sh", script, "echo", "LAUNCHED")
	agent.Dir, agent.Env = wd, env
	out, err := agent.CombinedOutput()
	if err == nil || strings.Contains(string(out), "LAUNCHED") || !strings.Contains(string(out), "_spec is missing") {
		t.Errorf("an agent launch after a refused seed: err=%v\n%s", err, out)
	}

	shell := exec.Command("sh", script, "--cwd")
	shell.Dir, shell.Env = wd, env
	if out, err := shell.Output(); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf("the shell's --cwd must still answer so the operator can look around: err=%v out=%q", err, out)
	}
}

func TestCloneBaseIsRecordedOncePerClone(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	clone, _ := cloneWithBranch(t)
	run := func(vars ...string) {
		cmd := exec.Command(bash, "-c", `source "$1/packages/lib/entrypoint-lib.sh"; proveo_clone_base`, "bash", repoRoot(t))
		cmd.Env = append(append(os.Environ(), "PROVEO_WORKDIR="+clone), vars...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("proveo_clone_base: %v\n%s", err, out)
		}
	}
	base := filepath.Join(gitIn(t, clone, "rev-parse", "--absolute-git-dir"), "proveo-clone-base")
	run()
	if _, err := os.Stat(base); err == nil {
		t.Fatal("a mounted checkout must never get a clone base written into its .git")
	}
	start := gitIn(t, clone, "rev-parse", "HEAD")
	run("PROVEO_CLONE_WORKSPACE=1")
	if err := os.WriteFile(filepath.Join(clone, "c"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", "c")
	gitIn(t, clone, "commit", "-qm", "c")
	run("PROVEO_CLONE_WORKSPACE=1")
	if b, _ := os.ReadFile(base); strings.TrimSpace(string(b)) != start {
		t.Errorf("base = %q, want the boot's HEAD %s — a reboot must not move it past unlifted work", b, start)
	}
}
