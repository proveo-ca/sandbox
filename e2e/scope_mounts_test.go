//go:build e2e

// SPEC: _spec/internal/workspace/subdir-scope-mounts.puml, _spec/packages/lib/dependency-trees.puml

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestScopeCarriesRootSpecDir(t *testing.T) {
	const target = "opencode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	for _, tc := range []struct {
		mode  string
		scope string
	}{
		{"root", ""},
		{"subproject", "apps"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			repo := newTempMonorepo(t)
			input := repo
			if tc.scope != "" {
				input = filepath.Join(repo, tc.scope)
			}
			k := launchKit(t, proveoBin, target, input, nil)

			script := `root=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "NO_REPO_ROOT"; exit 1; }
[ -d "$root/_spec" ] || { echo "MISSING_SPEC_DIR"; exit 1; }
grep -q x "$root/_spec/c.puml" || { echo "SPEC_UNREADABLE"; exit 1; }
printf 'edited by the agent\n' > "$root/_spec/written.puml" || { echo "SPEC_READONLY"; exit 1; }
rm -f "$root/_spec/written.puml"
[ -d "$root/apps/web" ] || { echo "MISSING_SCOPE"; exit 1; }
echo "SPEC_OK"`

			out, status := k.exec(script, 60*time.Second)
			if status != 0 || !strings.Contains(out, "SPEC_OK") {
				t.Fatalf("%s scope cannot reach a writable _spec (exit %d):\n%s", tc.mode, status, out)
			}
		})
	}
}

func TestSubdirScopeOmitsUnmountedPaths(t *testing.T) {
	const target = "opencode"
	requireHarness(t, target)
	repo := newTempMonorepo(t)
	k := launchKit(t, buildProveo(t), target, filepath.Join(repo, "apps"), nil)

	script := `[ -e libs ] && { echo "LIBS_PRESENT"; exit 1; }
[ -e unmounted ] && { echo "UNMOUNTED_PRESENT"; exit 1; }
echo "NARROWED_OK"`

	out, status := k.exec(script, 60*time.Second)
	if status != 0 || !strings.Contains(out, "NARROWED_OK") {
		t.Fatalf("a subdir scope must not carry unrelated repo paths (exit %d):\n%s", status, out)
	}
}

func newTempWorktree(t *testing.T) (worktree string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "monorepo")
	wt := filepath.Join(base, "worktrees", "hotfix")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(main, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(main, "init", "-q", ".")
	run(main, "config", "user.email", "e2e@proveo.test")
	run(main, "config", "user.name", "proveo e2e")
	write(".gitignore", "node_modules\n.env\n")
	write(".env", "SECRET=from-monorepo\n")
	write("package.json", `{"name":"m","scripts":{"test":"echo t"}}`+"\n")
	run(main, "add", "-A")
	run(main, "commit", "-q", "-m", "seed")
	run(main, "worktree", "add", "-q", wt, "-b", "hotfix")
	if err := os.Symlink("../../monorepo/.env", filepath.Join(wt, ".env")); err != nil {
		t.Fatal(err)
	}
	return wt
}

// TestWorktreeWorkspaceIsFullyUsable drives a real `proveo run claudecode`
// through its kit and probes it with `sbx exec`: the git-safe-dir bridging
// under test is the seed's own, not a manually re-invoked copy of it. It asserts
// the MOUNTED worktree, which since clone-through-main is the explicit opt-out.
func TestWorktreeWorkspaceIsFullyUsable(t *testing.T) {
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)
	wt := newTempWorktree(t)

	k := launchKit(t, proveoBin, target, wt, []string{"PROVEO_DEPS=shared"}, "--clone=false")
	script := `grep -q from-monorepo .env || { echo "ENV_UNREACHABLE"; exit 1; }
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "NOT_A_REPO"; exit 1; }
[ "$(git rev-parse --abbrev-ref HEAD)" = hotfix ] || { echo "WRONG_BRANCH"; exit 1; }
unexpected=$(git status --porcelain | grep -v '^?? AGENTS\.md$' || true)
[ -z "$unexpected" ] || { echo "DIRTY: $(printf '%s' "$unexpected" | head -2)"; exit 1; }
proveo-entrypoint verify "$PWD" 2>/dev/null | grep -q . || { echo "NO_VERIFY_COMMANDS"; exit 1; }
touch AGENTS.md.probe 2>/dev/null || { echo "WORKSPACE_READONLY"; exit 1; }
rm -f AGENTS.md.probe
echo "WORKTREE_OK"`

	out, status := k.exec(script, 60*time.Second)
	if status != 0 || !strings.Contains(out, "WORKTREE_OK") {
		t.Fatalf("worktree workspace unusable (exit %d):\n%s", status, out)
	}

	seeded, _ := k.exec(`git status --porcelain | head -5`, 30*time.Second)
	if !strings.Contains(seeded, "?? AGENTS.md") {
		t.Errorf("claudecode left the worktree untouched, but its seed writes AGENTS.md "+
			"into a workspace that carries none — the seed either stopped running or wrote elsewhere:\n%s", seeded)
	}
}

