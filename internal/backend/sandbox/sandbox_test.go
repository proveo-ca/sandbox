// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package sandbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// A clone-mode workspace list has to be FLAT. sbx mounts every positional at its
// own host path and clones only into an empty workspace, so a bind under the repo
// is a bind inside the clone target — the clone is then skipped without a word and
// the agent starts in an empty root-owned directory (proveo-1788366117-41470).
func TestSplitNestedKeepsTheRootAndItsSiblingsOnly(t *testing.T) {
	t.Parallel()
	root := "/Users/op/repo"
	mounts := []sbx.Mount{
		{Host: root},
		{Host: "/Users/op/repo/reports"},       // the output dir: nested
		{Host: "/Users/op/repo/data/fixtures"}, // a --data-dir inside the repo: nested
		{Host: "/Users/op/repo2"},              // shares a prefix, is not under root
		{Host: "/Users/op/Syncd/_spec"},        // a symlink target elsewhere
		{Host: "/Users/op/.proveo"},
	}
	kept, nested := SplitNested(root, mounts)
	wantKept := []sbx.Mount{{Host: root}, {Host: "/Users/op/repo2"}, {Host: "/Users/op/Syncd/_spec"}, {Host: "/Users/op/.proveo"}}
	if diff := cmp.Diff(wantKept, kept); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
	wantNested := []sbx.Mount{{Host: "/Users/op/repo/reports"}, {Host: "/Users/op/repo/data/fixtures"}}
	if diff := cmp.Diff(wantNested, nested); diff != "" {
		t.Errorf("nested mismatch (-want +got):\n%s", diff)
	}
}

func TestNestedRelIsStrictAndSlashSeparated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		root, path, want string
		ok               bool
	}{
		{"/r", "/r/reports", "reports", true},
		{"/r", "/r/out/reports/", "out/reports", true},
		{"/r/", "/r", "", false},
		{"/r", "/r", "", false},
		{"/r", "/r2/x", "", false},
		{"/r", "/", "", false},
		{"", "/r/x", "", false},
		{"/r", "", "", false},
	}
	for _, c := range cases {
		got, ok := nestedRel(c.root, c.path)
		if got != c.want || ok != c.ok {
			t.Errorf("nestedRel(%q, %q) = (%q, %v), want (%q, %v)", c.root, c.path, got, ok, c.want, c.ok)
		}
	}
}

// The lift runs only for an output dir that IS nested — a sibling output dir was
// mounted live and has nothing to lift — and it unpacks under the repo root, so
// the archive's relative members land on the same path they had in the clone.
func TestLiftClonedOutputTargetsTheRepoRootWithTheCloneRelativePath(t *testing.T) {
	t.Parallel()
	in := Input{Clone: true, RepoRoot: "/Users/op/repo", OutputDir: "/Users/op/repo/reports"}
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: "/Users/op/repo"}, {Host: "/Users/op/.proveo"}}}

	var gotArgs []string
	var gotInto string
	liftClonedOutput(in, cfg, func(args []string, into string) (int, string, error) {
		gotArgs, gotInto = args, into
		return 0, "", nil
	})
	if gotInto != "/Users/op/repo" {
		t.Errorf("unpacked under %q, want the repo root so members like reports/x land on the output dir", gotInto)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec -w / sb", "cd '/Users/op/repo'", "tar -cf - 'reports'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lift argv %q lacks %q", joined, want)
		}
	}

	called := false
	liftClonedOutput(Input{Clone: true, RepoRoot: "/Users/op/repo", OutputDir: "/Users/op/out"}, cfg,
		func([]string, string) (int, string, error) { called = true; return 0, "", nil })
	if called {
		t.Error("an output dir OUTSIDE the repo was mounted live; lifting it would be a second copy")
	}

	// "Nothing there" is a note, not a failure — it must not be reported as one.
	// The fake returns the sentinel with an error, the way exec does.
	liftClonedOutput(in, cfg, func([]string, string) (int, string, error) {
		return sbx.CloneLiftNothing, "", errors.New("exit status 3")
	})
}

// The viewport is offered only where there is a browser to show AND a port to
// show it on, and it is the caller who picks the port — Spec must stay pure, or
// a --print plan would depend on which ports this machine has free.
func TestCDPPublishNeedsBothABrowserAndAPort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   Input
		want []string
	}{
		{"browser and port", Input{Browser: true, CDPHostPort: 49999}, []string{"49999:9222"}},
		{"no browser add-on", Input{CDPHostPort: 49999}, nil},
		{"no port (print mode)", Input{Browser: true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if diff := cmp.Diff(c.want, cdpPublish(c.in)); diff != "" {
				t.Errorf("cdpPublish mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if p := FreeLoopbackPort(); p <= 0 {
		t.Errorf("FreeLoopbackPort returned %d — the viewport needs a port before the sandbox exists", p)
	}
}
