//go:build !windows

// SPEC: _spec/internal/results/result-store.puml
package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
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

func TestRemoveKeptClearsNothingOnAListingItCannotTrust(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	for name, set := range map[string]func(){
		"sbx gave no listing": func() { listing(t, false) },
		"sbx listed nothing":  func() { listing(t, true) },
	} {
		dir := keptStore(t, "sb")
		set()
		err := RemoveKept("sb", dir, func(string, ...any) {}, false)
		if !errors.Is(err, errNoListing) {
			t.Errorf("%s: RemoveKept = %v, want a refusal", name, err)
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

	err := RemoveKept("sb", dir, func(string, ...any) {}, false)
	if err == nil || !strings.Contains(err.Error(), "is running") {
		t.Errorf("RemoveKept of a running sandbox = %v, want a refusal", err)
	}
	var said []string
	if err := RemoveKept("gone", dir, func(f string, a ...any) { said = append(said, f) }, false); err != nil {
		t.Errorf("RemoveKept of a sandbox sbx no longer lists = %v", err)
	}
	if keptNames(dir) != "sb" {
		t.Errorf("entries = %q, want the running one kept and the gone one cleared", keptNames(dir))
	}
	if len(said) != 1 || !strings.Contains(said[0], "already gone") {
		t.Errorf("reports = %q", said)
	}
	if strings.Contains(sbxCalls(t, calls), "rm --force") {
		t.Error("sbx rm ran for a running or a missing sandbox")
	}
}

func TestRemoveKeptCarriesThenRemovesAStoppedSandbox(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped", Workspaces: []string{t.TempDir()}})
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
}

func TestRemoveKeptKeepsASandboxWhoseWorkCannotComeHome(t *testing.T) {
	calls := fakeSbx(t, guestClone, 0, bundleFails)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	dir := keptStore(t, "sb")
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "stopped", Workspaces: []string{repo}})
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	err := RemoveKept("sb", dir, func(string, ...any) {}, false)
	if err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("RemoveKept = %v, want a refusal that keeps the sandbox", err)
	}
	if strings.Contains(sbxCalls(t, calls), "rm --force") {
		t.Error("a sandbox whose work did not come home was removed")
	}
	if keptNames(dir) != "sb" {
		t.Error("the entry of a kept sandbox was cleared")
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
	listing(t, true, sbx.Listed{Name: "sb", Agent: "proveo-claudecode", Status: "running", Workspaces: []string{t.TempDir()}})
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