// TestMountedWorktreeRefusesAForeignPlatformInstall pins the gate: a mounted
// checkout on another platform would take a Linux install into the host tree.
func TestMountedWorktreeRefusesAForeignPlatformInstall(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("a linux host of the image's arch runs its trees as-is; the gate passes")
	}
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireHarness(t, target)
	wt := newTempWorktree(t)
	run := func(env ...string) (string, error) {
		cmd := exec.Command(buildProveo(t), "run", target, "--input", wt, "--clone=false", "--print")
		cmd.Env = append(os.Environ(), append([]string{"PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_DEPS="}, env...)...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run()
	if err == nil || !strings.Contains(out, "PROVEO_DEPS=shared") || !strings.Contains(out, "--clone") {
		t.Fatalf("a mounted worktree on %s/%s must refuse and name both remedies (err=%v):\n%s", runtime.GOOS, runtime.GOARCH, err, out)
	}
	if out, err := run("PROVEO_DEPS=shared"); err != nil {
		t.Fatalf("PROVEO_DEPS=shared is consent and must let the run through: %v\n%s", err, out)
	}
}

// rootLibProbe runs a script against entrypoint-lib.sh inside the image, as
// ROOT, with dir bind-mounted at /w.
//
// ROOT IS DELIBERATE — these probes exercise provisioning paths that install
// packages — and it is also why this helper exists rather than four copies of
// the invocation. A root container writing into a `t.TempDir()` leaves files the
// TEST USER cannot unlink, and Go's TempDir cleanup then fails the test AFTER
// its assertions have already passed. MEASURED: the absent-node_modules probe
// below has no node_modules, so ensure_dependency_trees auto-installs, and
// `pnpm install` left /w/.pnpm-store and /w/node_modules owned by root:root —
// `TempDir RemoveAll cleanup: unlinkat .../.pnpm-store/v10/files/7b: permission
// denied`, on a subtest whose own check had passed.
//
// So ownership is handed back before the container exits. The trap fires on
// every exit path, including the `exit 1`s the scripts use to report failure, so
// a FAILING probe cleans up too — otherwise the first real failure here would be
// followed by a cleanup failure masking it.
func rootLibProbe(t *testing.T, img, dir, script string) (string, error) {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", "--user", "root",
		"-v", entrypointLibPath(t)+":/lib.sh:ro", "-v", dir+":/w", "-w", "/w",
		"--entrypoint", "bash", img, "-c",
		"export HOME=/tmp/h; mkdir -p $HOME\n"+
			"trap 'chown -R "+hostUIDGID(t)+" /w 2>/dev/null || true' EXIT\n"+
			"source /lib.sh 2>/dev/null\n"+script).CombinedOutput()
	return string(out), err
}

