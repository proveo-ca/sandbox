package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMountPlanAppWholeRepo(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	touch(t, filepath.Join(root, ".env"))
	got, wd, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "open", Credentials: "forward"}.Plan()
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
	root := tempDir(t)
	touch(t, filepath.Join(root, ".env"))
	got, _, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "allowlist"}.Plan()
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

func TestMountPlanAppFirewallMasksNestedEnv(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	touch(t, filepath.Join(root, ".env"))
	touch(t, filepath.Join(root, "packages", "worker", ".env")) // nested per-package
	touch(t, filepath.Join(root, "node_modules", "x", ".env"))  // pruned
	got, _, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "allowlist"}.Plan()

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
	root := tempDir(t)
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, ".env"))
	touch(t, filepath.Join(scope, "sub", ".env"))
	got, _, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: scope, EgressMode: "allowlist"}.Plan()

	// The scope is mounted at /app/apps/web; its .env files are masked under that base.
	masked := maskedEnvSet(got)
	if !masked["/app/apps/web/.env"] || !masked["/app/apps/web/sub/.env"] {
		t.Errorf("subdir .env not masked at scope base: %v", masked)
	}
}

func TestMountPlanAppWholeRepoSymlinkEnv(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	secrets := filepath.Join(tempDir(t), "secrets.env")
	touch(t, secrets)
	if err := os.Symlink(secrets, filepath.Join(root, ".env")); err != nil {
		t.Fatal(err)
	}

	got, _, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, RepoRoot: root, InputDir: root, EgressMode: "open", Credentials: "forward"}.Plan()
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
	root := tempDir(t)
	touch(t, filepath.Join(root, "package.json"))
	touch(t, filepath.Join(root, "pnpm-workspace.yaml"))
	touch(t, filepath.Join(root, ".cursor", "cli.json"))
	touch(t, filepath.Join(root, ".env"))
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "package.json")) // scope has its own package.json

	got, wd, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app", ConfigDir: ".cursor", GitMode: "rw"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "open", Credentials: "forward",
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
	root := tempDir(t)
	scope := filepath.Join(root, "svc")
	touch(t, filepath.Join(scope, "go.mod"))
	got, _, _ := MountSpec{
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
	got, wd, _ := MountSpec{Workspace: manifest.Workspace{Layout: "app", Mode: "ro"}, InputDir: "/somedir"}.Plan()
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
	root := tempDir(t)
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
	root := tempDir(t)
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

func TestPlanSubdirPreservesRootDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "_spec", "components.puml"))
	touch(t, filepath.Join(root, "vendor", "modules.txt"))
	touch(t, filepath.Join(root, "node_modules", ".modules.yaml"))
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "index.ts"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "open", Credentials: "forward",
		MountRootDeps: true,
	}.Plan()

	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	for _, dir := range []string{"/app/_spec", "/app/vendor", "/app/node_modules"} {
		m, ok := byContainer[dir]
		if !ok {
			t.Errorf("%s not mounted into the subdir scope", dir)
			continue
		}
		if m.ReadOnly {
			t.Errorf("%s mounted read-only; agents edit specs and installs write to deps", dir)
		}
	}
}

func TestPlanSubdirRootDepsAreOptional(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "_spec", "components.puml"))
	touch(t, filepath.Join(root, "node_modules", ".modules.yaml"))
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "index.ts"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "open", Credentials: "forward",
		MountRootDeps: false,
	}.Plan()

	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	if _, ok := byContainer["/app/node_modules"]; ok {
		t.Error("root node_modules mounted despite MountRootDeps=false")
	}
	if _, ok := byContainer["/app/_spec"]; !ok {
		t.Error("_spec must survive the dependency opt-out — it is not a dependency")
	}
}

func TestPlanSubdirRootDirsYieldToScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "node_modules", ".modules.yaml"))
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "node_modules", ".modules.yaml"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "open", Credentials: "forward",
		MountRootDeps: true,
	}.Plan()

	for _, m := range got {
		if m.Container == "/app/node_modules" {
			t.Errorf("root node_modules mounted over a scope that has its own: %+v", m)
		}
	}
}

func TestPlanSubdirIgnoresRootDirNamedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "_spec")) // a file, not a directory
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "index.ts"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	for _, m := range got {
		if m.Container == "/app/_spec" {
			t.Errorf("a file named _spec was mounted as the specs directory: %+v", m)
		}
	}
}

