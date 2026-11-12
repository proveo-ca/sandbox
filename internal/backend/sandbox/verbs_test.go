//go:build !windows

// SPEC: _spec/internal/results/result-store.puml
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// listing makes sbx's listing answer rows; ok false is sbx giving none.
func listing(t *testing.T, ok bool, rows ...sbx.Listed) {
	t.Helper()
	prev := listSandboxes
	t.Cleanup(func() { listSandboxes = prev })
	listSandboxes = func() ([]sbx.Listed, bool) { return rows, ok }
}

func keptStore(t *testing.T, names ...string) string {
	t.Helper()
	dir := results.Dir(t.TempDir())
	for _, n := range names {
		if err := results.Put(dir, results.Entry{Kind: results.Kept, Key: n, Why: "the run failed"}); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func keptNames(dir string) string {
	var names []string
	for _, e := range results.List(dir) {
		if e.Kind == results.Kept {
			names = append(names, e.Key)
		}
	}
	return strings.Join(names, ",")
}

// gitRepo is an empty repository, the workspace of a kept clone.
func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return repo
}

// isReported reports whether err is one a printed line already said, so main prints nothing for it.
func isReported(err error) bool {
	var r backend.Reported
	return errors.As(err, &r)
}

func TestRemoveKeptClearsNothingOnAListingItCannotTrust(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	for name, set := range map[string]func(){
		"sbx gave no listing": func() { listing(t, false) },
		"sbx listed nothing":  func() { listing(t, true) },
	} {
		screen, _ := captured(t)
		dir := keptStore(t, "sb")
		set()
		err := RemoveKept("sb", dir, func(string, ...any) {}, false)
		if !errors.Is(err, errNoListing) || !isReported(err) {
			t.Errorf("%s: RemoveKept = %v, want a refusal it printed", name, err)
		}
		want := "error: sb — not removed\n    sbx gave no listing of its sandboxes\n    `proveo rm sb` tries again once `sbx ls` answers\n"
		if screen.String() != want {
			t.Errorf("%s: the screen =\n%s\nwant\n%s", name, screen, want)
		}
		if keptNames(dir) != "sb" {
			t.Errorf("%s: the entry was cleared on a listing that proves nothing", name)
		}
	}
	if strings.Contains(sbxCalls(t, calls), "rm --force") {
		t.Error("a refused removal still ran sbx rm")
	}
}

func TestRemoveKeptRefusesARunningSandboxAndForgetsAGoneOne(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	dir := keptStore(t, "sb", "gone")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "running"}, sbx.Listed{Name: "other", Status: "stopped"})

	screen, _ := captured(t)
	err := RemoveKept("sb", dir, func(string, ...any) {}, false)
	if err == nil || !strings.Contains(err.Error(), "is running") {
		t.Errorf("RemoveKept of a running sandbox = %v, want a refusal", err)
	}
	screen.Reset()
	var said []string
	if err := RemoveKept("gone", dir, func(f string, a ...any) { said = append(said, f) }, false); err != nil {
		t.Errorf("RemoveKept of a sandbox sbx no longer lists = %v", err)
	}
	if keptNames(dir) != "sb" {
		t.Errorf("entries = %q, want the running one kept and the gone one cleared", keptNames(dir))
	}
	if len(said) != 0 || screen.String() != "ok: gone — already gone\n" {
		t.Errorf("reports %q, screen %q; want one closing line and no entry-cleared line", said, screen)
	}
	if strings.Contains(sbxCalls(t, calls), "rm --force") {
		t.Error("sbx rm ran for a running or a missing sandbox")
	}
}

func TestRemoveKeptOfANameNoRunKeptWarnsAndExitsZero(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	dir := keptStore(t, "kept")
	listing(t, true, sbx.Listed{Name: "bystander", Status: "running"})
	screen, _ := captured(t)
	if err := RemoveKept("typo", dir, func(string, ...any) {}, false); err != nil {
		t.Errorf("RemoveKept of a name nothing knows = %v, want nil (exit 0)", err)
	}
	if want := "warn: typo — no such sandbox\n    sbx lists none, and no run kept one by that name\n"; screen.String() != want {
		t.Errorf("the screen =\n%s\nwant\n%s", screen, want)
	}
	if keptNames(dir) != "kept" || strings.Contains(sbxCalls(t, calls), "rm --force") {
		t.Error("a name nothing knows changed the store or reached sbx rm")
	}
}

