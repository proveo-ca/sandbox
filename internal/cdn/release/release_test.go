// SPEC: _spec/internal/cdn/distribution-update.puml
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func TestDeployCutsTagsPublishesThenPushes(t *testing.T) {
	t.Parallel()
	work, origin := newRepo(t)
	f := &fakes{}
	if err := Deploy(work, "patch", f.steps(t, work), quiet); err != nil {
		t.Fatal(err)
	}
	if len(f.built) != 1 || f.built[0] != "v0.3.1" || f.published != 1 {
		t.Errorf("built %q and published %d times, want v0.3.1 once", f.built, f.published)
	}
	if got := run(t, work, "log", "-1", "--format=%s"); got != "release: v0.3.1" {
		t.Errorf("HEAD's subject = %q, want release: v0.3.1", got)
	}
	if got := run(t, work, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); got != LatestJSON {
		t.Errorf("the release commit changes %q, want %s alone", got, LatestJSON)
	}
	head := run(t, work, "rev-parse", "HEAD")
	if got := run(t, work, "rev-parse", "v0.3.1^{commit}"); got != head {
		t.Errorf("v0.3.1 points at %s, want the release commit %s", got, head)
	}
	if got := run(t, origin, "rev-parse", Branch); got != head {
		t.Errorf("origin's %s is %s, want the release commit %s", Branch, got, head)
	}
	if got := run(t, origin, "rev-parse", "v0.3.1^{commit}"); got != head {
		t.Errorf("origin's v0.3.1 is %s, want %s", got, head)
	}
	if why := StagedRefusal(work); why != "" {
		t.Errorf("after a cut, StagedRefusal = %q, want none", why)
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
		err := Deploy(work, "patch", st, quiet)
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
		{"off main", "patch", func(t *testing.T, r Repo) { run(t, r, "checkout", "-qb", "feature") }, "cuts main"},
		{"ahead of origin", "patch", func(t *testing.T, r Repo) {
			write(t, filepath.Join(r.Root, "b.go"), "package a\n")
			run(t, r, "add", "-A")
			run(t, r, "commit", "-qm", "unpushed")
		}, "is not origin/main"},
		{"dirty tree", "patch", func(t *testing.T, r Repo) {
			write(t, filepath.Join(r.Root, "scratch.txt"), "x")
		}, "changes outside " + LatestJSON},
		{"hand tag on HEAD", "patch", func(t *testing.T, r Repo) { run(t, r, "tag", "v0.3.2") }, "is no release commit"},
		{"explicit version not above", "v0.2.9", func(*testing.T, Repo) {}, "does not follow v0.3.0"},
	} {
		work, _ := newRepo(t)
		c.arrange(t, work)
		f := &fakes{}
		_, err := Cut(work, c.version, f.steps(t, work), quiet)
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
	tag, err := Cut(work, "v0.3.2", f.steps(t, work), quiet)
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
	if _, err := Cut(work, "patch", f.steps(t, work), quiet); err != nil {
		t.Fatal(err)
	}
	head := run(t, work, "rev-parse", "HEAD")
	for _, version := range []string{"patch", "v0.3.1"} {
		tag, err := Cut(work, version, f.steps(t, work), quiet)
		if err != nil || tag != "v0.3.1" {
			t.Errorf("Cut(%s) on a cut HEAD = %q, %v; want the resume of v0.3.1", version, tag, err)
		}
	}
	if len(f.built) != 1 || run(t, work, "rev-parse", "HEAD") != head {
		t.Errorf("a resume rebuilt (%q) or moved HEAD", f.built)
	}
	if _, err := Cut(work, "v0.4.0", f.steps(t, work), quiet); err == nil || !strings.Contains(err.Error(), "cut as v0.3.1, not v0.4.0") {
		t.Errorf("Cut(v0.4.0) on a HEAD cut as v0.3.1 = %v, want a refusal naming both", err)
	}
	if err := os.Remove(filepath.Join(work.Root, binDir, asset)); err != nil {
		t.Fatal(err)
	}
	if _, err := Cut(work, "patch", f.steps(t, work), quiet); err == nil || !strings.Contains(err.Error(), asset+" is missing") {
		t.Errorf("Cut on a cut HEAD without its binary = %v, want a refusal naming %s", err, asset)
	}
}

func TestStagedRefusalNamesTheFixInOneLine(t *testing.T) {
	t.Parallel()
	work, _ := newRepo(t)
	untagged := StagedRefusal(work)
	for _, want := range []string{"carries no vX.Y.Z tag", "mise run deploy --cli"} {
		if !strings.Contains(untagged, want) {
			t.Errorf("an untagged HEAD refuses with %q, want it to name %q", untagged, want)
		}
	}
	run(t, work, "tag", "nightly")
	if StagedRefusal(work) == "" {
		t.Error("a HEAD tagged nightly passed; only a vX.Y.Z tag ships")
	}
	run(t, work, "tag", "v0.3.1")
	for _, version := range []string{"0.3.1-SNAPSHOT-abc1234", "dev@abc1234", "0.3.2"} {
		write(t, filepath.Join(work.Root, LatestJSON), manifestJSON(version, "00"))
		if got := StagedRefusal(work); !strings.Contains(got, version) || !strings.Contains(got, "git checkout") {
			t.Errorf("a staged %s under v0.3.1 refuses with %q, want one naming it and the restore", version, got)
		}
	}
	write(t, filepath.Join(work.Root, LatestJSON), manifestJSON("0.3.1", "00"))
	if got := StagedRefusal(work); got != "" {
		t.Errorf("a staged 0.3.1 under v0.3.1 refuses with %q, want none", got)
	}
	for _, got := range []string{untagged, StagedRefusal(work)} {
		if strings.Contains(got, "\n") {
			t.Errorf("the refusal spans lines: %q", got)
		}
	}
}

func TestPushRefusesAnUntaggedHead(t *testing.T) {
	t.Parallel()
	work, origin := newRepo(t)
	before := run(t, origin, "rev-parse", Branch)
	if err := Push(work, quiet); err == nil || !strings.Contains(err.Error(), "mise run deploy --cli") {
		t.Errorf("Push on an untagged HEAD = %v, want a refusal naming mise run deploy --cli", err)
	}
	if got := run(t, origin, "rev-parse", Branch); got != before {
		t.Errorf("origin's %s moved to %s", Branch, got)
	}
}

func TestPlanNamesEveryStepInOrder(t *testing.T) {
	t.Parallel()
	plan := strings.Join(Plan("v0.3.2"), "\n")
	order := []string{"TestNoRetirementOutlivesOneRelease", "ask:", "git fetch", "GORELEASER_CURRENT_TAG=v0.3.2 goreleaser",
		"go run ./internal/cdn/stage", "git commit", "git tag v0.3.2", "wrangler deploy", "git push --atomic"}
	at := 0
	for _, step := range order {
		i := strings.Index(plan[at:], step)
		if i < 0 {
			t.Fatalf("the plan lacks %q after byte %d:\n%s", step, at, plan)
		}
		at += i
	}
	if !strings.Contains(strings.Join(Plan("patch"), "\n"), "git tag <patch>") {
		t.Error("a bump plan does not name its tag as a placeholder")
	}
}