func TestPythonEnvironmentIsProvisioned(t *testing.T) {
	img := harnessImage(t, "opencode")
	dir := t.TempDir()
	for name, body := range map[string]string{
		"requirements.txt": "requests\n",
		".python-version":  "3.12\n",
		"app.py":           "import requests\nprint(requests.__version__)\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A host-built .venv whose interpreter cannot run here must not be adopted.
	if err := os.MkdirAll(filepath.Join(dir, ".venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/host/python3", filepath.Join(dir, ".venv", "bin", "python")); err != nil {
		t.Fatal(err)
	}

	out, err := rootLibProbe(t, img, dir, `ensure_python_env /w
[ -n "$VIRTUAL_ENV" ] || { echo "NO_VIRTUAL_ENV"; exit 1; }
case "$VIRTUAL_ENV" in /w/*) echo "ENV_INSIDE_WORKSPACE"; exit 1 ;; esac
python -c 'import requests' 2>/dev/null || { echo "DEP_UNIMPORTABLE"; exit 1; }
pyright --outputjson app.py 2>/dev/null | grep -q reportMissingModuleSource && { echo "PYRIGHT_UNRESOLVED"; exit 1; }
echo "PYENV_OK"`)

	if err != nil || !strings.Contains(out, "PYENV_OK") {
		t.Fatalf("python environment not provisioned correctly: %v\n%s", err, out)
	}
}

// A workspace with no Python markers must not provision anything.
func TestPythonEnvironmentSkipsNonPythonWorkspaces(t *testing.T) {
	img := harnessImage(t, "opencode")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := rootLibProbe(t, img, dir, `ensure_python_env /w
[ -z "$VIRTUAL_ENV" ] || { echo "PROVISIONED_ANYWAY"; exit 1; }
[ -d "$HOME/.cache/proveo/venv" ] && { echo "VENV_DIR_CREATED"; exit 1; }
echo "SKIP_OK"`)
	if err != nil || !strings.Contains(out, "SKIP_OK") {
		t.Fatalf("non-Python workspace triggered provisioning: %v\n%s", err, out)
	}
}

func TestPythonEnvironmentIsProvisionedForNestedProject(t *testing.T) {
	img := harnessImage(t, "opencode")
	dir := t.TempDir()
	for name, body := range map[string]string{
		"package.json":        `{"name":"monorepo"}`,
		"pnpm-workspace.yaml": "packages:\n  - apps/*\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc := filepath.Join(dir, "apps", "svc")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"requirements.txt": "requests\n",
		".python-version":  "3.12\n",
		"app.py":           "import requests\nprint(requests.__version__)\n",
	} {
		if err := os.WriteFile(filepath.Join(svc, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Deeper than the default scan depth: named, never built.
	deep := filepath.Join(dir, "a", "b", "c", "d", "e")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "pyproject.toml"), []byte("[project]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := rootLibProbe(t, img, dir, `roots="$(_py_project_roots /w)"
[ "$roots" = "/w/apps/svc" ] || { echo "BAD_ROOTS[$roots]"; exit 1; }
ensure_python_env /w
[ -n "$VIRTUAL_ENV" ] || { echo "NO_VIRTUAL_ENV"; exit 1; }
case "$VIRTUAL_ENV" in /w/*) echo "ENV_INSIDE_WORKSPACE"; exit 1 ;; esac
python -c 'import requests' 2>/dev/null || { echo "DEP_UNIMPORTABLE"; exit 1; }
echo "NESTED_OK"`)

	if err != nil || !strings.Contains(out, "NESTED_OK") {
		t.Fatalf("nested Python project not provisioned: %v\n%s", err, out)
	}
}

func TestHostBuiltDependencyTreesAreReported(t *testing.T) {
	img := harnessImage(t, "opencode")

	// Mach-O 64-bit LE: built on the macOS host, unloadable in a Linux container.
	machO := []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x01}
	elf := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}

	write := func(t *testing.T, path string, body []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(t *testing.T, dir, script string) string {
		t.Helper()
		out, err := rootLibProbe(t, img, dir, script)
		if err != nil {
			t.Fatalf("probe failed: %v\n%s", err, out)
		}
		return out
	}

	// One workspace, one pass: each language's tree must be found where its own
	// ecosystem puts it, and each package named the way that ecosystem names it.
	t.Run("every language is probed and named", func(t *testing.T) {
		dir := t.TempDir()
		// typescript — pnpm's content-addressed store, plus a scoped package.
		write(t, filepath.Join(dir, "package.json"), []byte(`{"name":"m"}`))
		write(t, filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"))
		write(t, filepath.Join(dir, "node_modules/.pnpm/nx@22.7.8/node_modules/nx/src/native/nx.darwin-arm64.node"), machO)
		write(t, filepath.Join(dir, "node_modules/@scope/thing/build/thing.node"), machO)
		write(t, filepath.Join(dir, "node_modules/native-ok/ok.node"), elf)
		// ruby — nested, and a .bundle extension macOS uses and Linux does not.
		write(t, filepath.Join(dir, "svc/api/Gemfile"), []byte("source 'https://rubygems.org'\n"))
		write(t, filepath.Join(dir, "svc/api/vendor/bundle/ruby/3.3.0/gems/nokogiri-1.16.0/lib/nokogiri/nokogiri.bundle"), machO)
		// terraform — provider binaries, keyed by namespace/name.
		write(t, filepath.Join(dir, "infra/.terraform.lock.hcl"), []byte("# lock\n"))
		write(t, filepath.Join(dir, "infra/.terraform/providers/registry.terraform.io/hashicorp/aws/5.0.0/darwin_arm64/terraform-provider-aws"), machO)
		// lua — luarocks keys by module, and the first path segment is "lib".
		write(t, filepath.Join(dir, "lua/app.rockspec"), []byte("package='app'\n"))
		write(t, filepath.Join(dir, "lua/lua_modules/lib/lua/5.4/cjson.so"), machO)
		// rust — build output, so the remedy is removal, not a reinstall.
		write(t, filepath.Join(dir, "rs/Cargo.toml"), []byte("[package]\nname=\"x\"\n"))
		write(t, filepath.Join(dir, "rs/target/debug/deps/x.o"), machO)

		got := run(t, dir, `ensure_dependency_trees /w`)
		for _, want := range []string{
			"nx@22.7.8", "@scope/thing", // typescript
			"nokogiri-1.16.0",   // ruby
			"hashicorp/aws",     // terraform
			"cjson",             // lua
			"rust build output", // rust takes the artifacts path, not addons
		} {
			if !strings.Contains(got, want) {
				t.Errorf("report is missing %q:\n%s", want, got)
			}
		}
		// Build output needs no registry, so offering a reinstall for it would
		// send the operator down a path that cannot help.
		if strings.Contains(got, "target was built on the host") {
			t.Errorf("rust build output was reported as a reinstallable tree:\n%s", got)
		}
		// The ELF addon runs here; naming it would send the operator after a
		// package that is not the problem.
		if strings.Contains(got, "native-ok") {
			t.Errorf("a container-native addon was reported as foreign:\n%s", got)
		}
	})

	t.Run("native addons stay silent", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "package.json"), []byte(`{"name":"m"}`))
		write(t, filepath.Join(dir, "package-lock.json"), []byte("{}"))
		write(t, filepath.Join(dir, "node_modules/native-ok/ok.node"), elf)

		if got := run(t, dir, `ensure_dependency_trees /w; echo DONE`); strings.Contains(got, "built on the host") {
			t.Errorf("an all-ELF tree was reported as foreign:\n%s", got)
		}
	})

	t.Run("off disables the probe", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "package.json"), []byte(`{"name":"m"}`))
		write(t, filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"))
		write(t, filepath.Join(dir, "node_modules/x/x.node"), machO)

		if got := run(t, dir, `PROVEO_DEPS=off ensure_dependency_trees /w; echo DONE`); strings.Contains(got, "built on the host") {
			t.Errorf("PROVEO_DEPS=off still probed:\n%s", got)
		}
	})

	// A JS workspace with nothing installed is a distinct, louder failure: it is
	// not "some bindings will not load", it is "nothing resolves".
	t.Run("absent node_modules is called out", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "package.json"), []byte(`{"name":"m"}`))
		write(t, filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"))

		if got := run(t, dir, `ensure_dependency_trees /w`); !strings.Contains(got, "no node_modules") {
			t.Errorf("missing node_modules went unreported:\n%s", got)
		}
	})
}
