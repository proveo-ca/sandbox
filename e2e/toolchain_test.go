//go:build e2e

// SPEC: _spec/tests/43-toolchain-e2e.puml, _spec/_runtimes/toolchain-provisioning.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/envflag"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "testdata", "polyglot22")
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..")
}

func entrypointLibPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRootDir(t), "packages", "lib", "entrypoint-lib.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("entrypoint-lib.sh not found at %s: %v", p, err)
	}
	return p
}

var toolchainHarnesses = []string{"opencode", "claudecode", "codex", "cursor"}

type runOpts struct {
	env     map[string]string
	target  string  // harness; empty = opencode
	work    string  // workspace; empty = a fresh copy of the fixture
	run     *kitRun // an already-launched run to reuse
	timeout time.Duration
}

// fixtureWorkspace copies the polyglot fixture into a git repo of its own, which
// is what an operator hands proveo and what the sandbox clones.
func fixtureWorkspace(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	entries, err := os.ReadDir(fixtureDir(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(fixtureDir(t), e.Name()))
		if err != nil {
			t.Fatalf("read fixture file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(work, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "config", "user.email", "e2e@proveo.test")
	mustRun(t, work, "git", "config", "user.name", "proveo e2e")
	mustRun(t, work, "git", "add", "-A")
	mustRun(t, work, "git", "commit", "-qm", "polyglot fixture")
	return work
}

func toolchainSandbox(t *testing.T, target, work string) *kitRun {
	t.Helper()
	return toolchainSandboxHome(t, target, work, "")
}

// toolchainSandboxHome runs with a proveo home of the caller's choosing.
func toolchainSandboxHome(t *testing.T, target, work, home string) *kitRun {
	t.Helper()
	requireHarness(t, target)
	var env []string
	if home != "" {
		env = append(env, "PROVEO_HOME="+home)
	}
	return launchKit(t, buildProveo(t), target, work, env)
}

// seedInstalls counts what the seed installed.
func seedInstalls(t *testing.T, k *kitRun) int {
	t.Helper()
	out, _ := k.exec(
		`printf 'INSTALLS %s\n' "$(grep -cE 'Dynamically installing|Installing ' /var/log/sbx-kit-startup.log 2>/dev/null || echo 0)"`,
		60*time.Second)
	n := -1
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || fields[0] != "INSTALLS" {
			continue
		}
		if v, err := strconv.Atoi(fields[1]); err == nil {
			n = v
		}
	}
	if n < 0 {
		t.Fatalf("could not read the seed's install count from the startup log:\n%s", out)
	}
	return n
}

func seedCounts(pane string) (started, done int) {
	for _, line := range strings.Split(pane, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		// the echoed command spells SEED too, but never followed by two numbers
		if len(fields) != 3 || fields[0] != "SEED" {
			continue
		}
		a, errA := strconv.Atoi(fields[1])
		b, errB := strconv.Atoi(fields[2])
		if errA != nil || errB != nil {
			continue
		}
		started, done = a, b
	}
	return started, done
}

func envPrefix(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%q\n", k, env[k])
	}
	return b.String()
}

// runLib drives the entrypoint lib inside a sandbox `proveo run` created.
func runLib(t *testing.T, o runOpts, script string) (string, error) {
	t.Helper()
	k := o.run
	if k != nil {
		k = k.with(t)
	} else {
		target := o.target
		if target == "" {
			target = "opencode"
		}
		work := o.work
		if work == "" {
			work = fixtureWorkspace(t)
		}
		k = toolchainSandbox(t, target, work)
	}
	to := o.timeout
	if to == 0 {
		to = 5 * time.Minute
	}
	full := envPrefix(o.env) + "source /entrypoint-lib.sh 2>/dev/null\n" +
		"_fence=LIBOUT; printf '%s-BEGIN\\n' \"$_fence\"\n" +
		strings.ReplaceAll(script, "/work", `"$PWD"`) +
		"\nprintf '%s-END\\n' \"$_fence\"\n"
	out, status := k.exec(full, to)
	if status != 0 {
		return fencedOutput(out), fmt.Errorf("script exited %d", status)
	}
	return fencedOutput(out), nil
}

func fencedOutput(pane string) string {
	beg := strings.LastIndex(pane, "LIBOUT-BEGIN")
	if beg < 0 {
		return pane
	}
	rest := pane[beg+len("LIBOUT-BEGIN"):]
	if end := strings.LastIndex(rest, "LIBOUT-END"); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}

// requireSandboxGitHubToken keeps mise installs off the anonymous GitHub rate limit.
func requireSandboxGitHubToken(t *testing.T, k *kitRun) {
	t.Helper()
	out, status := k.exec(`gh auth token >/dev/null 2>&1 && echo GH-TOKEN-OK || echo GH-TOKEN-NO`,
		60*time.Second)
	if status != 0 || !strings.Contains(out, "GH-TOKEN-OK") {
		t.Skipf("no GitHub token inside the sandbox (`sbx secret set github`) — the ubi recipes " +
			"would hit the 60/hr anonymous limit and make this flaky rather than failing honestly")
	}
}

func TestToolchainProvisioningIsIdempotent(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_TOOLCHAIN_TEST")) {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to run the warm-home idempotence check")
	}
	work := fixtureWorkspace(t)
	home := t.TempDir() // a private toolchain store: this run's saves, nobody else's

	first := toolchainSandboxHome(t, "claudecode", work, home)
	requireSandboxGitHubToken(t, first)
	cold := seedInstalls(t, first)
	if cold == 0 {
		t.Fatalf("the first seed installed nothing against an empty store (%s) — "+
			"the fixture or the store is wrong, or /var/log/sbx-kit-startup.log is missing, so the warm half would prove nothing", home)
	}
	teardown, exited := first.end()
	if !exited {
		t.Fatalf("the first run never exited, so its late teardown could remove the same-named sandbox the warm run creates\n%s", tail(teardown, 40))
	}
	if names, ok := sbxSandboxNames(); ok && names[first.name] {
		t.Fatalf("the first run kept %s (work stranded in it, or its clone mode unknown), so the warm run would reattach instead of seeding fresh\n%s", first.name, tail(teardown, 40))
	}

	// Name the store's state in the failure message.
	store := toolStoreEntries(t, home)
	if len(store) == 0 {
		t.Fatalf("after teardown the store under %s is empty — the save half never wrote, "+
			"so no later run can restore anything\n--- teardown ---\n%s", home, teardown)
	}
	t.Logf("cold seed installed %d tool(s); store now carries %v", cold, store)

	// A second `proveo run` over the SAME workspace and the same store is where
	// the operator meets this claim: what one run installed, the next restores.
	second := toolchainSandboxHome(t, "claudecode", work, home)
	if warm := seedInstalls(t, second); warm > 0 {
		t.Errorf("the second run's seed installed %d tool(s) again (cold installed %d) — "+
			"the restore half is not reading the store, so every run pays the download again",
			warm, cold)
	}
	second.end()
}