func TestARefusedRemovalIsOneItemWithTheStopFix(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "running"})
	screen, _ := captured(t)
	if err := RemoveKept("sb", dir, func(string, ...any) {}, false); !isReported(err) {
		t.Errorf("RemoveKept = %v, want a printed refusal", err)
	}
	want := "error: sb — not removed\n    it is running\n    `proveo rm --stop sb` carries its work home and removes it\n"
	if screen.String() != want {
		t.Errorf("the screen =\n%s\nwant\n%s", screen, want)
	}
	if strings.Contains(sbxCalls(t, calls), "stop sb") {
		t.Error("a refusal stopped the sandbox")
	}
}

func TestRemoveKeptCarriesThenRemovesAStoppedSandbox(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped", Workspaces: []string{gitRepo(t)}})
	screen, _ := captured(t)
	var said []string
	if err := RemoveKept("sb", dir, func(f string, a ...any) { said = append(said, f) }, false); err != nil {
		t.Fatalf("RemoveKept = %v", err)
	}
	if !strings.Contains(sbxCalls(t, calls), "rm --force sb") {
		t.Errorf("the sandbox was not removed:\n%s", sbxCalls(t, calls))
	}
	if keptNames(dir) != "" {
		t.Errorf("the entry outlived the removal: %q", keptNames(dir))
	}
	if len(said) != 2 || !strings.Contains(said[0], "carrying") || !strings.Contains(said[1], "sbx rm --force") {
		t.Errorf("reports = %q, want the carry, then the removal", said)
	}
	if !strings.HasSuffix(screen.String(), "branches: no new commits\nok: sb — removed\n") {
		t.Errorf("the screen does not close on the removal after what came home:\n%s", screen)
	}
}

func TestRemoveKeptOfAMountedCheckoutPrintsNoCarryLine(t *testing.T) {
	for _, c := range []struct {
		name   string
		status string
		stop   bool
		want   []string
	}{
		{"stopped", "stopped", false, []string{"sbx rm --force %s"}},
		{"running, under --stop", "running", true, []string{"sbx stop %s", "sbx rm --force %s"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeSbx(t, guestDirect, 0, bundleFails)
			dir := keptStore(t, "sb")
			listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: c.status, Workspaces: []string{gitRepo(t)}})
			captured(t)
			var said []string
			if err := RemoveKept("sb", dir, func(f string, a ...any) { said = append(said, f) }, c.stop); err != nil {
				t.Fatalf("RemoveKept = %v", err)
			}
			if strings.Join(said, "|") != strings.Join(c.want, "|") {
				t.Errorf("reports = %q, want %q: a mounted checkout carries nothing, so it prints no carry line", said, c.want)
			}
			if got := sbxCalls(t, calls); strings.Contains(got, "bundle create") || !strings.Contains(got, "rm --force sb") {
				t.Errorf("a mounted checkout was carried, or not removed:\n%s", got)
			}
		})
	}
}

func TestRemoveKeptOfASandboxThatVanishedBeforeTheProbeSaysAlreadyGone(t *testing.T) {
	calls := endingSbx(t, ending{})
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped", Workspaces: []string{gitRepo(t)}})
	screen, _ := captured(t)
	var said []string
	if err := RemoveKept("sb", dir, func(f string, a ...any) { said = append(said, f) }, false); err != nil {
		t.Fatalf("RemoveKept = %v", err)
	}
	if screen.String() != "ok: sb — already gone\n" || len(said) != 0 {
		t.Errorf("reports %q, screen %q; want only the closing line", said, screen)
	}
	if got := callsOf(t, calls); strings.Contains(got, "/run/sandbox/source") || strings.Contains(got, "rm --force") {
		t.Errorf("a sandbox gone before the probe was probed or removed:\n%s", got)
	}
	if keptNames(dir) != "" {
		t.Errorf("the entry of a gone sandbox stayed: %q", keptNames(dir))
	}
}

