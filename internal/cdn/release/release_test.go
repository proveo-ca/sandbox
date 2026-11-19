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

	"github.com/proveo-ca/proveo/internal/cdn"
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
	write(t, filepath.Join(work.Root, ".gitignore"), ".cache/\n")
	write(t, filepath.Join(work.Root, publicDir, "cli", "install.sh"), "install v1\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "-qm", "v0.3.0")
	run(t, work, "tag", "v0.3.0")
	write(t, filepath.Join(work.Root, "a.go"), "package a\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "-qm", "after v0.3.0")
	run(t, work, "push", "-q", Remote, Branch, "--tags")
	return work, origin
}

type fakes struct {
	built     []string
	published int
	confirmed int
	live      cdn.Manifest
}

func (f *fakes) steps(t *testing.T, r Repo) Steps {
	var tag string
	return Steps{
		Ledger:  func() error { return nil },
		Confirm: func(string) bool { f.confirmed++; return true },
		Build: func(tg string) error {
			if dirty := run(t, r, "status", "--porcelain"); dirty != "" {
				t.Errorf("build got a dirty tree: %q", dirty)
			}
			tag = tg
			f.built = append(f.built, tg)
			return nil
		},
		Stage: func(path string) error {
			bin := []byte("binary " + tag)
			sum := sha256.Sum256(bin)
			digest := hex.EncodeToString(sum[:])
			write(t, filepath.Join(path, "bin", asset), string(bin))
			write(t, filepath.Join(path, "checksums.txt"), digest+"  "+asset+"\n")
			write(t, filepath.Join(path, "latest.json"), manifestJSON(strings.TrimPrefix(tag, "v"), digest))
			return nil
		},
		Publish: func(path string) error {
			f.published++
			installer, err := os.ReadFile(filepath.Join(path, "cli", "install.sh"))
			if err != nil || string(installer) != "install v1\n" {
				t.Errorf("staged installer = %q, %v", installer, err)
			}
			m, err := readArtifact(path, tag)
			f.live = m
			return err
		},
		Live: func() (cdn.Manifest, error) { return f.live, nil },
		Verify: func(want cdn.Manifest) error {
			if !sameManifest(f.live, want) {
				t.Error("published manifest does not match the candidate")
			}
			return nil
		},
	}
}

var quiet = ui.New(io.Discard)

func TestDeployPublishesAndTagsDetachedMainCommit(t *testing.T) {
	t.Parallel()
	work, origin := newRepo(t)
	base := run(t, origin, "rev-parse", "main")
	run(t, work, "switch", "--detach", base)
	tip := run(t, origin, "commit-tree", base+"^{tree}", "-p", base, "-m", "main advanced after dispatch")
	run(t, origin, "update-ref", "refs/heads/main", tip, base)
	protectMain(t, origin)
	f := &fakes{}
	st := f.steps(t, work)
	verify := st.Verify
	st.Verify = func(want cdn.Manifest) error {
		if got := run(t, origin, "tag", "--list", "v0.3.2"); got != "" {
			t.Error("the release tag appeared before live verification")
		}
		if run(t, origin, "for-each-ref", lockRef) == "" {
			t.Error("the production lock was released before live verification")
		}
		return verify(want)
	}
	for range 2 {
		if err := Deploy(work, "v0.3.2", st, quiet); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.built) != 1 || f.published != 1 {
		t.Errorf("built %q, published %d times", f.built, f.published)
	}
	if got := run(t, origin, "rev-parse", "refs/tags/v0.3.2"); got != base {
		t.Errorf("remote tag points to %s, want %s", got, base)
	}
	if got := run(t, origin, "rev-parse", "main"); got != tip {
		t.Errorf("origin/main moved to %s", got)
	}
	if dirty := run(t, work, "status", "--porcelain"); dirty != "" {
		t.Errorf("deployment dirtied the checkout: %s", dirty)
	}
	if got := run(t, origin, "for-each-ref", lockRef); got != "" {
		t.Errorf("the production lock leaked: %s", got)
	}
}

func TestDeployReusesTheArtifactAfterPartialFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"upload", "verification", "tag push"} {
		t.Run(failure, func(t *testing.T) {
			work, origin := newRepo(t)
			protectMain(t, origin)
			f := &fakes{}
			st := f.steps(t, work)
			first := st
			switch failure {
			case "upload":
				first.Publish = func(string) error { return errors.New("upload refused") }
			case "verification":
				first.Verify = func(cdn.Manifest) error { return errors.New("verification unavailable") }
			case "tag push":
				installHook(t, origin, "#!/bin/sh\nwhile read old new ref; do\n  [ \"$ref\" != refs/tags/v0.3.2 ] || exit 1\ndone\n")
			}
			if err := Deploy(work, "v0.3.2", first, quiet); err == nil {
				t.Fatal("expected a release failure")
			}
			if run(t, origin, "tag", "--list", "v0.3.2") != "" || run(t, origin, "for-each-ref", lockRef) != "" {
				t.Fatal("an incomplete release pushed a tag or left the lock")
			}
			protectMain(t, origin)
			if err := Deploy(work, "v0.3.2", st, quiet); err != nil {
				t.Fatal(err)
			}
			if len(f.built) != 1 || f.published != 1 || run(t, origin, "tag", "--list", "v0.3.2") == "" {
				t.Errorf("retry built %q, published %d times", f.built, f.published)
			}
		})
	}
}

