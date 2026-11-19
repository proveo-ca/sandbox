// SPEC: _spec/internal/cdn/distribution-update.puml
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/ui"
)

const asset = "proveo-linux-amd64"

// hermeticEnv drops GIT_*, HOME and XDG_CONFIG_HOME, then pins a throwaway HOME, no git config and one identity.
func hermeticEnv(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
}

func run(t *testing.T, r Repo, args ...string) string {
	t.Helper()
	out, err := r.git(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func manifestJSON(version, sum string) string {
	return `{"version": "` + version + `", "checksums": {"` + asset + `": "` + sum + `"}}` + "\n"
}

// newRepo is a clone of a bare origin whose main holds v0.3.0 and one commit after it.
func newRepo(t *testing.T) (work, origin Repo) {
	t.Helper()
	dir := t.TempDir()
	env := hermeticEnv(t)
	origin = Repo{Root: filepath.Join(dir, "origin.git"), Env: env}
	cmd := exec.Command("git", "init", "-q", "--bare", "-b", Branch, origin.Root)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	work = Repo{Root: filepath.Join(dir, "work"), Env: env}
	cmd = exec.Command("git", "clone", "-q", origin.Root, work.Root)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	write(t, filepath.Join(work.Root, ".gitignore"), binDir+"/\n")
	write(t, filepath.Join(work.Root, LatestJSON), manifestJSON("0.3.0", "00"))
	run(t, work, "add", "-A")
	run(t, work, "commit", "-qm", "v0.3.0")
	run(t, work, "tag", "v0.3.0")
	write(t, filepath.Join(work.Root, "a.go"), "package a\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "-qm", "after v0.3.0")
	run(t, work, "push", "-q", Remote, Branch, "--tags")
	return work, origin
}

// fakes records what Deploy asked of its side effects.
type fakes struct {
	built     []string
	published int
	confirmed int
}

// steps stage a binary and a latest.json naming the tag Build was given.
func (f *fakes) steps(t *testing.T, r Repo) Steps {
	var tag string
	return Steps{
		Ledger:  func() error { return nil },
		Confirm: func(string) bool { f.confirmed++; return true },
		Build: func(tg string) error {
			if dirty, err := r.dirty(); err != nil || len(dirty) > 0 {
				t.Errorf("goreleaser ran on a dirty tree: %q %v", dirty, err)
			}
			tag = tg
			f.built = append(f.built, tg)
			return nil
		},
		Stage: func() error {
			bin := []byte("binary " + tag)
			sum := sha256.Sum256(bin)
			write(t, filepath.Join(r.Root, binDir, asset), string(bin))
			write(t, filepath.Join(r.Root, LatestJSON), manifestJSON(strings.TrimPrefix(tag, "v"), hex.EncodeToString(sum[:])))
			return nil
		},
		Publish: func() error { f.published++; return nil },
	}
}

var quiet = ui.New(io.Discard)

func TestNextTagBumpsOrTakesAnExplicitVersion(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ version, newest, want, err string }{
		{"patch", "v0.3.0", "v0.3.1", ""},
		{"minor", "v0.3.7", "v0.4.0", ""},
		{"major", "v0.3.7", "v1.0.0", ""},
		{"patch", "", "v0.0.1", ""},
		{"v0.3.2", "v0.3.0", "v0.3.2", ""},
		{"v1.0.0", "", "v1.0.0", ""},
		{"v0.3.0", "v0.3.0", "", "does not follow"},
		{"v0.2.9", "v0.3.0", "", "does not follow"},
		{"0.3.2", "v0.3.0", "", "want patch, minor, major or vX.Y.Z"},
		{"v0.3.2-rc1", "v0.3.0", "", "want patch, minor, major or vX.Y.Z"},
	} {
		got, err := NextTag(c.version, c.newest)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("NextTag(%q, %q) = %q, %v; want an error naming %q", c.version, c.newest, got, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("NextTag(%q, %q) = %q, %v; want %q", c.version, c.newest, got, err, c.want)
		}
	}
}

func TestValidVersionTakesABumpOrAnExplicitTag(t *testing.T) {
	t.Parallel()
	for v, ok := range map[string]bool{"patch": true, "minor": true, "major": true, "v0.3.2": true,
		"all": false, "codex": false, "0.3.2": false, "": false, "v0.3": false} {
		if err := ValidVersion(v); (err == nil) != ok {
			t.Errorf("ValidVersion(%q) = %v, want ok=%v", v, err, ok)
		}
	}
}

func TestDeployUsesAReleasePRAndTagsTheMergedCommit(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"merge", "squash", "legacy-squash"} {
		t.Run(method, func(t *testing.T) {
			work, origin := newRepo(t)
			base := run(t, work, "rev-parse", "main")
			tag, branch := "v0.3.1", "release/v0.3.1"
			f := &fakes{}
			st := f.steps(t, work)
			if method == "legacy-squash" {
				if _, err := cut(work, "patch", st, quiet); err != nil {
					t.Fatal(err)
				}
				run(t, work, "checkout", "-B", "main")
				run(t, work, "branch", "-D", branch)
				run(t, work, "tag", tag)
			}
			protectMain(t, origin)
			pull := &pullRequest{}
			st.GitHub = fakeGitHub(t, origin, branch, pull)
			for range 2 {
				if ready, err := Deploy(work, "patch", st, quiet); ready || err != nil {
					t.Fatalf("waiting Deploy = %v, %v", ready, err)
				}
			}
			candidate := run(t, work, "rev-parse", "HEAD")
			if len(f.built) != 1 || f.built[0] != tag || f.published != 0 {
				t.Fatalf("built %q and published %d times while waiting", f.built, f.published)
			}
			if got := run(t, work, "diff-tree", "--no-commit-id", "--name-only", "-r", candidate); got != LatestJSON {
				t.Errorf("release commit changes %q, want %s alone", got, LatestJSON)
			}
			if method != "legacy-squash" && run(t, work, "rev-parse", "main") != base {
				t.Error("local main moved while preparing the PR")
			}
			if run(t, origin, "rev-parse", "main") != base || run(t, origin, "tag", "--list", tag) != "" {
				t.Fatal("remote main moved or the tag leaked before the PR merge")
			}

			head := run(t, origin, "rev-parse", "refs/heads/"+branch)
			pull.State = "MERGED"
			pull.MergeCommit.OID = mergeOnGitHub(t, origin, base, head, method == "merge")
			run(t, origin, "update-ref", "-d", "refs/heads/"+branch)
			publish := st.Publish
			st.Publish = func() error { return errors.New("upload failed") }
			if ready, err := Deploy(work, tag, st, quiet); ready || err == nil || err.Error() != "upload failed" {
				t.Fatalf("failed publish Deploy = %v, %v", ready, err)
			}
			if run(t, origin, "tag", "--list", tag) != "" {
				t.Fatal("remote tag exists after failed publication")
			}
			st.Publish = publish
			for range 2 {
				if ready, err := Deploy(work, tag, st, quiet); !ready || err != nil {
					t.Fatalf("merged Deploy = %v, %v", ready, err)
				}
			}
			if len(f.built) != 1 || f.published != 1 {
				t.Errorf("built %q and published %d times, want once", f.built, f.published)
			}
			for _, repo := range []Repo{work, origin} {
				if got := run(t, repo, "rev-parse", "refs/tags/"+tag); got != pull.MergeCommit.OID {
					t.Errorf("tag points to %s, want merge %s", got, pull.MergeCommit.OID)
				}
			}
		})
	}
}

func TestDeployRejectsUnpublishableMerges(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"differs from the built tree", "not on origin/main", "proveo-linux-amd64 is missing", "already points", "different CLI manifest"} {
		t.Run(failure, func(t *testing.T) {
			work, origin := newRepo(t)
			protectMain(t, origin)
			f := &fakes{}
			st := f.steps(t, work)
			pull := &pullRequest{}
			branch := "release/v0.3.1"
			st.GitHub = fakeGitHub(t, origin, branch, pull)
			if ready, err := Deploy(work, "patch", st, quiet); ready || err != nil {
				t.Fatalf("prepare Deploy = %v, %v", ready, err)
			}
			base := run(t, origin, "rev-parse", "main")
			head := pull.HeadRefOID
			pull.State = "MERGED"
			pull.MergeCommit.OID = mergeOnGitHub(t, origin, base, head, false)
			switch failure {
			case "differs from the built tree":
				run(t, origin, "update-ref", "refs/heads/main", run(t, origin, "commit-tree", "-p", base, "-m", "wrong tree", base+"^{tree}"))
				pull.MergeCommit.OID = run(t, origin, "rev-parse", "main")
			case "not on origin/main":
				pull.MergeCommit.OID = head
				run(t, origin, "update-ref", "refs/heads/main", base)
			case "proveo-linux-amd64 is missing":
				if err := os.Remove(filepath.Join(work.Root, binDir, asset)); err != nil {
					t.Fatal(err)
				}
			case "already points":
				run(t, origin, "tag", "v0.3.1", base)
			case "different CLI manifest":
				run(t, origin, "update-ref", "refs/heads/main", run(t, origin, "commit-tree", "-p", pull.MergeCommit.OID, "-m", "new manifest", base+"^{tree}"))
			}
			ready, err := Deploy(work, "patch", st, quiet)
			if ready || err == nil || !strings.Contains(err.Error(), failure) || f.published != 0 {
				t.Fatalf("Deploy = %v, %v, published %d times; want %q before publish", ready, err, f.published, failure)
			}
		})
	}
}

