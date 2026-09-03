// SPEC: _spec/packages/lib/dependency-trees.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These drive ensure_dependency_trees with FAKE package managers on PATH — each
// one appends "<tool> <args> @ <cwd>" to a log and exits 0 — so what the seed
// decides to run, where, and how often is observable without a registry, an
// image, or a real install.

var (
	machO = []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x01}
	elf   = []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}
)

type depsHarness struct {
	t    *testing.T
	bash string
	ws   string // the workspace the seed scans
	bin  string // fake tools
	log  string // one line per fake invocation
}

func newDepsHarness(t *testing.T) *depsHarness {
	t.Helper()
	h := &depsHarness{t: t, bash: bashOrSkip(t), ws: t.TempDir(), bin: t.TempDir()}
	h.log = filepath.Join(t.TempDir(), "calls.log")
	for _, tool := range []string{"pnpm", "npm", "yarn", "bun", "go", "cargo", "bundle", "terraform"} {
		script := "#!/usr/bin/env bash\nprintf '%s %s @ %s\\n' \"$(basename \"$0\")\" \"$*\" \"$PWD\" >> \"$PROVEO_TEST_CALLS\"\n"
		if err := os.WriteFile(filepath.Join(h.bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *depsHarness) write(rel string, body []byte) {
	h.t.Helper()
	p := filepath.Join(h.ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// run sources the lib, applies `prelude` (a place to redefine a helper, the way
// a test double does), and runs ensure_dependency_trees over the workspace.
func (h *depsHarness) run(prelude string, env ...string) string {
	h.t.Helper()
	script := `source "$1/packages/lib/entrypoint-lib.sh"
export PATH="$2:/usr/bin:/bin"
export PROVEO_TEST_CALLS="$3"
` + prelude + `
ensure_dependency_trees "$4"
echo DONE`
	cmd := exec.Command(h.bash, "-c", script, "bash", repoRoot(h.t), h.bin, h.log, h.ws)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		h.t.Fatalf("ensure_dependency_trees did not run to completion: %v\n%s", err, out)
	}
	return string(out)
}

func (h *depsHarness) calls() []string {
	h.t.Helper()
	b, err := os.ReadFile(h.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

const isolated = `_proveo_dep_is_isolated() { return 0; }`
const hostTree = `_proveo_dep_is_isolated() { return 1; }`

func TestSeedInstallsAWorkspaceWithNothingInstalledOnceAtItsRoot(t *testing.T) {
	t.Parallel()
	h := newDepsHarness(t)
	h.write("package.json", []byte(`{"name":"m","private":true}`))
	h.write("pnpm-workspace.yaml", []byte("packages:\n  - apps/*\n  - packages/*\n"))
	h.write("pnpm-lock.yaml", []byte("lockfileVersion: '9.0'\n"))
	h.write("apps/tui/package.json", []byte(`{"name":"tui"}`))
	h.write("packages/lib/package.json", []byte(`{"name":"lib"}`))

	out := h.run(isolated)
	if !strings.Contains(out, "no node_modules") {
		t.Errorf("an uninstalled workspace must be called out:\n%s", out)
	}
	want := []string{"pnpm install --frozen-lockfile @ " + h.ws}
	if got := h.calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("installs = %q, want exactly one at the workspace root:\n%s\n"+
			"a pnpm workspace hoists every member into ONE node_modules, so the members are not "+
			"separate 'nothing installed' findings and `npm ci`/`pnpm install` inside one is wrong", got, out)
	}
	if strings.Count(out, "has no node_modules") != 1 {
		t.Errorf("workspace members reported as uninstalled on their own:\n%s", out)
	}
}

func TestSeedRebuildsAForeignTreeOnlyWhenItIsProveosCopy(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) *depsHarness {
		h := newDepsHarness(t)
		h.write("package.json", []byte(`{"name":"m"}`))
		h.write("pnpm-lock.yaml", []byte("lockfileVersion: '9.0'\n"))
		h.write("node_modules/.modules.yaml", []byte("x"))
		h.write("node_modules/sharp/build/sharp.node", machO)
		return h
	}
	survives := func(t *testing.T, h *depsHarness) bool {
		_, err := os.Stat(filepath.Join(h.ws, "node_modules", "sharp", "build", "sharp.node"))
		return err == nil
	}

	t.Run("private copy is cleared and reinstalled", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		out := h.run(isolated)
		if survives(t, h) {
			t.Errorf("the foreign binary survived: a package manager handed an existing tree keeps what it finds,\n"+
				"so the copy has to start empty for the rebuild to mean anything:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(h.ws, "node_modules")); err != nil {
			t.Errorf("node_modules itself must remain (it is a mount point in the container): %v", err)
		}
		if got := h.calls(); len(got) != 1 || !strings.HasPrefix(got[0], "pnpm install --frozen-lockfile @ ") {
			t.Errorf("rebuild = %q, want one frozen pnpm install", got)
		}
		if !strings.Contains(out, "host checkout is untouched") {
			t.Errorf("the operator is not told the host tree is safe:\n%s", out)
		}
	})

	t.Run("host tree is left alone and --clone is named", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		out := h.run(hostTree)
		if !survives(t, h) {
			t.Errorf("a tree that is the operator's own was cleared:\n%s", out)
		}
		if got := h.calls(); len(got) != 0 {
			t.Errorf("an in-place rewrite of the host tree ran without PROVEO_DEPS=reinstall: %q", got)
		}
		if !strings.Contains(out, "--clone") || !strings.Contains(out, "PROVEO_DEPS=reinstall") {
			t.Errorf("the two remedies (--clone, PROVEO_DEPS=reinstall) are not offered:\n%s", out)
		}
	})

	t.Run("PROVEO_DEPS=reinstall rewrites the host tree in place", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.run(hostTree, "PROVEO_DEPS=reinstall")
		if !survives(t, h) {
			t.Error("in-place means in place: nothing is cleared before the package manager runs")
		}
		if got := h.calls(); len(got) != 1 {
			t.Errorf("reinstall = %q, want one install", got)
		}
	})
}

func TestSeedRefreshesANativeTreeOnlyWithIdempotentManagers(t *testing.T) {
	t.Parallel()
	t.Run("pnpm refreshes against the lockfile", func(t *testing.T) {
		t.Parallel()
		h := newDepsHarness(t)
		h.write("package.json", []byte(`{"name":"m"}`))
		h.write("pnpm-lock.yaml", []byte("lockfileVersion: '9.0'\n"))
		h.write("node_modules/native-ok/ok.node", elf)
		out := h.run(isolated)
		if got := h.calls(); len(got) != 1 || !strings.HasPrefix(got[0], "pnpm install --frozen-lockfile @ ") {
			t.Errorf("refresh = %q, want one frozen pnpm install:\n%s", got, out)
		}
		if strings.Contains(out, "built on the host") {
			t.Errorf("an all-ELF tree was reported as foreign:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(h.ws, "node_modules", "native-ok", "ok.node")); err != nil {
			t.Error("a native tree must not be cleared before a refresh")
		}
	})
	t.Run("npm ci is not run over a present native tree", func(t *testing.T) {
		t.Parallel()
		h := newDepsHarness(t)
		h.write("package.json", []byte(`{"name":"m"}`))
		h.write("package-lock.json", []byte("{}"))
		h.write("node_modules/native-ok/ok.node", elf)
		h.run(isolated)
		if got := h.calls(); len(got) != 0 {
			t.Errorf("npm ci deletes node_modules first, every time — it is only for an absent or foreign tree, got %q", got)
		}
	})
}

func TestSeedFetchesPortableAndArtifactLanguagesBeforeTheAgent(t *testing.T) {
	t.Parallel()
	h := newDepsHarness(t)
	h.write("apps/api/go.mod", []byte("module example.com/api\n\ngo 1.26\n"))
	h.write("apps/harness/Cargo.toml", []byte("[package]\nname=\"h\"\n"))
	h.write("apps/harness/target/debug/deps/x.o", machO) // stale host build output in the copy
	h.write("infra/.terraform.lock.hcl", []byte("# lock\n"))

	out := h.run(isolated)
	got := strings.Join(h.calls(), "\n")
	for _, want := range []string{
		"go mod download @ " + filepath.Join(h.ws, "apps", "api"),
		"cargo fetch @ " + filepath.Join(h.ws, "apps", "harness"),
		"terraform init -input=false @ " + filepath.Join(h.ws, "infra"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in installs:\n%s\n%s", want, got, out)
		}
	}
	if strings.Contains(got, "-upgrade") {
		t.Errorf("terraform init -upgrade rewrites .terraform.lock.hcl; the seed respects lockfiles: %s", got)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "apps", "harness", "target", "debug")); err == nil {
		t.Errorf("foreign build output in proveo's copy was kept; the toolchain has to rebuild it anyway:\n%s", out)
	}
}

func TestSeedInstallsNothingWhenDepsAreOff(t *testing.T) {
	t.Parallel()
	h := newDepsHarness(t)
	h.write("package.json", []byte(`{"name":"m"}`))
	h.write("pnpm-lock.yaml", []byte("lockfileVersion: '9.0'\n"))
	h.write("apps/api/go.mod", []byte("module x\n"))
	h.run(isolated, "PROVEO_DEPS=off")
	if got := h.calls(); len(got) != 0 {
		t.Errorf("PROVEO_DEPS=off still ran %q", got)
	}
}

func TestSeedStopsAtTheScanDepthThePlanUses(t *testing.T) {
	t.Parallel()
	h := newDepsHarness(t)
	h.write("a/b/c/d/e/package.json", []byte(`{"name":"deep"}`)) // depth 5 > 4
	h.write("a/b/c/d/e/pnpm-lock.yaml", []byte("lockfileVersion: '9.0'\n"))
	h.write("a/b/c/package.json", []byte(`{"name":"shallow"}`)) // depth 3
	h.write("a/b/c/pnpm-lock.yaml", []byte("lockfileVersion: '9.0'\n"))
	h.run(isolated)
	got := strings.Join(h.calls(), "\n")
	if !strings.Contains(got, filepath.Join(h.ws, "a", "b", "c")+"\n") && !strings.HasSuffix(got, filepath.Join(h.ws, "a", "b", "c")) {
		t.Errorf("project within the scan depth not installed: %q", got)
	}
	if strings.Contains(got, filepath.Join("d", "e")) {
		t.Errorf("project beyond the scan depth was installed — the mount plan does not isolate it, so this "+
			"install would land in the operator's checkout: %q", got)
	}
}
