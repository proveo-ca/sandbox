//go:build e2e

// SPEC: _spec/internal/workspace/subdir-scope-mounts.puml

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A subdir scope must still carry the repo's shared directories. _spec is the
// one that matters most here: agents are asked to read AND maintain the specs,
// and plantuml is on the floor precisely so they can verify what they edited —
// none of which works if _spec never reaches the container. The mount plan is
// unit-tested; this asserts the container can actually reach it.
func TestScopeCarriesRootSpecDir(t *testing.T) {
	const target = "opencode"
	img := harnessImage(t, target)

	for _, tc := range []struct {
		mode  string
		scope string
	}{
		{"root", ""},
		{"subproject", "apps"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			repo := newTempMonorepo(t)
			args := []string{"run", "--rm"}
			args = append(args, scopedMountArgs(t, target, repo, tc.scope)...)
			args = append(args, "-w", "/app", "--user", hostUIDGID(t),
				"--entrypoint", "bash", img, "-c", `
[ -d /app/_spec ] || { echo "MISSING_SPEC_DIR"; exit 1; }
grep -q x /app/_spec/c.puml || { echo "SPEC_UNREADABLE"; exit 1; }
printf 'edited by the agent\n' > /app/_spec/written.puml || { echo "SPEC_READONLY"; exit 1; }
rm -f /app/_spec/written.puml
[ -d /app/apps/web ] || { echo "MISSING_SCOPE"; exit 1; }
echo "SPEC_OK"`)

			out, err := exec.Command("docker", args...).CombinedOutput()
			s := string(out)
			if err != nil || !strings.Contains(s, "SPEC_OK") {
				t.Fatalf("%s scope cannot reach a writable _spec: %v\n%s", tc.mode, err, s)
			}
		})
	}
}

// The scope must still NARROW the tree — otherwise rootDirs would just be a
// roundabout way of mounting everything, and the git scoping in
// scope_git_worktree would have nothing to hide.
func TestSubdirScopeOmitsUnmountedPaths(t *testing.T) {
	const target = "opencode"
	img := harnessImage(t, target)
	repo := newTempMonorepo(t)

	args := []string{"run", "--rm"}
	args = append(args, scopedMountArgs(t, target, repo, "apps")...)
	args = append(args, "-w", "/app", "--user", hostUIDGID(t),
		"--entrypoint", "bash", img, "-c", `
[ -e /app/libs ] && { echo "LIBS_PRESENT"; exit 1; }
[ -e /app/unmounted ] && { echo "UNMOUNTED_PRESENT"; exit 1; }
echo "NARROWED_OK"`)

	out, err := exec.Command("docker", args...).CombinedOutput()
	s := string(out)
	if err != nil || !strings.Contains(s, "NARROWED_OK") {
		t.Fatalf("a subdir scope must not carry unrelated repo paths: %v\n%s", err, s)
	}
}

// newTempWorktree reproduces the shape that broke: a linked git worktree, whose
// .git is a FILE pointing under the main repo, with .env symlinked back to the
// main checkout. Both references escape the mounted tree.
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

// A linked worktree must behave like any other checkout. Its .git is a pointer
// FILE to a path under the main repo, and its .env a symlink out of the tree —
// neither of which is reachable from a container unless proveo carries them in.
func TestWorktreeWorkspaceIsFullyUsable(t *testing.T) {
	const target = "claudecode"
	img := harnessImage(t, target)
	wt := newTempWorktree(t)

	args := []string{"run", "--rm"}
	args = append(args, workspaceMountArgs(t, target, wt)...)
	args = append(args, worktreeEnvArgs(t, target, wt)...)
	args = append(args,
		"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
		"-w", "/app", "--user", hostUIDGID(t),
		"--entrypoint", "bash", img, "-c", `
source /entrypoint-lib.sh 2>/dev/null || true
ensure_git_safe_directory "$PWD" >/dev/null 2>&1 || true
grep -q from-monorepo .env || { echo "ENV_UNREACHABLE"; exit 1; }
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "NOT_A_REPO"; exit 1; }
[ "$(git rev-parse --abbrev-ref HEAD)" = hotfix ] || { echo "WRONG_BRANCH"; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "DIRTY: $(git status --porcelain | head -2)"; exit 1; }
proveo-entrypoint verify "$PWD" 2>/dev/null | grep -q . || { echo "NO_VERIFY_COMMANDS"; exit 1; }
touch CLAUDE.md.probe 2>/dev/null || { echo "WORKSPACE_READONLY"; exit 1; }
rm -f CLAUDE.md.probe
echo "WORKTREE_OK"`)

	out, err := exec.Command("docker", args...).CombinedOutput()
	if s := string(out); err != nil || !strings.Contains(s, "WORKTREE_OK") {
		t.Fatalf("worktree workspace unusable: %v\n%s", err, s)
	}
}

