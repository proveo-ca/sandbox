//go:build !windows

// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// fakeSbx puts an sbx on PATH that lists sb as stopped, answers the clone-mode
// probe with mode, exits snapshot from the snapshot, runs bundle for the bundle,
// and logs every call; it returns the log's path. Receipts live beside it.
func fakeSbx(t *testing.T, mode string, snapshot int, bundle string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + calls + "'\n" +
		"case \"$*\" in\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\nsb claude stopped\\n' ;;\n" +
		"  */run/sandbox/source*) echo " + mode + " ;;\n" +
		"  *'git add -A'*) exit " + strconv.Itoa(snapshot) + " ;;\n" +
		"  *'bundle create'*) " + bundle + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	orig := receiptDir
	t.Cleanup(func() { receiptDir = orig })
	receiptDir = func() string { return bin }
	return calls
}

const (
	bundleFails = "echo 'bundle refused' >&2; exit 1"
	bundleEmpty = "exit 4"
)

func callsOf(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestRetiringASandboxKeepsItWhenTheCloneCouldNotBeCarriedHome(t *testing.T) {
	for _, c := range []struct {
		name         string
		guest        string
		receipt      *launchReceipt
		thisRunClone bool
		snapshot     int
		bundle       string
		wantErr      bool
		wantSnapshot bool
		wantRemove   bool
	}{
		{"the bundle failed", "clone", nil, true, 0, bundleFails, true, true, false},
		{"the snapshot failed", "clone", nil, true, 1, bundleEmpty, true, true, false},
		{"the clone held nothing new", "clone", nil, true, 0, bundleEmpty, false, true, true},
		{"a clone sandbox retired by a --clone=false run", "clone", nil, false, 0, bundleFails, true, true, false},
		{"a direct sandbox retired by a clone run", "direct", nil, true, 0, bundleEmpty, false, false, true},
		{"the receipt outranks the guest", "clone", &launchReceipt{Clone: false}, true, 0, bundleFails, false, false, true},
		{"an unreadable clone mode keeps the sandbox", "notice: sbx update available", nil, true, 0, bundleEmpty, true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeSbx(t, c.guest, c.snapshot, c.bundle)
			if c.receipt != nil {
				writeReceipt("sb", *c.receipt)
			}
			repo := t.TempDir()
			in := Input{Clone: c.thisRunClone, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			err := retireSandbox(in, cfg)
			log := callsOf(t, calls)
			if (err != nil) != c.wantErr {
				t.Errorf("retireSandbox(%s) error = %v, want error %v", c.name, err, c.wantErr)
			}
			if got := strings.Contains(log, "git add -A"); got != c.wantSnapshot {
				t.Errorf("retireSandbox(%s) snapshotted = %v, want %v\ncalls:\n%s", c.name, got, c.wantSnapshot, log)
			}
			if got := strings.Contains(log, "rm --force sb"); got != c.wantRemove {
				t.Errorf("retireSandbox(%s) ran sbx rm = %v, want %v\ncalls:\n%s", c.name, got, c.wantRemove, log)
			}
		})
	}
}

func TestTeardownCarriesTheSandboxsOwnCloneModeNotThisRuns(t *testing.T) {
	for _, c := range []struct {
		name         string
		guest        string
		thisRunClone bool
		wantErr      bool
		wantSnapshot bool
	}{
		{"a --clone=false run reattached to a clone", "clone", false, true, true},
		{"a clone run reattached to a direct sandbox", "direct", true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeSbx(t, c.guest, 0, bundleFails)
			repo := t.TempDir()
			in := Input{Clone: c.thisRunClone, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			err := TeardownClone(in, cfg)
			log := callsOf(t, calls)
			if (err != nil) != c.wantErr {
				t.Errorf("TeardownClone(%s) error = %v, want error %v", c.name, err, c.wantErr)
			}
			if got := strings.Contains(log, "git add -A"); got != c.wantSnapshot {
				t.Errorf("TeardownClone(%s) snapshotted = %v, want %v — a direct sandbox's workdir is the operator's checkout\ncalls:\n%s", c.name, got, c.wantSnapshot, log)
			}
		})
	}
}

func TestRetiredWorkLandsBesideThisRunsOwnRefs(t *testing.T) {
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	agent := t.TempDir()
	git(agent, "init", "-q", "-b", "main", ".")
	git(agent, "commit", "-q", "--allow-empty", "-m", "the old run's work")
	bundle := filepath.Join(t.TempDir(), "old.bundle")
	git(agent, "bundle", "create", "-q", bundle, "--branches")
	host := t.TempDir()
	git(host, "init", "-q", "-b", "main", ".")

	fakeSbx(t, "clone", 0, "cat '"+bundle+"'")
	in := Input{Clone: true, RepoRoot: host, Sid: "proveo-1-2"}
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: host}}}
	if err := retireSandbox(in, cfg); err != nil {
		t.Fatalf("retireSandbox = %v", err)
	}
	refs := git(host, "for-each-ref", "--format=%(refname)", "refs/proveo/")
	if refs != "refs/proveo/proveo-1-2-retired/main" {
		t.Errorf("retired work landed at %q, want refs/proveo/proveo-1-2-retired/main — under this run's own refs, its teardown fetch would overwrite it", refs)
	}
}
