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
		{Port: 1313, Command: "dev", Tool: "hugo", Source: "Makefile", Line: "hugo server"},
		{Port: 3000, Command: "dev", Tool: "next", Source: "apps/web/package.json", Line: "next dev"},
		{Port: 3001, Command: "dev", Tool: "vite", Source: "apps/docs/package.json", Explicit: true, Line: "vite --port 3001"},
		{Port: 4173, Command: "preview", Tool: "vite preview", Source: "apps/docs/package.json", Line: "vite preview"},
		{Port: 5050, Command: "web", Tool: "gunicorn", Source: "Procfile", Explicit: true, Line: "gunicorn app:wsgi --bind 0.0.0.0:5050"},
		{Port: 6006, Command: "storybook", Tool: "storybook", Source: "apps/docs/package.json", Explicit: true, Line: "storybook dev -p 6006"},
		{Port: 8000, Command: "runserver", Tool: "django", Source: "services/admin/manage.py"},
		{Port: 8001, Command: "tasks.serve", Tool: "uvicorn", Source: "mise.toml", Explicit: true, Line: "uvicorn app:api --port 8001"},
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

func TestLaunchStartsTheServerOnEveryInterface(t *testing.T) {
	t.Parallel()
	root := "testdata/mono"
	byPort := map[int]Candidate{}
	for _, c := range Discover(root) {
		byPort[c.Port] = c
	}
	for _, tc := range []struct {
		port int
		dir  string
		cmd  string
	}{
		{3000, "apps/web", "HOST=0.0.0.0 PORT=3000 npm run dev -- -H 0.0.0.0"},
		{3001, "apps/docs", "HOST=0.0.0.0 PORT=3001 npm run dev -- --host 0.0.0.0"},
		{1313, ".", "HOST=0.0.0.0 PORT=1313 make dev"},
		{5050, ".", "HOST=0.0.0.0 PORT=5050 gunicorn app:wsgi --bind 0.0.0.0:5050"},
		{8000, "services/admin", "HOST=0.0.0.0 PORT=8000 python3 manage.py runserver 0.0.0.0:8000"},
		{8001, ".", "HOST=0.0.0.0 PORT=8001 mise run serve -- --host 0.0.0.0"},
		{8080, "apps/api", "HOST=0.0.0.0 PORT=8080 go run ."},
	} {
		dir, cmd := Launch(byPort[tc.port], root)
		if dir != tc.dir || cmd != tc.cmd {
			t.Errorf("Launch(:%d) = %q, %q; want %q, %q", tc.port, dir, cmd, tc.dir, tc.cmd)
		}
	}
}

func TestLaunchPicksThePackageManagerFromTheLockfile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	app := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := Candidate{Port: 5173, Command: "dev", Tool: "vite", Source: "apps/web/package.json", Line: "vite"}
	if _, cmd := Launch(c, root); cmd != "HOST=0.0.0.0 PORT=5173 pnpm run dev --host 0.0.0.0" {
		t.Errorf("Launch(pnpm workspace) = %q", cmd)
	}
}
