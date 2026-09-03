package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
)

// polyglot lays out the e2e sample's shape: a pnpm-style root with hoisted
// deps, nested JS packages (one installed, one not), a Rust crate, a Go module,
// a Python service with an in-tree venv, a Terraform module — and two decoys.
func polyglot(t *testing.T) string {
	t.Helper()
	root := tempDir(t)
	touch(t, filepath.Join(root, "package.json"))
	touch(t, filepath.Join(root, "node_modules", ".modules.yaml"))
	touch(t, filepath.Join(root, "apps", "tui", "package.json"))
	touch(t, filepath.Join(root, "apps", "tui", "node_modules", "ink", "index.js"))
	touch(t, filepath.Join(root, "packages", "lib", "package.json")) // no node_modules on the host
	touch(t, filepath.Join(root, "apps", "harness", "Cargo.toml"))
	touch(t, filepath.Join(root, "apps", "harness", "target", "debug", "x.o"))
	touch(t, filepath.Join(root, "apps", "api", "go.mod")) // portable: nothing to isolate
	touch(t, filepath.Join(root, "svc", "py", "pyproject.toml"))
	touch(t, filepath.Join(root, "svc", "py", ".venv", "bin", "python"))
	touch(t, filepath.Join(root, "infra", ".terraform.lock.hcl"))
	touch(t, filepath.Join(root, "infra", ".terraform", "providers", "x"))
	// Decoys. A tracked directory that happens to be called build, with no build
	// system rooting it, is source — overlaying it would swallow the agent's edits.
	touch(t, filepath.Join(root, "tools", "build", "release.sh"))
	// A package.json INSIDE node_modules is a dependency, not a project.
	touch(t, filepath.Join(root, "node_modules", "left-pad", "package.json"))
	// Beyond the scan depth: the seed will not find it either.
	touch(t, filepath.Join(root, "a", "b", "c", "d", "e", "package.json"))
	touch(t, filepath.Join(root, "a", "b", "c", "d", "e", "node_modules", "x.js"))
	return root
}

func containers(copies []DepCopy) []string {
	var out []string
	for _, c := range copies {
		out = append(out, c.Container)
	}
	sort.Strings(out)
	return out
}

func TestDepCopiesCoverEveryLanguageAtEveryDepth(t *testing.T) {
	t.Parallel()
	root := polyglot(t)
	spec := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: root,
		EgressMode: "open", Credentials: "forward",
		DepStage: t.TempDir(),
	}
	want := []string{
		"/app/apps/harness/target",
		"/app/apps/tui/node_modules",
		"/app/infra/.terraform",
		"/app/node_modules",
		"/app/packages/lib/node_modules", // absent on the host, overlaid anyway
		"/app/svc/py/.venv",
		"/app/svc/py/venv", // the table names both spellings; an empty overlay costs nothing
	}
	if diff := cmp.Diff(want, containers(spec.DepCopies())); diff != "" {
		t.Errorf("isolated trees (-want +got):\n%s", diff)
	}

	got, _, _ := spec.Plan()
	direct := map[string]bool{}
	for _, m := range got {
		direct[m.Host] = true
	}
	for _, host := range []string{
		filepath.Join(root, "node_modules"),
		filepath.Join(root, "apps", "tui", "node_modules"),
		filepath.Join(root, "apps", "harness", "target"),
		filepath.Join(root, "svc", "py", ".venv"),
		filepath.Join(root, "infra", ".terraform"),
	} {
		if direct[host] {
			t.Errorf("%s is bind-mounted from the host; every dependency tree must be a private copy", host)
		}
	}
	for _, m := range got {
		if strings.HasSuffix(m.Container, "/tools/build") {
			t.Errorf("a source directory called build was overlaid: %+v — only a build system's marker makes it a dependency tree", m)
		}
		if strings.Contains(m.Container, "/a/b/c/d/e/") {
			t.Errorf("a project past the scan depth was overlaid: %+v", m)
		}
		if strings.Contains(m.Container, "left-pad") {
			t.Errorf("a package inside node_modules was treated as a project: %+v", m)
		}
	}
}

