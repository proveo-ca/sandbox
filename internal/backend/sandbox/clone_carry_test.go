//go:build !windows

// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// fakeSbx puts an sbx on PATH that lists sb as stopped, runs mode for the
// clone-mode probe, exits snapshot from the snapshot, runs bundle for the bundle,
// and logs every call; it returns the log's path. Receipts live beside it.
func fakeSbx(t *testing.T, mode string, snapshot int, bundle string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + calls + "'\n" +
		"case \"$*\" in\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\nsb claude stopped\\n' ;;\n" +
		"  */run/sandbox/source*) " + mode + " ;;\n" +
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
	guestClone  = "echo clone"
	guestDirect = "echo direct"
)

// gitEnv is the environment minus every GIT_* variable, with no global or system config.
func gitEnv(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1")
}

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
		{"the bundle failed", guestClone, nil, true, 0, bundleFails, true, true, false},
		{"the snapshot failed", guestClone, nil, true, 1, bundleEmpty, true, true, false},
		{"the clone held nothing new", guestClone, nil, true, 0, bundleEmpty, false, true, true},
		{"a clone sandbox retired by a --clone=false run", guestClone, nil, false, 0, bundleFails, true, true, false},
		{"a direct sandbox retired by a clone run", guestDirect, nil, true, 0, bundleEmpty, false, false, true},
		{"a stale clone receipt does not outrank a direct sandbox", guestDirect, &launchReceipt{Clone: true}, true, 0, bundleFails, false, false, true},
		{"an sbx notice on stderr does not hide the mode", "echo 'notice: sbx update available' >&2; echo direct", nil, true, 0, bundleFails, false, false, true},
		{"an unreadable clone mode keeps the sandbox", "echo 'notice: sbx update available'", nil, true, 0, bundleEmpty, true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeSbx(t, c.guest, c.snapshot, c.bundle)
			if c.receipt != nil {
				writeReceipt("sb", *c.receipt)
			}
			repo := t.TempDir()
			in := Input{Clone: c.thisRunClone, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			err := retireSandbox(in, cfg, quietReport)
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

func shrinkGuestDeadlines(t *testing.T, d time.Duration) {
	t.Helper()
	probe, guest := cloneProbeDeadline, guestExecDeadline
	t.Cleanup(func() { cloneProbeDeadline, guestExecDeadline = probe, guest })
	cloneProbeDeadline, guestExecDeadline = d, d
}

func TestAGuestThatNeverAnswersKeepsTheSandboxInsteadOfHangingTheRun(t *testing.T) {
	if cloneProbeDeadline != 36*time.Second || guestExecDeadline != 3*time.Minute {
		t.Errorf("deadlines = %s probe, %s guest exec; decided 36s and 3m", cloneProbeDeadline, guestExecDeadline)
	}
	for _, c := range []struct {
		name   string
		guest  string
		bundle string
	}{
		{"the clone-mode probe hangs", "sleep 30; echo clone", bundleEmpty},
		{"the bundle hangs", guestClone, "sleep 30"},
	} {
		t.Run(c.name, func(t *testing.T) {
			shrinkGuestDeadlines(t, 300*time.Millisecond)
			calls := fakeSbx(t, c.guest, 0, c.bundle)
			repo := t.TempDir()
			in := Input{Clone: true, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			start := time.Now()
			err := retireSandbox(in, cfg, quietReport)
			took := time.Since(start)
			log := callsOf(t, calls)
			if took > 10*time.Second {
				t.Errorf("retireSandbox(%s) took %s; the deadline must end the wait", c.name, took)
			}
			if err == nil {
				t.Errorf("retireSandbox(%s) error = nil, want the sandbox kept", c.name)
			}
			if strings.Contains(log, "rm --force sb") {
				t.Errorf("retireSandbox(%s) ran sbx rm after a guest that never answered\ncalls:\n%s", c.name, log)
			}
		})
	}
}

func TestRetireReportsEachStepOnlyWhenItStarts(t *testing.T) {
	for _, c := range []struct {
		name   string
		bundle string
		want   []string
	}{
		{"a failed carry stops at step 1", bundleFails, []string{"1/3"}},
		{"a carry with nothing new runs all three", bundleEmpty, []string{"1/3", "2/3", "3/3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeSbx(t, guestClone, 0, c.bundle)
			repo := t.TempDir()
			in := Input{Clone: true, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			var steps []string
			_ = retireSandbox(in, cfg, func(f string, a ...any) {
				steps = append(steps, strings.Fields(fmt.Sprintf(f, a...))[0])
			})
			if !slices.Equal(steps, c.want) {
				t.Errorf("retireSandbox(%s) reported steps %v, want %v", c.name, steps, c.want)
			}
		})
	}
}

func TestTeardownCarriesTheSandboxsOwnCloneModeNotThisRuns(t *testing.T) {
	for _, c := range []struct {
		name         string
		guest        string
		thisRunClone bool
		created      bool
		wantErr      bool
		wantSnapshot bool
		wantProbe    bool
	}{
		{"a --clone=false run reattached to a clone", guestClone, false, false, true, true, true},
		{"a clone run reattached to a direct sandbox", guestDirect, true, false, false, false, true},
		{"a sandbox this run created as direct needs no probe", guestClone, false, true, false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeSbx(t, c.guest, 0, bundleFails)
			repo := t.TempDir()
			in := Input{Clone: c.thisRunClone, CloneKnown: c.created, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			err := TeardownClone(in, cfg)
			log := callsOf(t, calls)
			if (err != nil) != c.wantErr {
				t.Errorf("TeardownClone(%s) error = %v, want error %v", c.name, err, c.wantErr)
			}
			if got := strings.Contains(log, "git add -A"); got != c.wantSnapshot {
				t.Errorf("TeardownClone(%s) snapshotted = %v, want %v — a direct sandbox's workdir is the operator's checkout\ncalls:\n%s", c.name, got, c.wantSnapshot, log)
			}
			if got := strings.Contains(log, "/run/sandbox/source"); got != c.wantProbe {
				t.Errorf("TeardownClone(%s) probed the guest = %v, want %v\ncalls:\n%s", c.name, got, c.wantProbe, log)
			}
		})
	}
}

func TestRetiredWorkLandsBesideThisRunsOwnRefs(t *testing.T) {
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Env = gitEnv(t)
		out, err := cmd.CombinedOutput()
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

	fakeSbx(t, guestClone, 0, "cat '"+bundle+"'")
	in := Input{Clone: true, RepoRoot: host, Sid: "proveo-1-2"}
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: host}}}
	if err := retireSandbox(in, cfg, quietReport); err != nil {
		t.Fatalf("retireSandbox = %v", err)
	}
	refs := git(host, "for-each-ref", "--format=%(refname)", "refs/proveo/")
	if refs != "refs/proveo/proveo-1-2-retired/main" {
		t.Errorf("retired work landed at %q, want refs/proveo/proveo-1-2-retired/main — under this run's own refs, its teardown fetch would overwrite it", refs)
	}
}

func TestTheAdoptHintLeadsWithABranchThatHoldsNewWork(t *testing.T) {
	ahead := map[string]bool{"proveo/s/proveo/stash-0": true, "proveo/s/work": true}
	for _, c := range []struct {
		name  string
		names []string
		want  string
	}{
		{"an untouched copy of main loses to the agent's branch", []string{"proveo/s/main", "proveo/s/work"}, "proveo/s/work"},
		{"the agent's branch beats a stash", []string{"proveo/s/proveo/stash-0", "proveo/s/work"}, "proveo/s/work"},
		{"a stash is the lead when it alone holds work", []string{"proveo/s/main", "proveo/s/proveo/stash-0"}, "proveo/s/proveo/stash-0"},
		{"nothing new names the first", []string{"proveo/s/main"}, "proveo/s/main"},
	} {
		if got := adoptLead(c.names, "proveo/s/", func(n string) bool { return ahead[n] }); got != c.want {
			t.Errorf("adoptLead(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}
