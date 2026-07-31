package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMountPlanInputOutput(t *testing.T) {
	t.Parallel()
	got, wd := MountSpec{Workspace: manifest.Workspace{Layout: "input-output"}, InputDir: "/repo", OutputDir: "/repo/reports"}.Plan()
	want := []runner.Mount{
		{Host: "/repo", Container: "/workspace/input"},
		{Host: "/repo/reports", Container: "/workspace/output"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("input-output mounts mismatch (-want +got):\n%s", diff)
	}
	if wd != "" {
		t.Errorf("input-output workdir = %q, want empty", wd)
	}
}

func TestMountPlanInputOutputRO(t *testing.T) {
	t.Parallel()
	got, _ := MountSpec{Workspace: manifest.Workspace{Layout: "input-output", Mode: "ro"}, InputDir: "/repo", OutputDir: "/out"}.Plan()
	want := []runner.Mount{
		{Host: "/repo", Container: "/workspace/input", ReadOnly: true},
		{Host: "/out", Container: "/workspace/output"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("input-output mode=ro mounts mismatch (-want +got):\n%s", diff)
	}
}

func TestMountPlanAppWholeRepo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, ".env"))
	got, wd := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "broker"}.Plan()
	want := []runner.Mount{
		{Host: root, Container: "/app"},
		{Host: filepath.Join(root, ".env"), Container: "/app/.env", ReadOnly: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("app whole-repo mounts mismatch (-want +got):\n%s", diff)
	}
	if wd != "/app" {
		t.Errorf("workdir = %q, want /app", wd)
	}
}

func TestMountPlanAppWholeRepoFirewallMasksEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, ".env"))
	got, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "firewall"}.Plan()
	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	m, ok := byContainer["/app/.env"]
	if !ok || m.Host != "/dev/null" || !m.ReadOnly {
		t.Fatalf("firewall should mask .env with /dev/null: %+v (ok=%v)", m, ok)
	}
}

// maskedEnvSet returns the set of container paths masked with a /dev/null:ro bind.
func maskedEnvSet(mounts []runner.Mount) map[string]bool {
	out := map[string]bool{}
	for _, m := range mounts {
		if m.Host == "/dev/null" && m.ReadOnly {
			out[m.Container] = true
		}
	}
	return out
}

func TestMountPlanInputOutputFirewallMasksNestedEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, ".env"))
	touch(t, filepath.Join(root, "svc", "api", ".env")) // nested
	touch(t, filepath.Join(root, ".env.local"))
	touch(t, filepath.Join(root, ".env.example"))              // template: must stay readable
	touch(t, filepath.Join(root, "node_modules", "p", ".env")) // pruned
	got, _ := MountSpec{Workspace: manifest.Workspace{Layout: "input-output"}, InputDir: root, OutputDir: filepath.Join(root, "reports"), EgressMode: "firewall"}.Plan()

	masked := maskedEnvSet(got)
	for _, want := range []string{"/workspace/input/.env", "/workspace/input/svc/api/.env", "/workspace/input/.env.local"} {
		if !masked[want] {
			t.Errorf("expected %s masked, got masks=%v", want, masked)
		}
	}
	if masked["/workspace/input/.env.example"] {
		t.Error(".env.example is a template and must stay readable (not masked)")
	}
	if masked["/workspace/input/node_modules/p/.env"] {
		t.Error("node_modules must be pruned from env masking")
	}
}

func TestMountPlanAppFirewallMasksNestedEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, ".env"))
	touch(t, filepath.Join(root, "packages", "worker", ".env")) // nested per-package
	touch(t, filepath.Join(root, "node_modules", "x", ".env"))  // pruned
	got, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "firewall"}.Plan()

	masked := maskedEnvSet(got)
	if !masked["/app/.env"] || !masked["/app/packages/worker/.env"] {
		t.Errorf("nested .env not fully masked: %v", masked)
	}
	if masked["/app/node_modules/x/.env"] {
		t.Error("node_modules must be pruned")
	}
}

func TestMountPlanAppSubdirFirewallMasksEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, ".env"))
	touch(t, filepath.Join(scope, "sub", ".env"))
	got, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: scope, EgressMode: "firewall"}.Plan()

	// The scope is mounted at /app/apps/web; its .env files are masked under that base.
	masked := maskedEnvSet(got)
	if !masked["/app/apps/web/.env"] || !masked["/app/apps/web/sub/.env"] {
		t.Errorf("subdir .env not masked at scope base: %v", masked)
	}
}

func TestMountPlanAppWholeRepoSymlinkEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	secrets := filepath.Join(t.TempDir(), "secrets.env")
	touch(t, secrets)
	if err := os.Symlink(secrets, filepath.Join(root, ".env")); err != nil {
		t.Fatal(err)
	}

	got, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "broker"}.Plan()
	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	m, ok := byContainer["/app/.env"]
	if !ok || !m.ReadOnly {
		t.Fatalf(".env overlay missing or not ro: %+v (ok=%v)", m, ok)
	}
	if m.Host != secrets {
		t.Errorf(".env host = %q, want resolved target %q", m.Host, secrets)
	}
}

func TestMountPlanAppSubdir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "package.json"))
	touch(t, filepath.Join(root, "pnpm-workspace.yaml"))
	touch(t, filepath.Join(root, ".cursor", "cli.json"))
	touch(t, filepath.Join(root, ".env"))
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "package.json")) // scope has its own package.json

	got, wd := MountSpec{
		Workspace: manifest.Workspace{Layout: "app", ConfigDir: ".cursor", GitMode: "rw"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "broker",
	}.Plan()

	if wd != "/app" {
		t.Fatalf("workdir = %q, want /app", wd)
	}
	// Index the produced mounts by container path for order-independent assertions.
	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	// scope mounted at /app/apps/web (rw)
	if m, ok := byContainer["/app/apps/web"]; !ok || m.Host != scope || m.ReadOnly {
		t.Errorf("scope mount = %+v (ok=%v), want host=%s rw at /app/apps/web", m, ok, scope)
	}
	// root .git mounted rw
	if m, ok := byContainer["/app/.git"]; !ok || m.ReadOnly {
		t.Errorf(".git mount = %+v (ok=%v), want rw", m, ok)
	}
	// root pnpm-workspace.yaml preserved ro (scope lacks it)
	if m, ok := byContainer["/app/pnpm-workspace.yaml"]; !ok || !m.ReadOnly {
		t.Errorf("pnpm-workspace.yaml not preserved ro: %+v (ok=%v)", m, ok)
	}
	// scope HAS its own package.json → root package.json NOT preserved
	if m, ok := byContainer["/app/package.json"]; ok {
		t.Errorf("root package.json should not be mounted (scope has its own): %+v", m)
	}
	// .cursor config dir preserved ro; .env preserved ro
	if _, ok := byContainer["/app/.cursor"]; !ok {
		t.Error(".cursor config dir not preserved")
	}
	if m, ok := byContainer["/app/.env"]; !ok || !m.ReadOnly {
		t.Errorf(".env not preserved ro: %+v (ok=%v)", m, ok)
	}
}

func TestMountPlanAppGitROAndOutput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := filepath.Join(root, "svc")
	touch(t, filepath.Join(scope, "go.mod"))
	got, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app", GitMode: "ro", Output: true},
		RepoRoot:  root, InputDir: scope, OutputDir: "/out",
	}.Plan()
	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	if m, ok := byContainer["/app/.git"]; !ok || !m.ReadOnly {
		t.Errorf("gitMode=ro should mount .git read-only: %+v (ok=%v)", m, ok)
	}
	if m, ok := byContainer["/app/output"]; !ok || m.Host != "/out" || m.ReadOnly {
		t.Errorf("output mount = %+v (ok=%v), want /out rw at /app/output", m, ok)
	}
}

func TestMountPlanAppNonRepoReadOnly(t *testing.T) {
	t.Parallel()
	// Mode:"ro" (now wired via manifest.Workspace, D6) makes the /app mount read-only.
	got, wd := MountSpec{Workspace: manifest.Workspace{Layout: "app", Mode: "ro"}, InputDir: "/somedir"}.Plan()
	want := []runner.Mount{{Host: "/somedir", Container: "/app", ReadOnly: true}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("app non-repo ro mounts mismatch (-want +got):\n%s", diff)
	}
	if wd != "/app" {
		t.Errorf("workdir = %q, want /app", wd)
	}
}

func TestEnvFileSourcePrefersRepoRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(root, ".env")
	if err := os.WriteFile(envPath, []byte("CURSOR_API_KEY=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := EnvFileSource(sub, sub, root)
	if got != envPath {
		t.Fatalf("EnvFileSource(sub, sub, root) = %q, want %q", got, envPath)
	}
}

func TestEnvFileSourcePrefersInvocationWD(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := filepath.Join(root, "scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	pwdEnv := filepath.Join(root, ".env")
	if err := os.WriteFile(pwdEnv, []byte("CURSOR_API_KEY=from-pwd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scopeEnv := filepath.Join(scope, ".env")
	if err := os.WriteFile(scopeEnv, []byte("CURSOR_API_KEY=from-scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := EnvFileSource(root, scope, "")
	if got != pwdEnv {
		t.Fatalf("EnvFileSource(pwd, scope, \"\") = %q, want pwd %q", got, pwdEnv)
	}
}
