package workspace

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/manifest"
)

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

	got, _ := spec.Plan()
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
		MountRootDeps: true,
	}.DepCopies()
	want := []string{"/app/apps/web/node_modules", "/app/target"}
	if diff := cmp.Diff(want, containers(copies)); diff != "" {
		t.Errorf("subdir scope copies (-want +got):\n%s", diff)
	}
}