func TestToolchainRespectsItsOptOuts(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_TOOLCHAIN_TEST")) {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to drive the opt-outs through a real sandbox")
	}
	k := toolchainSandbox(t, "opencode", fixtureWorkspace(t))
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"auto_install_tools_false", map[string]string{"PROVEO_AUTO_INSTALL_TOOLS": "false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runLib(t, runOpts{run: k, env: tc.env, timeout: 2 * time.Minute},
				`proveo_provision_toolchain /work`)
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			if strings.Contains(out, "Installing") || strings.Contains(out, "Dynamically installing") {
				t.Errorf("%v did not suppress provisioning:\n%s", tc.env, out)
			}
		})
	}
}

func TestToolchainFailsSoftWithoutEgress(t *testing.T) {
	t.Skip("blackholing egress under sbx means `sbx policy init deny-all`, which is host-wide " +
		"state shared with every other sandbox in this suite; run it on a host pinned to the " +
		"deny-all baseline, where the run banner stops warning that the Kit allowlist only adds reach")
}

func TestToolchainFloorInvariantsHoldForEveryHarness(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_TOOLCHAIN_TEST")) {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to probe the floor inside a sandbox per harness")
	}
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			out, err := runLib(t, runOpts{target: name, timeout: 2 * time.Minute}, `
for b in plantuml mise jq git; do
  command -v "$b" >/dev/null 2>&1 && echo "HAVE $b" || echo "MISS $b"
done
echo "PLANTUML_COPIES=$(type -a plantuml 2>/dev/null | awk '{print $NF}' \
  | xargs -r -n1 readlink -f 2>/dev/null | sort -u | wc -l | tr -d ' ')"`)
			if err != nil {
				t.Fatalf("probe %s: %v\n%s", name, err, out)
			}
			for _, want := range []string{"plantuml", "mise", "jq", "git"} {
				if !strings.Contains(out, "HAVE "+want) {
					t.Errorf("%s lacks floor tool %q — proveo/base is meant to provide it\n%s", name, want, out)
				}
			}
			if !strings.Contains(out, "PLANTUML_COPIES=1") {
				t.Errorf("%s does not ship exactly one plantuml\n%s", name, out)
			}
		})
	}
}

