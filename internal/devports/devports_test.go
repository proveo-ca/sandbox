// SPEC: _spec/internal/devports/dev-ports.puml
package devports

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line     string
		tool     string
		port     int
		explicit bool
		ok       bool
	}{
		{"next dev", "next", 3000, false, true},
		{"next dev -p 4000", "next", 4000, true, true},
		{"vite", "vite", 5173, false, true},
		{"vite --port 3001", "vite", 3001, true, true},
		{"vite build", "", 0, false, false},
		{"vite preview", "vite preview", 4173, false, true},
		{"astro dev", "astro", 4321, false, true},
		{"ng serve", "angular", 4200, false, true},
		{"wrangler dev", "wrangler", 8787, false, true},
		{"storybook dev -p 6006", "storybook", 6006, true, true},
		{"python manage.py runserver 0.0.0.0:8100", "django", 8100, true, true},
		{"uvicorn app:api --reload", "uvicorn", 8000, false, true},
		{"flask run", "flask", 5000, false, true},
		{"PORT=4100 node server.js", "custom", 4100, true, true},
		{"node server.js", "", 0, false, false},
		{"go run ./cmd/api", "go run", 0, false, false},
		{"cargo run -- --port 7000", "cargo run", 7000, true, true},
		{"hugo server", "hugo", 1313, false, true},
	} {
		tool, port, explicit, ok := Match(tc.line)
		if tool != tc.tool || port != tc.port || explicit != tc.explicit || ok != tc.ok {
			t.Errorf("Match(%q) = %q, %d, %v, %v; want %q, %d, %v, %v",
				tc.line, tool, port, explicit, ok, tc.tool, tc.port, tc.explicit, tc.ok)
		}
	}
}

func TestDiscoverMonorepo(t *testing.T) {
	t.Parallel()
	got := Discover("testdata/mono")
	want := []Candidate{
		{Port: 1313, Command: "dev", Tool: "hugo", Source: "Makefile"},
		{Port: 3000, Command: "dev", Tool: "next", Source: "apps/web/package.json"},
		{Port: 3001, Command: "dev", Tool: "vite", Source: "apps/docs/package.json", Explicit: true},
		{Port: 4173, Command: "preview", Tool: "vite preview", Source: "apps/docs/package.json"},
		{Port: 5050, Command: "web", Tool: "gunicorn", Source: "Procfile", Explicit: true},
		{Port: 6006, Command: "storybook", Tool: "storybook", Source: "apps/docs/package.json", Explicit: true},
		{Port: 8000, Command: "runserver", Tool: "django", Source: "services/admin/manage.py"},
		{Port: 8001, Command: "tasks.serve", Tool: "uvicorn", Source: "mise.toml", Explicit: true},
		{Port: 8080, Command: "go run", Tool: "go", Source: "apps/api/main.go", Explicit: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Discover(testdata/mono) mismatch (-want +got):\n%s", diff)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	t.Parallel()
	if got := Discover(t.TempDir()); len(got) != 0 {
		t.Errorf("Discover(empty) = %v, want none", got)
	}
}

func TestDiscoverSkipsDependencyTrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, dir := range []string{"node_modules/x", ".venv/y", "a/b/c"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		pkg := `{"scripts":{"dev":"vite --port 9999"}}`
		if err := os.WriteFile(filepath.Join(root, dir, "package.json"), []byte(pkg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := Discover(root); len(got) != 0 {
		t.Errorf("Discover(%s) = %v, want none from dependency trees or depth > %d", root, got, maxDepth)
	}
}