func TestTheCloneProbeRunsBehindALiveStep(t *testing.T) {
	fakeSbx(t, guestClone, 0, bundleEmpty)
	steps := captureSteps(t)
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped", Workspaces: []string{gitRepo(t)}})
	screen, _ := captured(t)
	if err := RemoveKept("sb", dir, func(string, ...any) {}, false); err != nil {
		t.Fatalf("RemoveKept = %v", err)
	}
	if names := stepNames(*steps); len(names) == 0 || names[0] != stepCloneMode {
		t.Errorf("steps = %v, want the clone-mode probe first, as its own step", names)
	}
	if !strings.HasPrefix(screen.String(), startLine(stepCloneMode)+"\n") {
		t.Errorf("plain output does not open on the probe's start line:\n%s", screen)
	}
}

func TestAStrandedCarryClosesOnNotRemovedWithItsRetry(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleFails)
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped", Workspaces: []string{gitRepo(t)}})
	screen, _ := captured(t)
	err := RemoveKept("sb", dir, func(string, ...any) {}, false)
	if err == nil || !strings.Contains(err.Error(), "kept") || !isReported(err) {
		t.Fatalf("RemoveKept = %v, want a printed refusal that keeps the sandbox", err)
	}
	got := screen.String()
	if !strings.HasSuffix(got, "error: sb — not removed\n    `proveo rm sb` tries again\n") {
		t.Errorf("the screen does not close on the retry:\n%s", got)
	}
	if strings.Count(got, "branches — not carried home") != 1 || strings.Contains(got, "sbx rm") || strings.Contains(got, "bundle create") {
		t.Errorf("the ⚠ above says once what stayed, and no sbx command or bundle recipe prints:\n%s", got)
	}
	if strings.Contains(sbxCalls(t, calls), "rm --force") {
		t.Error("a sandbox whose work did not come home was removed")
	}
	if keptNames(dir) != "sb" {
		t.Error("the entry of a kept sandbox was cleared")
	}
}

func TestAFailedRemovalNamesSbxsErrorAndTheCheck(t *testing.T) {
	endingSbx(t, ending{row: "sb claude stopped", rm: "echo 'ERROR: context deadline exceeded' >&2; exit 1"})
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped"})
	screen, _ := captured(t)
	if err := RemoveKept("sb", dir, func(string, ...any) {}, false); !isReported(err) {
		t.Errorf("RemoveKept = %v, want a printed failure", err)
	}
	want := "error: sb — not removed\n    sbx rm: exit status 1: ERROR: context deadline exceeded\n    check `sbx ls`, then `proveo rm sb` again\n"
	if screen.String() != want {
		t.Errorf("the screen =\n%s\nwant\n%s", screen, want)
	}
	if keptNames(dir) != "sb" {
		t.Error("a failed removal cleared the entry")
	}
}

func TestAStopThatFailedClosesOnNotRemoved(t *testing.T) {
	f := newFakeEngine(t, true)
	f.set(t, "stop-fails")
	dir := keptStore(t, ocEngine)
	listing(t, true, sbx.Listed{Name: ocEngine, Agent: "proveo-opencode", Status: "running"})
	screen, _ := captured(t)
	if err := RemoveKept(ocEngine, dir, func(string, ...any) {}, true); !isReported(err) {
		t.Errorf("RemoveKept = %v, want a printed failure", err)
	}
	want := "error: " + ocEngine + " — not removed\n    sbx stop: exit status 1\n    `proveo rm --stop " + ocEngine + "` tries again\n"
	if screen.String() != want {
		t.Errorf("the screen =\n%s\nwant\n%s", screen, want)
	}
	if !f.exists() || keptNames(dir) != ocEngine {
		t.Error("a sandbox that did not stop was removed, or its entry cleared")
	}
}