func TestPlanSubdirRootDirsAreEnvMasked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "_spec", "components.puml"))
	touch(t, filepath.Join(root, "vendor", "pkg", ".env"))
	touch(t, filepath.Join(root, "vendor", "pkg", ".env.example"))
	scope := filepath.Join(root, "apps", "web")
	touch(t, filepath.Join(scope, "index.ts"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app"},
		RepoRoot:  root, InputDir: scope,
		EgressMode: "allowlist", // isolates env
	}.Plan()

	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	m, ok := byContainer["/app/vendor/pkg/.env"]
	if !ok {
		t.Fatal("a .env inside a mounted root dir was not masked")
	}
	if m.Host != os.DevNull || !m.ReadOnly {
		t.Errorf(".env mask = %+v, want %s read-only", m, os.DevNull)
	}
	if _, masked := byContainer["/app/vendor/pkg/.env.example"]; masked {
		t.Error(".env.example must stay readable, not be masked")
	}
}

func TestPlanRootScopeHonorsGitModeRO(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, ".git", "HEAD"))
	touch(t, filepath.Join(root, "main.go"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app", GitMode: "ro"},
		RepoRoot:  root, InputDir: root,
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	byContainer := map[string]runner.Mount{}
	for _, m := range got {
		byContainer[m.Container] = m
	}
	if m, ok := byContainer["/app"]; !ok || m.ReadOnly {
		t.Errorf("/app = %+v (ok=%v), want the tree writable", m, ok)
	}
	m, ok := byContainer["/app/.git"]
	if !ok {
		t.Fatalf("gitMode=ro at repo root must pin /app/.git read-only; mounts=%+v", got)
	}
	if !m.ReadOnly || m.Host != filepath.Join(root, ".git") {
		t.Errorf("/app/.git = %+v, want host %s read-only", m, filepath.Join(root, ".git"))
	}
}

func TestPlanRootScopeAddsNoGitMountWhenModesAgree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, ".git", "HEAD"))

	for _, gm := range []string{"", "rw"} {
		got, _, _ := MountSpec{
			Workspace: manifest.Workspace{Layout: "app", GitMode: gm},
			RepoRoot:  root, InputDir: root,
			EgressMode: "open", Credentials: "forward",
		}.Plan()
		for _, m := range got {
			if m.Container == "/app/.git" {
				t.Errorf("gitMode=%q matches the parent mount; the extra bind is noise: %+v", gm, m)
			}
		}
	}
}

