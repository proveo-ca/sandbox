// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSharesNameWhatTheSeedMustFind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		spec, env bool
		clone, ro bool
		staged    bool
		want      string
	}{
		{name: "clone with both", spec: true, clone: true, staged: true, want: "_spec=rw|.env=rw"},
		{name: "clone without a host .env", spec: true, clone: true, want: "_spec=rw"},
		{name: "mounted checkout", spec: true, env: true, want: "_spec=rw|.env=rw"},
		{name: "read-only mounted checkout", spec: true, env: true, ro: true, want: "_spec=ro|.env=ro"},
		{name: "a clone is writable whatever the mount says", spec: true, clone: true, staged: true, ro: true, want: "_spec=rw|.env=rw"},
		{name: "neither on the host", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wd, _ := filepath.EvalSymlinks(t.TempDir())
			if tc.spec {
				writeTestFile(t, filepath.Join(wd, "_spec", "a.puml"), "@startuml\n@enduml\n")
			}
			if tc.env {
				writeTestFile(t, filepath.Join(wd, ".env"), "A=1\n")
			}
			in := Input{Clone: tc.clone, RepoRoot: wd}
			if tc.staged {
				in.CloneEnv = filepath.Join(t.TempDir(), ".env")
			}
			if got := shares(in, []sbx.Mount{{Host: wd, ReadOnly: tc.ro}}); got != tc.want {
				t.Errorf("shares = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWriteBackCloneEnvMergesTheClonesEditsHome(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	host := filepath.Join(dir, "repo", ".env")
	in := Input{
		CloneEnv:      filepath.Join(dir, "state", "project-env", ".env"),
		CloneEnvBase:  filepath.Join(dir, "state", "project-env.base"),
		CloneEnvHost:  host,
		CloneEnvStrip: []string{"ANTHROPIC_API_KEY"},
	}
	writeTestFile(t, host, "DB=x\nANTHROPIC_API_KEY=sk\n")
	if err := os.Chmod(host, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, in.CloneEnv, "DB=x\n")
	writeTestFile(t, in.CloneEnvBase, "DB=x\n")

	writeBackCloneEnv(in, func() ([]byte, error) { return []byte("DB=y\nNEW=1\n"), nil })
	if got := readTestFile(t, host); got != "DB=y\nANTHROPIC_API_KEY=sk\nNEW=1\n" {
		t.Errorf("host .env = %q; the clone's edits must land and the brokered key must stay", got)
	}
	if fi, _ := os.Stat(host); fi.Mode().Perm() != 0o600 {
		t.Errorf("host .env mode %v; the write-back must keep the operator's mode", fi.Mode().Perm())
	}
	if got := readTestFile(t, in.CloneEnv); got != "DB=y\nNEW=1\n" {
		t.Errorf("staged copy = %q; it must hold the clone's version for a conflict to point at", got)
	}
}

func TestWriteBackCloneEnvFallsBackToTheStagedCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := Input{
		CloneEnv:     filepath.Join(dir, "staged", ".env"),
		CloneEnvBase: filepath.Join(dir, "base"),
		CloneEnvHost: filepath.Join(dir, "host.env"),
	}
	writeTestFile(t, in.CloneEnvHost, "A=1\n")
	writeTestFile(t, in.CloneEnvBase, "A=1\n")
	writeTestFile(t, in.CloneEnv, "A=2\n")
	writeBackCloneEnv(in, func() ([]byte, error) { return nil, errors.New("sandbox gone") })
	if got := readTestFile(t, in.CloneEnvHost); got != "A=2\n" {
		t.Errorf("host .env = %q; a removed sandbox still left its edit in the rw-mounted staged copy", got)
	}
}

func TestWriteBackFromAWorktreeLandsInTheMonorepoEnvEveryWorktreeLinks(t *testing.T) {
	t.Parallel()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	mono := filepath.Join(root, "monorepo", ".env")
	writeTestFile(t, mono, "DB=x\n")
	var trees []string
	for _, wt := range []string{"dev", "main"} {
		dir := filepath.Join(root, "worktrees", wt)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "monorepo", ".env"), filepath.Join(dir, ".env")); err != nil {
			t.Fatal(err)
		}
		trees = append(trees, dir)
	}
	host := workspace.ProjectEnvFile(trees[0], trees[0])
	if host != mono {
		t.Fatalf("staging source = %q, want the monorepo's own file %s", host, mono)
	}
	state := t.TempDir()
	in := Input{CloneEnv: filepath.Join(state, "project-env", ".env"), CloneEnvBase: filepath.Join(state, "base"), CloneEnvHost: host}
	writeTestFile(t, in.CloneEnv, "DB=x\n")
	writeTestFile(t, in.CloneEnvBase, "DB=x\n")
	writeBackCloneEnv(in, func() ([]byte, error) { return []byte("DB=y\n"), nil })
	for _, dir := range trees {
		link := filepath.Join(dir, ".env")
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is no longer a symlink (%v); the write-back must replace the target, not the link", link, err)
		}
		if got := readTestFile(t, link); got != "DB=y\n" {
			t.Errorf("%s reads %q; the edit must cascade to every worktree through the monorepo file", link, got)
		}
	}
}