func TestRemoveKeptPrintsTheOpenCodeHistoryInsideItsBlock(t *testing.T) {
	f := newFakeEngine(t, true)
	writeDB(t, f.guestDB(), "ses_engine")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_HOME", filepath.Join(home, ".proveo"))
	dir := keptStore(t, ocEngine)
	listing(t, true, sbx.Listed{Name: ocEngine, Agent: "proveo-opencode", Status: "stopped"})
	screen, _ := captured(t)
	if err := RemoveKept(ocEngine, dir, func(string, ...any) {}, false); err != nil {
		t.Fatalf("RemoveKept = %v\ncalls:\n%s", err, f.log())
	}
	carry := "~/.proveo/opencode/carry/" + ocEngine + ".db"
	lines := strings.Split(strings.TrimSpace(screen.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "history: carried history (") || !strings.HasSuffix(lines[0], ") to "+carry) ||
		lines[1] != "ok: "+ocEngine+" — removed" {
		t.Errorf("the history is one fact of the block, before the closing line:\n%s", screen)
	}
	if strings.Contains(screen.String(), "for the engine that replaces it") {
		t.Errorf("the why stays in _spec:\n%s", screen)
	}
}

func TestAFailedEntryWriteAfterARemovalIsTheOneClosingLine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	endingSbx(t, ending{row: "sb claude stopped"})
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped"})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	screen, _ := captured(t)
	if err := RemoveKept("sb", dir, func(string, ...any) {}, false); !isReported(err) {
		t.Errorf("RemoveKept = %v, want the ⚠ it printed, Reported", err)
	}
	got := screen.String()
	if !strings.HasPrefix(got, "warn: sb — removed, its entry stays\n    ") || strings.Contains(got, "ok: ") {
		t.Errorf("one closing line says the sandbox went and its entry stayed, and no ✓ prints:\n%s", got)
	}
}

func TestReconcileDropsOnlyWhatIsProvablyGone(t *testing.T) {
	repo := results.Canonical(t.TempDir())
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
	git("commit", "-q", "--allow-empty", "-m", "work")
	git("update-ref", "refs/proveo/s1/feat", "HEAD")
	git("update-ref", "refs/proveo/s1/done", "HEAD~1")
	git("reset", "-q", "--hard", "HEAD~1")

	dir := keptStore(t, "listed", "unlisted")
	gone := filepath.Join(t.TempDir(), "deleted-repo")
	for _, e := range []results.Entry{
		{Kind: results.Carried, Key: "s1", Repo: repo, Refs: []results.Ref{{Name: "proveo/s1/feat", Lead: 1}, {Name: "proveo/s1/done", Lead: 1}, {Name: "proveo/s1/deleted", Lead: 2}}},
		{Kind: results.Carried, Key: "s2", Repo: gone, Refs: []results.Ref{{Name: "proveo/s2/x", Lead: 1}}},
		{Kind: results.Carried, Key: "s3", Repo: results.Canonical(t.TempDir()), Refs: []results.Ref{{Name: "proveo/s3/x", Lead: 1}}},
	} {
		if err := results.Put(dir, e); err != nil {
			t.Fatal(err)
		}
	}

	listing(t, true)
	ReconcileResults(dir, repo)
	if keptNames(dir) != "listed,unlisted" {
		t.Errorf("an empty listing cleared a kept entry: %q", keptNames(dir))
	}

	listing(t, true, sbx.Listed{Name: "listed", Status: "stopped"})
	ReconcileResults(dir, repo)
	if keptNames(dir) != "listed" {
		t.Errorf("kept entries = %q, want only the sandbox sbx still lists", keptNames(dir))
	}
	carried := map[string][]results.Ref{}
	for _, e := range results.List(dir) {
		if e.Kind == results.Carried {
			carried[e.Key] = e.Refs
		}
	}
	if refs := carried["s1"]; len(refs) != 1 || refs[0].Name != "proveo/s1/feat" {
		t.Errorf("s1 refs = %+v, want only the one no branch holds", refs)
	}
	if _, ok := carried["s2"]; ok {
		t.Error("a session whose repository is gone is still recorded")
	}
	if _, ok := carried["s3"]; !ok {
		t.Error("another repository's session was dropped without looking at it")
	}

	git("branch", "adopted", "refs/proveo/s1/feat")
	ReconcileResults(dir, repo)
	for _, e := range results.List(dir) {
		if e.Key == "s1" {
			t.Errorf("a session whose refs are all on a branch is still recorded: %+v", e)
		}
	}
}