func protectMain(t *testing.T, origin Repo) {
	t.Helper()
	write(t, filepath.Join(origin.Root, "hooks", "pre-receive"), "#!/bin/sh\nwhile read old new ref; do\n  [ \"$ref\" != refs/heads/main ] || exit 1\ndone\n")
	if err := os.Chmod(filepath.Join(origin.Root, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mergeOnGitHub(t *testing.T, origin Repo, base, head string, merge bool) string {
	t.Helper()
	args := []string{"commit-tree", "-p", base, "-m", "merge release", head + "^{tree}"}
	if merge {
		args = append(args, "-p", head)
	}
	merged := run(t, origin, args...)
	run(t, origin, "update-ref", "refs/heads/main", merged, base)
	return merged
}

func fakeGitHub(t *testing.T, origin Repo, branch string, pull *pullRequest) func(...string) (string, error) {
	t.Helper()
	return func(args ...string) (string, error) {
		switch strings.Join(args[:2], " ") {
		case "repo view":
			return "proveo-ca/cli\n", nil
		case "pr list":
			if !strings.Contains(strings.Join(args, " "), "--repo proveo-ca/cli --base main --head "+branch) {
				t.Fatalf("list PR args: %q", args)
			}
			if pull.State == "" {
				return "[]", nil
			}
			data, err := json.Marshal([]pullRequest{*pull})
			return string(data), err
		case "pr create":
			if pull.State != "" || !strings.Contains(strings.Join(args, " "), "--repo proveo-ca/cli --base main --head "+branch) {
				return "", fmt.Errorf("unexpected PR creation: %q", args)
			}
			pull.State = "OPEN"
			pull.URL = "https://github.com/proveo-ca/cli/pull/1"
			pull.HeadRefOID = run(t, origin, "rev-parse", "refs/heads/"+branch)
			return pull.URL + "\n", nil
		}
		return "", fmt.Errorf("unexpected gh command: %q", args)
	}
}

func TestDeployWritesNothingUntilTheLedgerPassesAndSomeoneConfirms(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		ledger    error
		confirm   bool
		want      string
		confirmed int
	}{
		{"red ledger", errors.New("red"), true, "retirement ledger", 0},
		{"declined", nil, false, "not confirmed", 1},
	} {
		work, _ := newRepo(t)
		before := run(t, work, "rev-parse", "HEAD")
		f := &fakes{}
		st := f.steps(t, work)
		st.Ledger = func() error { return c.ledger }
		st.Confirm = func(string) bool { f.confirmed++; return c.confirm }
		_, err := Deploy(work, "patch", st, quiet)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Deploy = %v, want an error naming %q", c.name, err, c.want)
		}
		if f.confirmed != c.confirmed || len(f.built) != 0 || f.published != 0 {
			t.Errorf("%s: asked %d, built %q, published %d; want asked %d and nothing else", c.name, f.confirmed, f.built, f.published, c.confirmed)
		}
		if got := run(t, work, "rev-parse", "HEAD"); got != before || run(t, work, "tag", "--points-at", "HEAD") != "" {
			t.Errorf("%s: HEAD moved or gained a tag", c.name)
		}
	}
}

func TestCutRefusesWhatItCannotRelease(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		version string
		arrange func(t *testing.T, r Repo)
		want    string
	}{
		{"off main", "patch", func(t *testing.T, r Repo) { run(t, r, "checkout", "-qb", "feature") }, "start a release on main"},
		{"ahead of origin", "patch", func(t *testing.T, r Repo) {
			write(t, filepath.Join(r.Root, "b.go"), "package a\n")
			run(t, r, "add", "-A")
			run(t, r, "commit", "-qm", "unpushed")
		}, "is not origin/main"},
		{"dirty tree", "patch", func(t *testing.T, r Repo) {
			write(t, filepath.Join(r.Root, "scratch.txt"), "x")
		}, "changes outside " + LatestJSON},
		{"tag collision", "v0.3.2", func(t *testing.T, r Repo) { run(t, r, "tag", "v0.3.2", "HEAD~1") }, "does not follow v0.3.2"},
		{"explicit version not above", "v0.2.9", func(*testing.T, Repo) {}, "does not follow v0.3.0"},
	} {
		work, _ := newRepo(t)
		c.arrange(t, work)
		f := &fakes{}
		_, err := cut(work, c.version, f.steps(t, work), quiet)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Cut = %v, want an error naming %q", c.name, err, c.want)
		}
		if len(f.built) != 0 {
			t.Errorf("%s: goreleaser ran for %q", c.name, f.built)
		}
	}
}