func TestPlanGitModeIgnoredWithoutAGitDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	touch(t, filepath.Join(root, "main.go"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Layout: "app", GitMode: "ro"},
		RepoRoot:  root, InputDir: root,
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	for _, m := range got {
		if strings.HasSuffix(m.Container, "/.git") {
			t.Errorf("no .git on disk, yet one was mounted: %+v", m)
		}
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestMountPlanMountsEscapingSymlink(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	root, outside := filepath.Join(base, "repo"), filepath.Join(base, "specs", "pluvo")
	touch(t, filepath.Join(outside, "topology.puml"))
	touch(t, filepath.Join(root, "README.md"))
	symlink(t, outside, filepath.Join(root, "_spec"))

	got, _, links := MountSpec{
		Workspace: manifest.Workspace{Output: true},
		InputDir:  root, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	// Order follows Plan(): scope → worktree → envOverlay → output → LINK mounts.
	want := []runner.Mount{
		{Host: root, Container: "/app"},
		{Host: filepath.Join(base, "out"), Container: "/app/output"},
		{Host: outside, Container: "/app/_spec"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Plan() with _spec -> %s mounts mismatch (-want +got):\n%s", outside, diff)
	}
	wantLinks := []Link{{Rel: "_spec", Target: outside, Action: LinkMounted}}
	if diff := cmp.Diff(wantLinks, links); diff != "" {
		t.Errorf("Plan() with _spec -> %s links mismatch (-want +got):\n%s", outside, diff)
	}
}

func TestMountPlanEscapingSymlinkFollowsWorkspaceMode(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	root, outside := filepath.Join(base, "repo"), filepath.Join(base, "specs")
	touch(t, filepath.Join(outside, "a.puml"))
	touch(t, filepath.Join(root, "go.mod"))
	symlink(t, outside, filepath.Join(root, "_spec"))

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Mode: "ro"},
		InputDir:  root, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	want := runner.Mount{Host: outside, Container: "/app/_spec", ReadOnly: true}
	if diff := cmp.Diff(want, got[len(got)-1]); diff != "" {
		t.Errorf("Plan() mode=ro link mount mismatch (-want +got):\n%s", diff)
	}
}

func TestMountPlanIgnoresSymlinkResolvingInsideTree(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	root := filepath.Join(base, "repo")
	touch(t, filepath.Join(root, "real", "keep.md"))
	symlink(t, "real", filepath.Join(root, "alias"))
	symlink(t, filepath.Join(root, "real"), filepath.Join(root, "abs_alias"))

	got, _, links := MountSpec{
		Workspace: manifest.Workspace{Output: true},
		InputDir:  root, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	if len(got) != 2 {
		t.Errorf("Plan() with in-tree symlinks = %d mounts, want 2 (input+output): %+v", len(got), got)
	}
	if len(links) != 0 {
		t.Errorf("Plan() with in-tree symlinks reported %+v, want no links", links)
	}
}

func TestMountPlanRefusesUnsafeOrBrokenLinks(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	root := filepath.Join(base, "repo")
	touch(t, filepath.Join(root, "go.mod"))
	symlink(t, filepath.Join(base, "gone"), filepath.Join(root, "broken"))
	symlink(t, base, filepath.Join(root, "up"))

	_, _, links := MountSpec{
		Workspace: manifest.Workspace{Output: true},
		InputDir:  root, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	want := []Link{
		{Rel: "broken", Action: LinkRefused, Reason: "target does not resolve on the host either"},
		{Rel: "up", Target: base, Action: LinkRefused, Reason: "target contains the workspace"},
	}
	if diff := cmp.Diff(want, links); diff != "" {
		t.Errorf("Plan() with broken + parent links mismatch (-want +got):\n%s", diff)
	}
}

func TestRefuseLinkTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, target, root, want string
	}{
		{"sibling tree outside the workspace", "/host/specs/pluvo", "/host/repo", ""},
		{"parent of the workspace", "/host", "/host/repo", "target contains the workspace"},
		{"filesystem root", "/", "/host/repo", "target contains the workspace"},
		{"monorepo root above a subdir scope", "/host/mono", "/host/mono/apps/web", "target contains the workspace"},
		{"top-level system directory", "/etc", "/elsewhere/repo", "target is a top-level system directory"},
		{"documented limit: two-segment target elsewhere", "/Users/other", "/tmp/repo", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := refuseLinkTarget(tc.target, tc.root); got != tc.want {
				t.Errorf("refuseLinkTarget(%q, %q) = %q, want %q", tc.target, tc.root, got, tc.want)
			}
		})
	}
}

func TestMountPlanLeavesSymlinkedDotenvToCredentialPolicy(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	root, hostEnv := filepath.Join(base, "repo"), filepath.Join(base, "base.env")
	touch(t, hostEnv)
	touch(t, filepath.Join(root, "go.mod"))
	symlink(t, hostEnv, filepath.Join(root, ".env"))
	spec := MountSpec{
		Workspace: manifest.Workspace{Output: true},
		InputDir:  root, OutputDir: filepath.Join(base, "out"),
	}

	spec.EgressMode, spec.Credentials = "open", "forward"
	got, _, links := spec.Plan()
	// The .env target appears TWICE, and that is pre-existing rather than new:
	// envMounts resolves the symlink for the scope mount and envOverlay carries the
	// escaping target in, and both fire for a .env that points outside the
	// workspace. Docker accepts two identical binds, so it is a redundancy and not
	// a fault — encoded here so the next reader does not take it for one. (It is
	// also the shape of this repo's own .env, which is a symlink to ~/base.env.)
	wantForward := []runner.Mount{
		{Host: root, Container: "/app"},
		{Host: hostEnv, Container: "/app/.env", ReadOnly: true},
		{Host: hostEnv, Container: "/app/.env", ReadOnly: true},
		{Host: filepath.Join(base, "out"), Container: "/app/output"},
	}
	if diff := cmp.Diff(wantForward, got); diff != "" {
		t.Errorf("Plan() credentials=forward with symlinked .env mismatch (-want +got):\n%s", diff)
	}
	wantLinks := []Link{{Rel: ".env", Action: LinkEnvPolicy, Reason: "placed by the credential policy, not by link resolution"}}
	if diff := cmp.Diff(wantLinks, links); diff != "" {
		t.Errorf("Plan() credentials=forward with symlinked .env links mismatch (-want +got):\n%s", diff)
	}

	spec.EgressMode, spec.Credentials = "allowlist", "broker"
	got, _, _ = spec.Plan()
	wantIsolated := []runner.Mount{
		{Host: root, Container: "/app"},
		{Host: "/dev/null", Container: "/app/.env", ReadOnly: true},
		{Host: filepath.Join(base, "out"), Container: "/app/output"},
	}
	if diff := cmp.Diff(wantIsolated, got); diff != "" {
		t.Errorf("Plan() isolating egress with symlinked .env mismatch (-want +got):\n%s", diff)
	}
}

func TestMountPlanSkipsLinksInPrunedAndDeepDirs(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	root, outside := filepath.Join(base, "repo"), filepath.Join(base, "outside")
	touch(t, filepath.Join(outside, "x.md"))
	touch(t, filepath.Join(root, "go.mod"))
	symlink(t, outside, filepath.Join(root, "node_modules", "pkg_link"))
	symlink(t, outside, filepath.Join(root, "a", "b", "c", "deep_link"))
	symlink(t, outside, filepath.Join(root, "a", "b", "shallow_link"))

	_, _, links := MountSpec{
		Workspace: manifest.Workspace{Output: true},
		InputDir:  root, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	want := []Link{{Rel: "a/b/shallow_link", Target: outside, Action: LinkMounted}}
	if diff := cmp.Diff(want, links); diff != "" {
		t.Errorf("Plan() with pruned + deep links mismatch (-want +got):\n%s", diff)
	}
}

// linkedWorktree builds the on-disk shape `git worktree add` produces: the tree's
// .git is a FILE pointing at <main>/.git/worktrees/<name>, whose commondir points
// back up to the shared .git. Returns the worktree tree and the common dir.
func linkedWorktree(t *testing.T, base, name string) (tree, common string) {
	t.Helper()
	common = filepath.Join(base, "main", ".git")
	admin := filepath.Join(common, "worktrees", name)
	tree = filepath.Join(base, name)
	touch(t, filepath.Join(common, "HEAD"))
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The host pointers hold HOST paths — the very thing the overlay replaces.
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(tree, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(tree, "README.md"))
	if err := os.WriteFile(filepath.Join(tree, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return tree, common
}

func TestPrepareWorktreeLinksWritesContainerPointers(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	tree, _ := linkedWorktree(t, base, "wt")
	root := filepath.Join(base, "proveo-home")

	spec := MountSpec{Workspace: manifest.Workspace{}, InputDir: tree}
	dir, err := spec.PrepareWorktreeLinks(root)
	if err != nil {
		t.Fatalf("PrepareWorktreeLinks: %v", err)
	}
	if dir == "" {
		t.Fatal("PrepareWorktreeLinks returned no dir for a linked worktree")
	}

	// The chain must be coherent in CONTAINER terms: .git → admin dir → back.
	for name, want := range map[string]string{
		"dotgit": "gitdir: " + ContainerGitCommonDir + "/worktrees/wt\n",
		"gitdir": "/app/.git\n",
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}

	// The host copies still point at host paths — rewriting them would break the
	// operator's own use of the worktree.
	b, err := os.ReadFile(filepath.Join(tree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), ContainerGitCommonDir) {
		t.Errorf("host .git was rewritten to a container path: %q", b)
	}
}

func TestPrepareWorktreeLinksUsesAppRootForAppLayout(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	tree, _ := linkedWorktree(t, base, "wt")

	spec := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, InputDir: tree, RepoRoot: tree}
	dir, err := spec.PrepareWorktreeLinks(filepath.Join(base, "proveo-home"))
	if err != nil {
		t.Fatalf("PrepareWorktreeLinks: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gitdir"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "/app/.git\n" {
		t.Errorf("app-layout gitdir = %q, want %q", b, "/app/.git\n")
	}
}

func TestPrepareWorktreeLinksSkipsNonWorktrees(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	root := filepath.Join(base, "repo")
	touch(t, filepath.Join(root, ".git", "HEAD")) // a normal repo: .git is a directory

	for _, tc := range []struct{ name, input, home string }{
		{"normal repo", root, filepath.Join(base, "home")},
		{"no root to write into", root, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := MountSpec{Workspace: manifest.Workspace{Layout: "app"}, InputDir: tc.input, RepoRoot: tc.input}
			dir, err := spec.PrepareWorktreeLinks(tc.home)
			if err != nil {
				t.Fatalf("PrepareWorktreeLinks: %v", err)
			}
			if dir != "" {
				t.Errorf("PrepareWorktreeLinks = %q, want \"\" (nothing to overlay)", dir)
			}
		})
	}
}

func TestPrepareWorktreeLinksKeyIsPerRepoNotPerName(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	// Two DIFFERENT repos, each with a worktree called "feature".
	a, _ := linkedWorktree(t, filepath.Join(base, "a"), "feature")
	b, _ := linkedWorktree(t, filepath.Join(base, "b"), "feature")
	root := filepath.Join(base, "home")

	dirA, err := (MountSpec{Workspace: manifest.Workspace{Layout: "app"}, InputDir: a, RepoRoot: a}).PrepareWorktreeLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dirB, err := (MountSpec{Workspace: manifest.Workspace{Layout: "app"}, InputDir: b, RepoRoot: b}).PrepareWorktreeLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if dirA == dirB {
		t.Errorf("same-named worktrees in different repos share %q; one would overwrite the other", dirA)
	}
}

func TestMountPlanWorktreeOverlaysBothPointerFiles(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	tree, common := linkedWorktree(t, base, "wt")
	links := filepath.Join(base, "links")

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Output: true},
		InputDir:  tree, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
		WorktreeLinkDir: links,
	}.Plan()

	want := []runner.Mount{
		{Host: tree, Container: "/app"},
		{Host: common, Container: ContainerGitCommonDir},
		{Host: filepath.Join(links, "dotgit"), Container: "/app/.git", ReadOnly: true},
		{Host: filepath.Join(links, "gitdir"), Container: ContainerGitCommonDir + "/worktrees/wt/gitdir", ReadOnly: true},
		{Host: filepath.Join(base, "out"), Container: "/app/output"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("worktree mounts mismatch (-want +got):\n%s", diff)
	}
}

func TestMountPlanWorktreeWithoutLinkDirBindsCommonDirOnly(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	tree, common := linkedWorktree(t, base, "wt")

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{Output: true},
		InputDir:  tree, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
	}.Plan()

	want := []runner.Mount{
		{Host: tree, Container: "/app"},
		{Host: common, Container: ContainerGitCommonDir},
		{Host: filepath.Join(base, "out"), Container: "/app/output"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fallback worktree mounts mismatch (-want +got):\n%s", diff)
	}
	// WorktreeEnv is the fallback that this shape depends on.
	wantEnv := []string{"GIT_DIR=" + ContainerGitCommonDir + "/worktrees/wt", "GIT_WORK_TREE=/app"}
	gotEnv := MountSpec{Workspace: manifest.Workspace{}, InputDir: tree}.WorktreeEnv()
	if diff := cmp.Diff(wantEnv, gotEnv); diff != "" {
		t.Errorf("WorktreeEnv mismatch (-want +got):\n%s", diff)
	}
}

func TestMountPlanWorktreeHonorsGitModeRO(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	tree, common := linkedWorktree(t, base, "wt")

	got, _, _ := MountSpec{
		Workspace: manifest.Workspace{GitMode: "ro"},
		InputDir:  tree, OutputDir: filepath.Join(base, "out"),
		EgressMode: "open", Credentials: "forward",
		WorktreeLinkDir: filepath.Join(base, "links"),
	}.Plan()

	var found bool
	for _, m := range got {
		if m.Container != ContainerGitCommonDir {
			continue
		}
		found = true
		if m.Host != common || !m.ReadOnly {
			t.Errorf("gitMode=ro shared .git = %+v, want host %s read-only", m, common)
		}
	}
	if !found {
		t.Fatalf("no %s mount planned for a linked worktree: %+v", ContainerGitCommonDir, got)
	}
}