func TestRemoveKeptStopsARunningSandboxOnlyWhenAsked(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "running", Workspaces: []string{gitRepo(t)}})
	captured(t)
	err := RemoveKept("sb", dir, func(string, ...any) {}, false)
	if err == nil || !strings.Contains(err.Error(), "is running") || !strings.Contains(err.Error(), "`proveo rm --stop sb`") {
		t.Errorf("RemoveKept of a running sandbox without --stop = %v, want a refusal that names --stop", err)
	}
	if strings.Contains(sbxCalls(t, calls), "stop sb") {
		t.Fatal("a sandbox was stopped without --stop")
	}
	var said []string
	if err := RemoveKept("sb", dir, func(f string, a ...any) { said = append(said, f) }, true); err != nil {
		t.Fatalf("RemoveKept --stop = %v", err)
	}
	got := sbxCalls(t, calls)
	if stop, rm := strings.Index(got, "stop sb"), strings.Index(got, "rm --force sb"); stop < 0 || rm < stop {
		t.Errorf("--stop runs `sbx stop`, then carries and removes:\n%s", got)
	}
	if keptNames(dir) != "" || len(said) != 3 || said[0] != "sbx stop %s" {
		t.Errorf("entries %q, reports %q", keptNames(dir), said)
	}
	for _, line := range said {
		if strings.Contains(line, "/2 ") {
			t.Errorf("%q carries a place; `proveo rm --stop` prints three unnumbered lines", line)
		}
	}
}

func TestKeptInputLiftsTheOutputDirTheReceiptRecords(t *testing.T) {
	fakeSbx(t, guestClone, 0, bundleEmpty)
	writeReceipt("sb", launchReceipt{OutputDir: "/w/repo/reports"})
	in, _ := keptInput(sbx.Listed{Name: "sb", Agent: "proveo-claudecode"}, results.Dir(t.TempDir()))
	if in.OutputDir != "/w/repo/reports" {
		t.Errorf("keptInput OutputDir = %q, want the receipt's: `proveo rm` lifts the output a failed lift left behind", in.OutputDir)
	}
	was := receiptOf(sbx.RunConfig{}, "img", nil, "", "")
	was.OutputDir = "/elsewhere"
	if receiptOf(sbx.RunConfig{}, "img", nil, "", "").changes(&was) != nil {
		t.Error("a different output dir marked the sandbox stale")
	}
}

// The run that keeps a sandbox records its output dir in the kept entry, a
// re-attach as well as a create, so `proveo rm` lifts into it even when the receipt predates it.
func TestKeptInputLiftsIntoTheDirTheKeepingRunRecorded(t *testing.T) {
	fakeSbx(t, guestClone, 0, bundleEmpty)
	writeReceipt("sb", launchReceipt{})
	dir := results.Dir(t.TempDir())
	recordKept(Input{ResultsDir: dir, RepoRoot: "/w/repo", OutputDir: "/w/repo/out"}, "sb", "the output is only in it", false)
	in, _ := keptInput(sbx.Listed{Name: "sb", Agent: "proveo-claudecode"}, dir)
	if in.OutputDir != "/w/repo/out" || in.RepoRoot != "/w/repo" {
		t.Errorf("keptInput = %q in %q, want the keeping run's output dir and repo", in.OutputDir, in.RepoRoot)
	}
}

// `proveo rm` lifts a kept clone's output, from the receipt's OutputDir, through
// sbx's tar stream, before it removes the sandbox.
func TestRemoveKeptLiftsTheOutputTheReceiptRecords(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main", ".")
	calls := endingSbx(t, ending{row: "sb claude stopped", lift: "tar -cf - -T /dev/null"})
	writeReceipt("sb", launchReceipt{OutputDir: filepath.Join(repo, "reports")})
	captureSteps(t)
	captured(t)
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped", Workspaces: []string{repo}})
	var said []string
	if err := RemoveKept("sb", dir, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }, false); err != nil {
		t.Fatalf("RemoveKept = %v", err)
	}
	got := callsOf(t, calls)
	lift, rm := strings.Index(got, "cd '"+repo+"' && { [ -d 'reports' ] || exit 3; } && tar -cf - 'reports'"), strings.Index(got, "rm --force sb")
	if lift < 0 || rm < lift {
		t.Errorf("want the output lifted from the clone's reports/, then the removal:\n%s", got)
	}
	if strings.Join(said, "|") != "carrying its work home from sb|sbx rm --force sb" {
		t.Errorf("reports = %q, want unnumbered lines", said)
	}
}
