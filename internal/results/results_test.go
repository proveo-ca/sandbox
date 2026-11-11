// SPEC: _spec/internal/results/result-store.puml
package results

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnEntryRoundTripsAndReplacesItsPredecessor(t *testing.T) {
	dir := Dir(t.TempDir())
	repo := t.TempDir()
	first := Entry{Kind: Kept, Key: "proveo-claudecode-abc", Sid: "proveo-1-2", Repo: repo, Pending: true, Why: "teardown did not finish"}
	if err := Put(dir, first); err != nil {
		t.Fatal(err)
	}
	final := first
	final.Pending, final.Why = false, "the run failed"
	if err := Put(dir, final); err != nil {
		t.Fatal(err)
	}
	got := List(dir)
	if len(got) != 1 {
		t.Fatalf("List = %d entries, want the one that replaced its predecessor: %+v", len(got), got)
	}
	if got[0].Pending || got[0].Why != "the run failed" || got[0].Repo != Canonical(repo) || got[0].At.IsZero() {
		t.Errorf("entry = %+v", got[0])
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".entry-*")); len(left) != 0 {
		t.Errorf("a write left its temporary file behind: %v", left)
	}
	if err := Remove(dir, Kept, "proveo-claudecode-abc"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, Kept, "proveo-claudecode-abc"); err != nil {
		t.Errorf("removing a missing entry = %v, want nil", err)
	}
	if got := List(dir); len(got) != 0 {
		t.Errorf("List after Remove = %+v", got)
	}
}

func TestAKeyThatCouldLeaveTheStoreIsRefused(t *testing.T) {
	dir := Dir(t.TempDir())
	for _, key := range []string{"", "../escape", "a/b", ".hidden", "with space", strings.Repeat("x", 200)} {
		if err := Put(dir, Entry{Kind: Kept, Key: key}); err == nil {
			t.Errorf("Put accepted the key %q", key)
		}
	}
	if err := Put(dir, Entry{Kind: "other", Key: "ok"}); err == nil {
		t.Error("Put accepted an unknown kind")
	}
	if err := Put("", Entry{Kind: Kept, Key: "ok"}); err == nil {
		t.Error("Put accepted an empty store directory")
	}
}

func TestListSkipsWhatItCannotTrust(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := Put(dir, Entry{Kind: Carried, Key: "proveo-1-2"}); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"kept-torn.json":    `{"kind":"kept","key":"torn"`,
		"kept-renamed.json": `{"kind":"kept","key":"another-name","at":"2026-11-10T00:00:00Z"}`,
		"notes.txt":         "not an entry",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := List(dir)
	if len(got) != 1 || got[0].Key != "proveo-1-2" {
		t.Errorf("List = %+v, want the one well-formed entry", got)
	}
	if got := List(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("List of a missing store = %+v, want nothing", got)
	}
}

func TestTheSummaryCountsThisRepositorysEntries(t *testing.T) {
	here, there := Canonical(t.TempDir()), Canonical(t.TempDir())
	entries := []Entry{
		{Kind: Kept, Key: "sb-a", Repo: here},
		{Kind: Kept, Key: "sb-b", Repo: there, Pending: true},
		{Kind: Carried, Key: "s1", Repo: here, Refs: []Ref{{Name: "proveo/s1/feat", Lead: 3}, {Name: "proveo/s1/main", Lead: 0}}},
		{Kind: Carried, Key: "s2", Repo: here, Refs: []Ref{{Name: "proveo/s2/fix", Lead: 1}}},
		{Kind: Carried, Key: "s3", Repo: there, Refs: []Ref{{Name: "proveo/s3/x", Lead: 2}}},
	}
	for _, c := range []struct{ name, repo, want string }{
		{"this repository", here, "1 kept · 2 to adopt"},
		{"the other one", there, "1 kept · 1 to adopt"},
		{"a repository with nothing", Canonical(t.TempDir()), ""},
		{"no repository", "", "2 kept · 3 to adopt"},
	} {
		if got := Summary(entries, c.repo); got != c.want {
			t.Errorf("%s: Summary = %q, want %q", c.name, got, c.want)
		}
	}
	if got := Summary([]Entry{{Kind: Carried, Key: "s", Repo: here, Refs: []Ref{{Name: "r", Lead: 0}}}}, here); got != "" {
		t.Errorf("a ref HEAD already holds counted as work to adopt: %q", got)
	}
	twice := []Entry{
		{Kind: Carried, Key: "s1", Repo: here, Refs: []Ref{{Name: "proveo/s1/main", Lead: 1, Tip: "abc"}}},
		{Kind: Carried, Key: "s1-rm", Repo: here, Refs: []Ref{{Name: "proveo/s1-rm/main", Lead: 1, Tip: "abc"}, {Name: "proveo/s1-rm/x", Lead: 2, Tip: "def"}}},
	}
	if got := Summary(twice, here); got != "2 to adopt" {
		t.Errorf("one commit carried under two refs: Summary = %q, want it counted once", got)
	}
	if got := Summary([]Entry{{Kind: Kept, Key: "sb", Repo: here}}, here); got != "1 kept" {
		t.Errorf("Summary = %q, want only the part that is not zero", got)
	}
}

func TestRepoOfFindsTheCheckoutFromInsideIt(t *testing.T) {
	root := Canonical(t.TempDir())
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RepoOf(deep); got != "" {
		t.Errorf("RepoOf outside a repository = %q", got)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RepoOf(deep); got != root {
		t.Errorf("RepoOf = %q, want %q", got, root)
	}
	worktree := filepath.Join(root, "a")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RepoOf(deep); got != worktree {
		t.Errorf("RepoOf under a linked worktree = %q, want its own root %q", got, worktree)
	}
}

func TestSetExitMarksOnlyTheSessionsEntries(t *testing.T) {
	dir := Dir(t.TempDir())
	for _, e := range []Entry{
		{Kind: Kept, Key: "sb-a", Sid: "s1", At: time.Unix(1, 0)},
		{Kind: Carried, Key: "s1", Sid: "s1", At: time.Unix(2, 0)},
		{Kind: Kept, Key: "sb-b", Sid: "s2", At: time.Unix(3, 0)},
	} {
		if err := Put(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	SetExit(dir, "s1", 130)
	SetExit(dir, "", 1)
	for _, e := range List(dir) {
		switch {
		case e.Sid == "s1" && (e.Exit == nil || *e.Exit != 130):
			t.Errorf("%s %s has exit %v, want 130", e.Kind, e.Key, e.Exit)
		case e.Sid == "s2" && e.Exit != nil:
			t.Errorf("another session's entry got exit %d", *e.Exit)
		}
	}
}
