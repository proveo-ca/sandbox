//go:build !windows

// SPEC: _spec/internal/results/result-store.puml
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// keptEntry is the store's entry for sandbox sb, or nil.
func keptEntry(dir string) *results.Entry {
	for _, e := range results.List(dir) {
		if e.Kind == results.Kept && e.Key == "sb" {
			return &e
		}
	}
	return nil
}

func TestTeardownWritesAheadThenSettlesTheKeptEntry(t *testing.T) {
	for _, c := range []struct {
		name    string
		failed  bool
		wantWhy string
	}{
		{"a normal exit removes the sandbox and forgets it", false, ""},
		{"a failed run keeps it and says why", true, "the run failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			captureSteps(t)
			td, _, _ := quietTeardown(t, c.failed)
			td.in.ResultsDir = results.Dir(t.TempDir())
			td.in.Sid, td.in.RepoRoot = "proveo-1-2", ""
			var during *results.Entry
			td.commitHome = func() error { during = keptEntry(td.in.ResultsDir); return nil }
			td.run(context.Background())
			if during == nil || !during.Pending || during.Sid != "proveo-1-2" {
				t.Errorf("no write-ahead entry while teardown ran: %+v", during)
			}
			after := keptEntry(td.in.ResultsDir)
			switch {
			case c.wantWhy == "" && after != nil:
				t.Errorf("a removed sandbox is still recorded: %+v", after)
			case c.wantWhy != "" && (after == nil || after.Pending || after.Why != c.wantWhy):
				t.Errorf("entry = %+v, want a settled one saying %q", after, c.wantWhy)
			}
		})
	}
}

func TestAStoppedTeardownRecordsTheSandboxItLeft(t *testing.T) {
	td, term, _ := liveTeardown(t, "sleep 30")
	td.in.ResultsDir = results.Dir(t.TempDir())
	td.in.Sid = "proveo-1-2"
	done := runWatched(t, td)
	term.await(t, stepRemove+" — up to")
	signalSelf(t, syscall.SIGINT)
	term.await(t, "Ctrl+C again leaves now")
	signalSelf(t, syscall.SIGINT)
	awaitEnd(t, done, "the second interrupt")
	got := keptEntry(td.in.ResultsDir)
	if got == nil || got.Pending || got.Why != "teardown stopped by SIGINT at "+stepRemove {
		t.Errorf("entry = %+v, want the sandbox recorded as left by the stop", got)
	}
}

func TestAStrandedCarryKeepsAnEntryNamingTheWork(t *testing.T) {
	captureSteps(t)
	td, _, _ := quietTeardown(t, false)
	repo := t.TempDir()
	td.in.ResultsDir = results.Dir(t.TempDir())
	td.in.Clone, td.in.RepoRoot, td.in.Sid = true, repo, "proveo-1-2"
	td.run(context.Background())
	got := keptEntry(td.in.ResultsDir)
	if got == nil || !strings.Contains(got.Why, "still only in it") || got.Repo != results.Canonical(repo) {
		t.Errorf("entry = %+v, want one that says the work is still in the sandbox", got)
	}
}

func TestTheCarryRecordsEachRefThatHoldsWorkHEADLacks(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base")
	const sid = "proveo-1-2"
	git("update-ref", sbx.CloneRefs(sid)+"/main", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "the agent's work")
	git("update-ref", sbx.CloneRefs(sid)+"/feat", "HEAD")
	git("reset", "-q", "--hard", "HEAD~1")

	_, _, _ = quietTeardown(t, false)
	in := Input{RepoRoot: repo, Sid: sid, ResultsDir: results.Dir(t.TempDir())}
	reportCloneRefs(in, sbx.RunConfig{Name: "sb"}, "", nil)
	var carried *results.Entry
	for _, e := range results.List(in.ResultsDir) {
		if e.Kind == results.Carried && e.Key == sid {
			carried = &e
		}
	}
	if carried == nil || len(carried.Refs) != 1 || carried.Refs[0].Lead != 1 || !strings.HasSuffix(carried.Refs[0].Name, "/feat") {
		t.Fatalf("carried entry = %+v, want the one ref that is one commit ahead", carried)
	}
	if got := results.Summary(results.List(in.ResultsDir), repo); got != "1 to adopt" {
		t.Errorf("Summary = %q", got)
	}

	git("merge", "-q", "--ff-only", sbx.CloneRefs(sid)+"/feat")
	reportCloneRefs(in, sbx.RunConfig{Name: "sb"}, "", nil)
	if left := results.List(in.ResultsDir); len(left) != 0 {
		t.Errorf("a session whose refs HEAD now holds is still recorded: %+v", left)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
}
