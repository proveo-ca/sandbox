// SPEC: _spec/internal/results/result-store.puml
package adopt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// carriedRepo is a repository with a base commit on main and two sessions' refs:
// s1/main at main, s1/feat one commit past it, s2/feat two commits past it.
func carriedRepo(t *testing.T) (repo string, run func(args ...string) string) {
	t.Helper()
	repo = t.TempDir()
	run = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	run("update-ref", RefRoot+"s1/main", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "one")
	run("update-ref", RefRoot+"s1/feat", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "two")
	run("update-ref", RefRoot+"s2/feat", "HEAD")
	run("reset", "-q", "--hard", "HEAD~2")
	return repo, run
}

func TestCarriedListsEachRefWithTheWorkNoBranchHolds(t *testing.T) {
	repo, _ := carriedRepo(t)
	refs, err := Carried(repo)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range refs {
		got[r.Name] = r.Lead
		if !strings.HasPrefix(r.Name, "proveo/"+r.Session+"/") || !strings.HasSuffix(r.Name, r.Branch) {
			t.Errorf("ref %+v does not split into its session and branch", r)
		}
	}
	for name, lead := range map[string]int{"proveo/s1/main": 0, "proveo/s1/feat": 1, "proveo/s2/feat": 2} {
		if got[name] != lead {
			t.Errorf("%s lead = %d, want %d", name, got[name], lead)
		}
	}
	if n := len(Waiting(refs)); n != 2 {
		t.Errorf("%d refs wait to be adopted, want 2", n)
	}
}

func TestPickTakesARefASessionOrAnUnambiguousBranch(t *testing.T) {
	repo, _ := carriedRepo(t)
	refs, _ := Carried(repo)
	for _, c := range []struct{ arg, want string }{
		{"proveo/s1/feat", "proveo/s1/feat"},
		{"refs/proveo/s2/feat", "proveo/s2/feat"},
		{"s1", "proveo/s1/feat"},
		{"s2", "proveo/s2/feat"},
		{"proveo/s1/main", "proveo/s1/main"},
	} {
		got, err := Pick(refs, c.arg)
		if err != nil || got.Name != c.want {
			t.Errorf("Pick(%q) = %q, %v; want %q", c.arg, got.Name, err, c.want)
		}
	}
	if _, err := Pick(refs, "feat"); err == nil || !strings.Contains(err.Error(), "proveo/s1/feat") || !strings.Contains(err.Error(), "proveo/s2/feat") {
		t.Errorf("Pick of a branch two sessions carried = %v, want an error naming both", err)
	}
	if _, err := Pick(refs, "nothing"); err == nil {
		t.Error("Pick matched a name no ref carries")
	}
}

func TestBranchCreatesTheBranchAndMovesNothingElse(t *testing.T) {
	repo, run := carriedRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("edited, not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, status := run("rev-parse", "HEAD"), run("status", "--porcelain")
	refs, _ := Carried(repo)
	feat, _ := Pick(refs, "proveo/s2/feat")

	name, err := Branch(repo, feat, "")
	if err != nil || name != "feat" {
		t.Fatalf("Branch = %q, %v", name, err)
	}
	if got := run("rev-parse", "refs/heads/feat"); got != run("rev-parse", "refs/proveo/s2/feat") {
		t.Error("the new branch does not point at the carried ref")
	}
	if run("rev-parse", "HEAD") != head || run("symbolic-ref", "--short", "HEAD") != "main" {
		t.Error("adopting moved HEAD")
	}
	if got := run("status", "--porcelain"); got != status {
		t.Errorf("adopting changed the index or the files:\n%s\nwas\n%s", got, status)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "tracked.txt")); string(b) != "edited, not committed\n" {
		t.Errorf("adopting rewrote a file with uncommitted edits: %q", b)
	}
	after, _ := Carried(repo)
	if n := len(Waiting(after)); n != 0 {
		t.Errorf("%d refs still wait after the branch holds both commits, want 0: %+v", n, Waiting(after))
	}

	if _, err := Branch(repo, feat, ""); err == nil || !strings.Contains(err.Error(), "--as") {
		t.Errorf("a second branch of the same name = %v, want a refusal that names --as", err)
	}
	if name, err := Branch(repo, feat, "review/feat"); err != nil || name != "review/feat" {
		t.Errorf("Branch --as = %q, %v", name, err)
	}
	if _, err := Branch(repo, feat, "bad name"); err == nil {
		t.Error("Branch accepted a name git refuses")
	}
}