func TestDeploySerializesIndependentCheckouts(t *testing.T) {
	t.Parallel()
	work, origin := newRepo(t)
	other := Repo{Root: filepath.Join(t.TempDir(), "other"), Env: work.Env}
	run(t, origin, "clone", "--quiet", origin.Root, other.Root)
	head := run(t, work, "rev-parse", "HEAD")
	f := &fakes{}
	st := f.steps(t, work)
	unlock, err := work.lock("v0.3.2", head)
	if err != nil {
		t.Fatal(err)
	}
	owner := run(t, origin, "rev-parse", lockRef)
	if err := Deploy(other, "v0.3.2", f.steps(t, other), quiet); err == nil || !strings.Contains(err.Error(), "CLI deploy is locked") {
		t.Fatalf("concurrent Deploy = %v", err)
	}
	if got := run(t, origin, "rev-parse", lockRef); got != owner {
		t.Errorf("competing release changed the lock to %s", got)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := Deploy(work, "v0.3.2", st, quiet); err != nil {
		t.Fatal(err)
	}
	if len(f.built) != 1 || f.published != 1 {
		t.Errorf("built %q, published %d times", f.built, f.published)
	}
}

func TestDeployRefusesUnsafeReleaseState(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"outside origin/main", "clean checkout", "already points", "must follow", "already live", "different checksums", "does not include"} {
		t.Run(failure, func(t *testing.T) {
			work, origin := newRepo(t)
			f := &fakes{}
			base := run(t, work, "rev-parse", "HEAD")
			switch failure {
			case "outside origin/main":
				run(t, work, "commit", "--quiet", "--allow-empty", "-m", "unmerged change")
			case "clean checkout":
				write(t, filepath.Join(work.Root, "scratch.txt"), "x")
			case "already points":
				run(t, origin, "tag", "v0.3.2", base+"^")
			case "must follow":
				run(t, origin, "tag", "v0.3.3", base)
			case "already live":
				f.live = cdn.Manifest{Version: "0.3.3"}
			case "different checksums":
				f.live = cdn.Manifest{Version: "0.3.2", Checksums: map[string]string{asset: "00"}}
			case "does not include":
				run(t, origin, "tag", "v0.3.1", base)
				run(t, work, "switch", "--detach", base+"^")
			}
			if err := Deploy(work, "v0.3.2", f.steps(t, work), quiet); err == nil || !strings.Contains(err.Error(), failure) {
				t.Fatalf("Deploy = %v, want %q", err, failure)
			}
			if f.published != 0 || run(t, origin, "for-each-ref", lockRef) != "" {
				t.Fatalf("failed deployment published %d times or leaked the lock", f.published)
			}
		})
	}
}

func TestDeploySkipsWritesWhenLedgerOrConfirmationFails(t *testing.T) {
	t.Parallel()
	for _, failLedger := range []bool{false, true} {
		work, origin := newRepo(t)
		f := &fakes{}
		st := f.steps(t, work)
		st.Confirm = func(string) bool { f.confirmed++; return false }
		want := "not confirmed"
		if failLedger {
			st.Ledger = func() error { return errors.New("red") }
			want = "retirement ledger"
		}
		err := Deploy(work, "v0.3.2", st, quiet)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Deploy = %v, want %q", err, want)
		}
		if (failLedger && f.confirmed > 0) || len(f.built) > 0 || f.published > 0 || run(t, origin, "for-each-ref", lockRef) != "" {
			t.Error("release ran after a failed gate")
		}
	}
}

func TestPlanNamesEveryStepInOrder(t *testing.T) {
	t.Parallel()
	plan := strings.Join(Plan("v0.3.2"), "\n")
	for _, step := range []string{"TestNoRetirementOutlivesOneRelease", "git fetch", "acquire the shared Git lock",
		"GORELEASER_CURRENT_TAG=v0.3.2 goreleaser", "go run ./internal/cdn/stage", "wrangler deploy",
		"verify the live manifest", "git push origin <commit>:refs/tags/v0.3.2", "release the shared Git lock"} {
		_, rest, ok := strings.Cut(plan, step)
		if !ok {
			t.Fatalf("the plan lacks %q in %s", step, plan)
		}
		plan = rest
	}
}

func protectMain(t *testing.T, origin Repo) {
	t.Helper()
	installHook(t, origin, "#!/bin/sh\nwhile read old new ref; do\n  [ \"$ref\" != refs/heads/main ] || exit 1\ndone\n")
}

func installHook(t *testing.T, origin Repo, script string) {
	t.Helper()
	path := filepath.Join(origin.Root, "hooks", "pre-receive")
	write(t, path, script)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