func TestCutRestoresASnapshotLatestJSONBeforeBuilding(t *testing.T) {
	t.Parallel()
	work, _ := newRepo(t)
	write(t, filepath.Join(work.Root, LatestJSON), manifestJSON("0.1.6-SNAPSHOT-9ab626af", "11"))
	f := &fakes{}
	tag, err := cut(work, "v0.3.2", f.steps(t, work), quiet)
	if err != nil || tag != "v0.3.2" {
		t.Fatalf("Cut = %q, %v; want v0.3.2", tag, err)
	}
	if dirty, _ := work.dirty(); len(dirty) != 0 {
		t.Errorf("after the cut the tree has changes: %q", dirty)
	}
}

func TestCutResumesAHeadItAlreadyCut(t *testing.T) {
	t.Parallel()
	work, _ := newRepo(t)
	f := &fakes{}
	if _, err := cut(work, "patch", f.steps(t, work), quiet); err != nil {
		t.Fatal(err)
	}
	head := run(t, work, "rev-parse", "HEAD")
	for _, version := range []string{"patch", "v0.3.1"} {
		tag, err := cut(work, version, f.steps(t, work), quiet)
		if err != nil || tag != "v0.3.1" {
			t.Errorf("Cut(%s) on a cut HEAD = %q, %v; want the resume of v0.3.1", version, tag, err)
		}
	}
	if len(f.built) != 1 || run(t, work, "rev-parse", "HEAD") != head {
		t.Errorf("a resume rebuilt (%q) or moved HEAD", f.built)
	}
	if _, err := cut(work, "v0.4.0", f.steps(t, work), quiet); err == nil || !strings.Contains(err.Error(), "for v0.3.1, requested v0.4.0") {
		t.Errorf("Cut(v0.4.0) on a HEAD cut as v0.3.1 = %v, want a refusal naming both", err)
	}
}

func TestPlanNamesEveryStepInOrder(t *testing.T) {
	t.Parallel()
	plan := strings.Join(Plan("v0.3.2"), "\n")
	order := []string{"TestNoRetirementOutlivesOneRelease", "ask:", "git fetch", "GORELEASER_CURRENT_TAG=v0.3.2 goreleaser",
		"go run ./internal/cdn/stage", "git commit", "git push origin HEAD:refs/heads/release/v0.3.2", "gh pr create", "wait for the PR", "wrangler deploy", "git push origin <merged-commit>:refs/tags/v0.3.2"}
	at := 0
	for _, step := range order {
		i := strings.Index(plan[at:], step)
		if i < 0 {
			t.Fatalf("the plan lacks %q after byte %d:\n%s", step, at, plan)
		}
		at += i + len(step)
	}
	if !strings.Contains(strings.Join(Plan("patch"), "\n"), "refs/tags/<patch>") {
		t.Error("a bump plan does not name its tag as a placeholder")
	}
}