func TestToolchainLibIsCurrentInEveryHarness(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_TOOLCHAIN_TEST")) {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to read the baked lib inside a sandbox per harness")
	}
	required := []string{
		"_proveo_lock_installs",         // §7 concurrency guard
		"_go_current_version",           // honours a go.mod toolchain pin
		"_proveo_github_token",          // GitHub API auth for mise installs
		"_proveo_walk",                  // the prune list §7c and §7d share
		"_proveo_project_roots",         // NESTED project discovery, every language
		"ensure_dependency_trees",       // §7d host-built dependency probe
		"ensure_jvm_toolchain",          // Gradle/Android build tools from the build files
		"_dep_lang_class",               // the per-language remedy table
		"proveo_provision_toolchain",    // §7 install-shaped work, reached by BOTH backends
		"_proveo_agent_home",            // the home the AGENT runs with, not this process's
		"_proveo_persist_tool_env",      // the resolved PATH, written where bash will read it
		"proveo_compose_house_rules",    // §7e proveo's conventions as user-level instructions
		"_proveo_write_block",           // marked-region rewrite that spares operator content
		"proveo_apply_ui_defaults",      // §7g sandbox theme + syntax highlighting
		"proveo_install_claude_hooks",   // §7h the cwd guard that names a vanished working directory
		"proveo_install_git_sync_hooks", // turn-end commit+push before idle/Stop
	}
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			script := "for fn in " + strings.Join(required, " ") + `; do
  grep -q "$fn" /entrypoint-lib.sh && echo "present $fn" || echo "absent $fn"
done`
			out, err := runLib(t, runOpts{target: name, timeout: 2 * time.Minute}, script)
			if err != nil {
				t.Fatalf("probe %s: %v\n%s", name, err, out)
			}
			for _, fn := range required {
				if !strings.Contains(out, "present "+fn) {
					t.Errorf("%s ships a STALE entrypoint-lib.sh: %q is missing. "+
						"Rebuild it (`mise run build %s`) — the source fix is not live until you do.",
						name, fn, name)
				}
			}
		})
	}
}

func TestToolchainShipsNoDuplicateBinaries(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_TOOLCHAIN_TEST")) {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to scan the image from inside a sandbox")
	}
	out, err := runLib(t, runOpts{timeout: 2 * time.Minute}, `
for b in plantuml mise node npm git gh jq tmux rg; do
  command -v "$b" >/dev/null 2>&1 || continue
  n=$(type -a "$b" 2>/dev/null | awk '{print $NF}' | xargs -r -n1 readlink -f 2>/dev/null | sort -u | wc -l)
  [ "$n" -gt 1 ] && echo "DUPLICATE $b ($n distinct files)"
done
echo "SCAN=done"`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SCAN=done") {
		t.Fatalf("duplicate scan did not complete:\n%s", out)
	}
	if strings.Contains(out, "DUPLICATE") {
		t.Errorf("a tool is installed in more than one layer:\n%s", out)
	}
}

// toolStoreDir mirrors _proveo_tool_store: PROVEO_STATE_HOME on the host, which
// is where a sandbox's toolchain is saved to and restored from.
func toolStoreDir(home string) string {
	if home == "" {
		home = os.Getenv("PROVEO_STATE_HOME")
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".proveo")
	}
	return filepath.Join(home, "toolchains")
}

func toolStoreEntries(t *testing.T, home string) []string {
	t.Helper()
	dir := toolStoreDir(home)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
