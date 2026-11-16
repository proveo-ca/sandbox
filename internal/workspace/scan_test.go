package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

var markers = []Marker{
	{Label: "go", Names: []string{"go.mod"}, Suffixes: []string{".go"}},
	{Label: "docker", Names: []string{"Dockerfile", "compose.yml"}},
	{Label: "node", Names: []string{"package.json"}},
}

func TestFindsNestedMarkers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "services", "api", "go.mod"))
	got := Scan(root, root, markers, 0)
	if !got.Has("go") {
		t.Errorf("nested go.mod not found: %+v", got.Found)
	}
	if got.Truncated {
		t.Error("a small tree must not report truncation")
	}
}

func TestDependencyTreesArePrunedWithoutGitignore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "node_modules", "some-pkg", "Dockerfile"))
	touch(t, filepath.Join(root, "vendor", "dep", "go.mod"))
	got := Scan(root, root, markers, 0)
	if got.Has("docker") {
		t.Error("a Dockerfile inside node_modules must not count as the workspace's")
	}
	if got.Has("go") {
		t.Error("vendor/ must not be walked")
	}
}

func TestScopeGitignoreDoesNotShadowRepoRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(root, ".gitignore"))
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build-artifacts/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(scope, ".gitignore"))
	if err := os.WriteFile(filepath.Join(scope, ".gitignore"), []byte(".next/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(scope, "build-artifacts", "Dockerfile"))
	touch(t, filepath.Join(scope, "package.json"))

	got := Scan(scope, root, markers, 0)
	if !got.Has("node") {
		t.Error("the scope's own package.json must be found")
	}
	if got.Has("docker") {
		t.Error("the repo root's .gitignore must still prune, even with a scope-local one")
	}
}

func TestSuffixMarkersMatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "pkg", "thing.go"))
	if got := Scan(root, root, markers, 0); !got.Has("go") {
		t.Errorf("*.go suffix marker did not match: %+v", got.Found)
	}
}

func TestDepthIsBounded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deep := root
	for i := 0; i <= ScanMaxDepth+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	touch(t, filepath.Join(deep, "go.mod"))
	if got := Scan(root, root, markers, 0); got.Has("go") {
		t.Errorf("a marker below MaxDepth=%d must not be found", ScanMaxDepth)
	}
}

func TestBudgetExhaustionReportsTruncated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		touch(t, filepath.Join(root, "f", string(rune('a'+i%26))+"x", "noise.txt"))
	}
	got := Scan(root, root, markers, 3)
	if !got.Truncated {
		t.Error("a scan that ran out of budget must report Truncated")
	}
}

func TestMissingScopeIsEmptyNotAPanic(t *testing.T) {
	t.Parallel()
	if got := Scan(filepath.Join(t.TempDir(), "nope"), "", markers, 0); len(got.Found) != 0 {
		t.Errorf("a missing scope must find nothing, got %+v", got.Found)
	}
	if got := Scan("", "", markers, 0); got.Truncated {
		t.Error("an empty scope must not report truncation")
	}
}