func worktreeEnvArgs(t *testing.T, target, input string) []string {
	t.Helper()
	bin := buildProveo(t)
	// --credentials forward is load-bearing, not incidental: it is the only posture
	// that carries the project .env INTO the container. Under the default broker the
	// credential policy masks that path with /dev/null instead, so the escaping-.env
	// mount these probes assert would not exist at all.
	cmd := exec.Command(bin, "run", target, "--credentials", "forward", "--input", input, "--print")
	// PROVEO_SBX=off is load-bearing: these probes SCRAPE -v specs out of the
	// printed plan and then run `docker run` themselves. On a host with sbx
	// installed the printed plan is the SANDBOX argv, which carries no -v at all —
	// so without this the scrape yields nothing and the probe runs against an
	// unmounted container while still looking like a pass.
	cmd.Env = append(os.Environ(), "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_SBX=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", target, err, out)
	}
	var args []string
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "GIT_DIR=") || strings.HasPrefix(f, "GIT_WORK_TREE=") {
			args = append(args, "-e", f)
		}
	}
	return args
}

// The claudecode entrypoint must operate on the directory that actually holds
// the workspace. It used to work in /workspace — the image's own directory, one
// level above the mount — so verify detection scanned an empty tree and the
// CLAUDE.md seed failed with a misleading "workspace may be read-only".
// Runs the REAL entrypoint (no override) so the cwd it picks is what is tested.
func TestClaudecodeEntrypointOperatesOnTheInputDir(t *testing.T) {
	const target = "claudecode"
	img := harnessImage(t, target)
	wt := newTempWorktree(t)

	args := []string{"run", "--rm", "--name", "proveo-cwd-probe-" + t.Name()}
	args = append(args, workspaceMountArgs(t, target, wt)...)
	args = append(args, worktreeEnvArgs(t, target, wt)...)
	args = append(args,
		"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
		// The image bakes its own entrypoint; run the working-tree one so this tests
		// current source rather than whenever the image was last built. Invoked via
		// bash because the tracked file carries no exec bit.
		"-v", filepath.Join(repoRootDir(t), "defs/claudecode/mcp/entrypoint.sh")+":/proveo-ep.sh:ro",
		"--user", hostUIDGID(t),
		"-e", "PROVEO_SMOKE_TEST=1", "-e", "PROVEO_SMOKE_TARGET=claudecode",
		"--entrypoint", "bash", img, "/proveo-ep.sh")

	cmd := exec.Command("docker", args...)
	done := make(chan struct{})
	var out []byte
	go func() { out, _ = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		_ = exec.Command("docker", "rm", "-f", "proveo-cwd-probe-"+t.Name()).Run()
		<-done
	}
	s := string(out)

	if !strings.Contains(s, "PROVEO_SMOKE_READY") {
		t.Fatalf("entrypoint did not reach smoke readiness:\n%s", s)
	}
	if strings.Contains(s, "Could not seed CLAUDE.md") {
		t.Errorf("entrypoint seeded into a directory it cannot write — it is not working "+
			"in the mounted input dir:\n%s", s)
	}
	if !verifyCommandsListed(s) {
		t.Errorf("Verification Commands section is empty — the entrypoint scanned a directory "+
			"that holds no project markers:\n%s", s)
	}
}

// verifyCommandsListed reports whether at least one command follows the
// "Verification Commands" banner before the section closes.
func verifyCommandsListed(out string) bool {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "Verification Commands") {
			continue
		}
		for _, next := range lines[i+1:] {
			t := strings.TrimSpace(next)
			if t == "" || strings.HasPrefix(t, "─") {
				break
			}
			return true
		}
	}
	return false
}