// specRepo is a host repository whose _spec the clone edits on a side ref.
func specRepo(t *testing.T) (repo, base, head string) {
	t.Helper()
	repo, _ = filepath.EvalSymlinks(t.TempDir())
	gitT(t, repo, "init", "-q")
	for _, f := range []string{"keep.md", "edit.md", "gone.md", "both.md"} {
		writeTestFile(t, filepath.Join(repo, "_spec", f), f+" v1\n")
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "base")
	base = gitT(t, repo, "rev-parse", "HEAD")

	gitT(t, repo, "checkout", "-q", "-b", "clone")
	writeTestFile(t, filepath.Join(repo, "_spec", "edit.md"), "edit.md clone\n")
	writeTestFile(t, filepath.Join(repo, "_spec", "both.md"), "both.md clone\n")
	writeTestFile(t, filepath.Join(repo, "_spec", "new.md"), "new.md clone\n")
	writeTestFile(t, filepath.Join(repo, "src.go"), "package x\n")
	gitT(t, repo, "rm", "-q", "_spec/gone.md")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "clone")
	head = gitT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "update-ref", "refs/proveo/s/clone", head)
	gitT(t, repo, "checkout", "-q", "main")
	gitT(t, repo, "branch", "-q", "-D", "clone")
	return repo, base, head
}

func TestLiftClonedSpecRestoresTheClonesEditsAndKeepsTheHosts(t *testing.T) {
	t.Parallel()
	repo, base, head := specRepo(t)
	writeTestFile(t, filepath.Join(repo, "_spec", "both.md"), "both.md host\n")

	var rebased string
	liftClonedSpec(Input{Sid: "s"}, repo, func() (string, string, error) { return base, head, nil },
		func(sha string) { rebased = sha })

	for f, want := range map[string]string{
		"keep.md": "keep.md v1\n",
		"edit.md": "edit.md clone\n",
		"new.md":  "new.md clone\n",
		"both.md": "both.md host\n",
	} {
		if got := readTestFile(t, filepath.Join(repo, "_spec", f)); got != want {
			t.Errorf("_spec/%s = %q, want %q", f, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "_spec", "gone.md")); !os.IsNotExist(err) {
		t.Errorf("_spec/gone.md survived; the clone deleted it (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "src.go")); !os.IsNotExist(err) {
		t.Error("the lift reached outside _spec; source comes home as refs only")
	}
	if staged := gitT(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("the lift staged %q; it must touch the worktree only", staged)
	}
	if rebased != head {
		t.Errorf("rebased to %q, want %s so the next run diffs from here", rebased, head)
	}
}

func TestLiftClonedSpecLeavesASymlinkedSpecAlone(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitT(t, repo, "init", "-q")
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, "_spec")); err != nil {
		t.Fatal(err)
	}
	called := false
	liftClonedSpec(Input{}, repo, func() (string, string, error) { called = true; return "", "", nil }, func(string) {})
	if called {
		t.Error("a symlinked _spec is relinked live; asking the clone for a span is wasted work")
	}
}

func TestCloneRebaseArgsRefusesANonCommit(t *testing.T) {
	t.Parallel()
	if args := sbx.CloneRebaseArgs("box", "/w", "abc; rm -rf /"); args != nil {
		t.Errorf("a non-hex sha reached a shell script: %q", args)
	}
}