func TestPlanIsPureAndMaterializeWrites(t *testing.T) {
	t.Parallel()
	root := polyglot(t)
	stage := filepath.Join(t.TempDir(), "deps")
	spec := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: root,
		EgressMode: "open", Credentials: "forward",
		DepStage: stage,
	}

	first, _, _ := spec.Plan()
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("Plan wrote the staging dir (%v); it must stay pure so --print never copies a tree", err)
	}
	second, _, _ := spec.Plan()
	if diff := cmp.Diff(first, second); diff != "" {
		t.Errorf("two plans of one spec disagree — staging paths must be deterministic:\n%s", diff)
	}

	copies := spec.DepCopies()
	made, err := MaterializeDeps(copies, true)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	byContainer := map[string]DepCopy{}
	for _, c := range copies {
		byContainer[c.Container] = c
	}
	// A host tree arrives in the copy, symlink-free and complete.
	if _, err := os.Stat(filepath.Join(byContainer["/app/apps/tui/node_modules"].Stage, "ink", "index.js")); err != nil {
		t.Errorf("copied tree is missing its content: %v", err)
	}
	// A tree the host never installed is an EMPTY overlay: it exists, so docker
	// binds it, and the seed's install lands there rather than on the host.
	empty := byContainer["/app/packages/lib/node_modules"].Stage
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Errorf("absent host tree must stage as an empty directory: entries=%v err=%v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "lib", "node_modules")); !os.IsNotExist(err) {
		t.Errorf("materializing must never create directories in the operator's checkout")
	}
	// The report distinguishes the two: only real copies are "made".
	if got := containers(made); !cmp.Equal(got, []string{
		"/app/apps/harness/target", "/app/apps/tui/node_modules", "/app/infra/.terraform", "/app/node_modules", "/app/svc/py/.venv",
	}) {
		t.Errorf("made = %v; want exactly the trees that exist on the host", got)
	}
	// Every staged dir is under the stage, so removing the stage removes them all.
	for _, c := range copies {
		if !underDir(c.Stage, stage) {
			t.Errorf("%s staged outside %s", c.Stage, stage)
		}
	}
}

func TestDepCopiesRootTreesOfASubdirScopeArePresentOnly(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	touch(t, filepath.Join(root, "package.json")) // a workspace root with nothing installed
	touch(t, filepath.Join(root, "Cargo.toml"))
	touch(t, filepath.Join(root, "target", "debug", "x"))
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "package.json")) // member: nothing installed either

	copies := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "open", Credentials: "forward",
		MountRootDeps: true, DepStage: t.TempDir(),
	}.DepCopies()
	// /app itself is container filesystem in a subdir scope, so an absent root
	// tree needs no overlay: a hoisted install the agent runs already stays put.
	// The scope's own absent tree DOES: its parent is the host bind.
	want := []string{"/app/apps/web/node_modules", "/app/target"}
	if diff := cmp.Diff(want, containers(copies)); diff != "" {
		t.Errorf("subdir scope copies (-want +got):\n%s", diff)
	}
}

func TestDepOverlaysStayWritableOnAReadOnlyWorkspace(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	touch(t, filepath.Join(root, "package.json"))
	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app", Mode: "ro"},
		InputDir:  root, EgressMode: "open", Credentials: "forward",
		DepStage: t.TempDir(),
	}.Plan()
	for _, m := range got {
		if m.Container == "/app/node_modules" && m.ReadOnly {
			t.Errorf("dependency overlay is read-only: %+v — an install has to land somewhere, and the copy is the only place that is not the operator's", m)
		}
	}
}

func TestStripDepCopiesDropsOnlyTheStagedOverlays(t *testing.T) {
	t.Parallel()
	stage := filepath.Join(t.TempDir(), "egress", "sid", "deps")
	mounts := []runner.Mount{
		{Host: "/home/op/repo", Container: "/app"},
		{Host: filepath.Join(stage, "abc-node_modules"), Container: "/app/node_modules"},
		{Host: filepath.Join(stage, "def-target"), Container: "/app/rs/target"},
		{Host: "/home/op/shared-docs", Container: "/app/docs"}, // an escaping symlink target: sbx CAN mirror this
		{Host: "/home/op/.proveo", Container: "/proveo-home"},
	}
	got, dropped := StripDepCopies(mounts, stage)
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	want := []runner.Mount{mounts[0], mounts[3], mounts[4]}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if got, n := StripDepCopies(mounts, ""); n != 0 || len(got) != len(mounts) {
		t.Errorf("an empty stage must strip nothing: n=%d len=%d", n, len(got))
	}
}

func TestMaterializeStagesEmptyOverlaysWhenTheTreeCannotRunHere(t *testing.T) {
	t.Parallel()
	root := polyglot(t)
	spec := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: root,
		EgressMode: "open", Credentials: "forward",
		DepStage: t.TempDir(),
	}
	copies := spec.DepCopies()
	made, err := MaterializeDeps(copies, false)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(made) != 0 {
		t.Errorf("copied %v although the host platform differs — the seed would only clear them again", containers(made))
	}
	for _, c := range copies {
		entries, err := os.ReadDir(c.Stage)
		if err != nil || len(entries) != 0 {
			t.Errorf("%s: overlay must exist and be empty (entries=%d err=%v) so the bind still isolates and the install lands in it", c.Container, len(entries), err)
		}
	}
}