// Python environments are detected and provisioned, never inherited: a host
// venv holds an interpreter and compiled extensions built for the host OS/arch.
// Asserts the provisioned env is usable, keyed outside the workspace, and that
// pyright resolves real packages through it.
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

	out, err := exec.Command("docker", "run", "--rm", "--user", "root",
		"-v", entrypointLibPath(t)+":/lib.sh:ro", "-v", dir+":/w", "-w", "/w",
		"--entrypoint", "bash", img, "-c", `
export HOME=/tmp/h; mkdir -p $HOME
source /lib.sh 2>/dev/null
ensure_python_env /w
[ -n "$VIRTUAL_ENV" ] || { echo "NO_VIRTUAL_ENV"; exit 1; }
case "$VIRTUAL_ENV" in /w/*) echo "ENV_INSIDE_WORKSPACE"; exit 1 ;; esac
python -c 'import requests' 2>/dev/null || { echo "DEP_UNIMPORTABLE"; exit 1; }
pyright --outputjson app.py 2>/dev/null | grep -q reportMissingModuleSource && { echo "PYRIGHT_UNRESOLVED"; exit 1; }
echo "PYENV_OK"`).CombinedOutput()

	if s := string(out); err != nil || !strings.Contains(s, "PYENV_OK") {
		t.Fatalf("python environment not provisioned correctly: %v\n%s", err, s)
	}
}

// A workspace with no Python markers must not provision anything.
func TestPythonEnvironmentSkipsNonPythonWorkspaces(t *testing.T) {
	img := harnessImage(t, "opencode")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "run", "--rm", "--user", "root",
		"-v", entrypointLibPath(t)+":/lib.sh:ro", "-v", dir+":/w", "-w", "/w",
		"--entrypoint", "bash", img, "-c", `
export HOME=/tmp/h; mkdir -p $HOME
source /lib.sh 2>/dev/null
ensure_python_env /w
[ -z "$VIRTUAL_ENV" ] || { echo "PROVISIONED_ANYWAY"; exit 1; }
[ -d "$HOME/.cache/proveo/venv" ] && { echo "VENV_DIR_CREATED"; exit 1; }
echo "SKIP_OK"`).CombinedOutput()
	if s := string(out); err != nil || !strings.Contains(s, "SKIP_OK") {
		t.Fatalf("non-Python workspace triggered provisioning: %v\n%s", err, s)
	}
}

// A monorepo root is whatever its PRIMARY language made it, so the Python
// project is nested under apps/. Provisioning that stat-ed only the root found
// nothing at all — while ensure_language_servers, which walks the tree, still
// installed pyright. That asymmetry left the agent with code intelligence for a
// project it had no interpreter to run.
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

	out, err := exec.Command("docker", "run", "--rm", "--user", "root",
		"-v", entrypointLibPath(t)+":/lib.sh:ro", "-v", dir+":/w", "-w", "/w",
		"--entrypoint", "bash", img, "-c", `
export HOME=/tmp/h; mkdir -p $HOME
source /lib.sh 2>/dev/null
roots="$(_py_project_roots /w)"
[ "$roots" = "/w/apps/svc" ] || { echo "BAD_ROOTS[$roots]"; exit 1; }
ensure_python_env /w
[ -n "$VIRTUAL_ENV" ] || { echo "NO_VIRTUAL_ENV"; exit 1; }
case "$VIRTUAL_ENV" in /w/*) echo "ENV_INSIDE_WORKSPACE"; exit 1 ;; esac
python -c 'import requests' 2>/dev/null || { echo "DEP_UNIMPORTABLE"; exit 1; }
echo "NESTED_OK"`).CombinedOutput()

	if s := string(out); err != nil || !strings.Contains(s, "NESTED_OK") {
		t.Fatalf("nested Python project not provisioned: %v\n%s", err, s)
	}
}

// Every language's dependency tree can arrive host-built — on docker as the
// private copy proveo stages, on sbx as the mirrored checkout itself — so this
// is never a TypeScript-only hazard; node_modules is just the loudest instance.
// The portable majority of each tree loads, so the failure arrives late and
// names the TOOL rather than the platform. One probe, one table, every language.
// The bind here is a plain host directory, which is the sbx shape: the probe
// reports and offers --clone rather than clearing a tree that is not its own.
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
		out, err := exec.Command("docker", "run", "--rm", "--user", "root",
			"-v", entrypointLibPath(t)+":/lib.sh:ro", "-v", dir+":/w", "-w", "/w",
			"--entrypoint", "bash", img, "-c",
			"export HOME=/tmp/h; mkdir -p $HOME\nsource /lib.sh 2>/dev/null\n"+script).CombinedOutput()
		if err != nil {
			t.Fatalf("probe failed: %v\n%s", err, out)
		}
		return string(out)
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
