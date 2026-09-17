//go:build e2e

// SPEC: _spec/tests/43-toolchain-e2e.puml, _spec/_runtimes/toolchain-provisioning.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

type languageOutcome int

const (
	served languageOutcome = iota
	servedOnAmd64
	servedByProjectTools
	unserved
)

var expectedLanguages = map[string]languageOutcome{
	"typescript": served, "python": served, "bash": served, "docker": served,
	"yaml": served, "json": served, "html": served, "css": served,
	"plantuml": served,
	"rust":     served, "markdown": served, "toml": served, "terraform": served,
	"lua": served, "zig": served, "kotlin": served, "java": served,
	"cpp":     servedOnAmd64,
	"go":      servedByProjectTools,
	"mermaid": unserved, "nix": unserved, "ruby": unserved,
}

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

var toolchainHarnesses = []string{"opencode", "claudecode", "codex", "cursor", "cecli"}

type runOpts struct {
	env     map[string]string
	target  string        // harness; empty = opencode
	work    string        // workspace; empty = a fresh copy of the fixture
	sess    *tmux.Session // an already-open sandbox shell to reuse
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

func toolchainSandbox(t *testing.T, target, work string) *tmux.Session {
	t.Helper()
	return toolchainSandboxHome(t, target, work, "")
}

// toolchainSandboxHome runs with a proveo home of the caller's choosing.
func toolchainSandboxHome(t *testing.T, target, work, home string) *tmux.Session {
	t.Helper()
	requireHarness(t, target)
	var extra []string
	if home != "" {
		extra = append(extra, "PROVEO_HOME="+home)
	}
	sess := launchShellEnv(t, buildProveo(t), target, work, extra)
	waitForSeed(t, sess)
	return sess
}

// seedInstalls counts what the seed installed.
func seedInstalls(t *testing.T, sess *tmux.Session) int {
	t.Helper()
	out, _ := shellExec(t, sess,
		`printf 'INSTALLS %s\n' "$(grep -c 'Installing' /var/log/sbx-kit-startup.log 2>/dev/null || echo 0)"`,
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

// waitForSeed holds until the Kit's startup commands are done.
func waitForSeed(t *testing.T, sess *tmux.Session) {
	t.Helper()
	deadline := time.Now().Add(durationEnv(t, "PROVEO_TEST_SEED_TIMEOUT", 12*time.Minute))
	const probe = `printf 'SEED %s %s\n' ` +
		`"$(grep -c '^> ' /var/log/sbx-kit-startup.log 2>/dev/null || echo 0)" ` +
		`"$(grep -cE '^(ok|fail|error)' /var/log/sbx-kit-startup.log 2>/dev/null || echo 0)"`
	for {
		out, _ := shellExec(t, sess, probe, 60*time.Second)
		started, done := seedCounts(out)
		if started > 0 && done >= started {
			t.Logf("seed finished: %d startup command(s)", done)
			return
		}
		if time.Now().After(deadline) {
			t.Logf("seed still running after the wait (%d started, %d done) — measuring anyway", started, done)
			return
		}
		time.Sleep(10 * time.Second)
	}
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
	sess := o.sess
	if sess == nil {
		target := o.target
		if target == "" {
			target = "opencode"
		}
		work := o.work
		if work == "" {
			work = fixtureWorkspace(t)
		}
		sess = toolchainSandbox(t, target, work)
	}
	to := o.timeout
	if to == 0 {
		to = 5 * time.Minute
	}
	full := envPrefix(o.env) + "source /entrypoint-lib.sh 2>/dev/null\n" +
		"_fence=LIBOUT; printf '%s-BEGIN\\n' \"$_fence\"\n" +
		strings.ReplaceAll(script, "/work", `"$PWD"`) +
		"\nprintf '%s-END\\n' \"$_fence\"\n"
	out, status := shellExec(t, sess, full, to)
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

// requireSandboxGitHubToken keeps the ubi recipes off the anonymous rate limit.
func requireSandboxGitHubToken(t *testing.T, sess *tmux.Session) {
	t.Helper()
	out, status := shellExec(t, sess, `gh auth token >/dev/null 2>&1 && echo GH-TOKEN-OK || echo GH-TOKEN-NO`,
		60*time.Second)
	if status != 0 || !strings.Contains(out, "GH-TOKEN-OK") {
		t.Skipf("no GitHub token inside the sandbox (`sbx secret set github`) — the ubi recipes " +
			"would hit the 60/hr anonymous limit and make this flaky rather than failing honestly")
	}
}

// endShell closes a sandbox session the way an operator does, so the run's own
// teardown is what removes the sandbox.
func endShell(t *testing.T, sess *tmux.Session) string {
	t.Helper()
	_ = sess.SendText("exit")
	_ = sess.Enter()
	out, exited := waitSessionExit(sess, 3*time.Minute)
	if !exited {
		t.Logf("the sandbox session did not exit after `exit`")
	}
	return out
}

func detectedLanguages(t *testing.T, o runOpts) map[string]bool {
	t.Helper()
	out, err := runLib(t, o, `detect_workspace_lsps /work | cut -d'|' -f1`)
	if err != nil {
		t.Fatalf("detect_workspace_lsps: %v\n%s", err, out)
	}
	got := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got[l] = true
		}
	}
	return got
}

func TestToolchainFixtureCoversEveryLanguage(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(fixtureDir(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(entries) != len(expectedLanguages) {
		t.Errorf("fixture has %d files but %d languages are expected — one trigger per language",
			len(entries), len(expectedLanguages))
	}
	script := "source " + entrypointLibPath(t) + " 2>/dev/null; _lsp_walk " + fixtureDir(t) + " | cut -f1 | sort -u"
	out, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatalf("_lsp_walk on host: %v", err)
	}
	found := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			found[l] = true
		}
	}
	for lang := range expectedLanguages {
		if !found[lang] {
			t.Errorf("no fixture file triggers %q", lang)
		}
	}
	for lang := range found {
		if _, ok := expectedLanguages[lang]; !ok {
			t.Errorf("fixture triggers %q, which expectedLanguages does not describe", lang)
		}
	}
}

func TestToolchainLanguageMatrix(t *testing.T) {
	if os.Getenv("PROVEO_TOOLCHAIN_TEST") != "1" {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to run the full language matrix (~600MB of downloads)")
	}
	sess := toolchainSandbox(t, "opencode", fixtureWorkspace(t))
	requireSandboxGitHubToken(t, sess)
	o := runOpts{sess: sess, timeout: 25 * time.Minute}

	out, err := runLib(t, o, `
ensure_project_tools >/dev/null 2>&1
ensure_language_servers /work
echo "===DETECTED==="
detect_workspace_lsps /work | cut -d'|' -f1
echo "===UNRESOLVED==="
detect_workspace_lsps /work | while IFS='|' read -r lang cnt cmd rest; do
  command -v "$cmd" >/dev/null 2>&1 || echo "$lang:$cmd"
done`)
	if err != nil {
		t.Fatalf("provisioning run failed: %v\n%s", err, out)
	}

	detected, unresolved := parseMatrix(t, out)
	amd64 := isAmd64(t, o)

	for lang, outcome := range expectedLanguages {
		want := outcome == served || outcome == servedByProjectTools ||
			(outcome == servedOnAmd64 && amd64)
		switch {
		case want && !detected[lang]:
			t.Errorf("%s: expected to be wired but was not\n--- run ---\n%s", lang, out)
		case !want && detected[lang]:
			t.Errorf("%s: expected NOT to be wired (outcome %d) but was", lang, outcome)
		}
	}
	if len(unresolved) > 0 {
		t.Errorf("wired servers that do not resolve on PATH: %v", unresolved)
	}
	if !amd64 && !strings.Contains(out, "Skipping clangd") {
		t.Errorf("on %s clangd must be skipped WITH a reason, not silently\n%s", runtime.GOARCH, out)
	}
}

func parseMatrix(t *testing.T, out string) (detected map[string]bool, unresolved []string) {
	t.Helper()
	detected = map[string]bool{}
	section := ""
	for _, raw := range strings.Split(out, "\n") {
		l := strings.TrimSpace(raw)
		switch l {
		case "===DETECTED===", "===UNRESOLVED===":
			section = l
			continue
		}
		if l == "" {
			continue
		}
		switch section {
		case "===DETECTED===":
			detected[l] = true
		case "===UNRESOLVED===":
			unresolved = append(unresolved, l)
		}
	}
	if len(detected) == 0 {
		t.Fatalf("no languages detected at all — the run did not work:\n%s", out)
	}
	return detected, unresolved
}

func isAmd64(t *testing.T, o runOpts) bool {
	t.Helper()
	out, err := runLib(t, o, `uname -m`)
	if err != nil {
		t.Fatalf("uname: %v\n%s", err, out)
	}
	m := strings.TrimSpace(lastLine(out))
	return m == "x86_64" || m == "amd64"
}

func lastLine(s string) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return parts[len(parts)-1]
}

func TestToolchainProvisioningIsIdempotent(t *testing.T) {
	if os.Getenv("PROVEO_TOOLCHAIN_TEST") != "1" {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to run the warm-home idempotence check")
	}
	work := fixtureWorkspace(t)
	home := t.TempDir() // a private toolchain store: this run's saves, nobody else's

	first := toolchainSandboxHome(t, "opencode", work, home)
	requireSandboxGitHubToken(t, first)
	cold := seedInstalls(t, first)
	if cold == 0 {
		t.Fatalf("the first seed installed nothing against an empty store (%s) — "+
			"the fixture or the store is wrong, so the warm half would prove nothing", home)
	}
	teardown := endShell(t, first)

	// Name the store's state in the failure message.
	store := toolStoreEntries(t, home)
	if len(store) == 0 {
		t.Fatalf("after teardown the store under %s is empty — the save half never wrote, "+
			"so no later run can restore anything\n--- teardown ---\n%s", home, teardown)
	}
	t.Logf("cold seed installed %d server(s); store now carries %v", cold, store)

	// A second `proveo run` over the SAME workspace and the same store is where
	// the operator meets this claim: what one run installed, the next restores.
	second := toolchainSandboxHome(t, "opencode", work, home)
	if warm := seedInstalls(t, second); warm > 0 {
		t.Errorf("the second run's seed installed %d server(s) again (cold installed %d) — "+
			"the restore half is not reading the store, so every run pays the download again",
			warm, cold)
	}
	endShell(t, second)
}

func TestToolchainRespectsItsOptOuts(t *testing.T) {
	if os.Getenv("PROVEO_TOOLCHAIN_TEST") != "1" {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to drive the opt-outs through a real sandbox")
	}
	sess := toolchainSandbox(t, "opencode", fixtureWorkspace(t))
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"auto_install_tools_false", map[string]string{"PROVEO_AUTO_INSTALL_TOOLS": "false"}},
		{"lsp_install_off", map[string]string{"PROVEO_LSP_INSTALL": "off"}},
		{"min_files_above_fixture", map[string]string{"PROVEO_LSP_MIN_FILES": "5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runLib(t, runOpts{sess: sess, env: tc.env, timeout: 2 * time.Minute},
				`ensure_language_servers /work`)
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			if strings.Contains(out, "Installing") {
				t.Errorf("%v did not suppress provisioning:\n%s", tc.env, out)
			}
		})
	}
}

func TestToolchainOptOutStillSeesInstalledServers(t *testing.T) {
	if os.Getenv("PROVEO_TOOLCHAIN_TEST") != "1" {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to drive the opt-out through a real sandbox")
	}
	out, err := runLib(t, runOpts{
		env:     map[string]string{"PROVEO_AUTO_INSTALL_TOOLS": "false"},
		timeout: 2 * time.Minute,
	}, `ensure_language_servers /work; detect_workspace_lsps /work | cut -d'|' -f1`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, lang := range []string{"typescript", "bash", "plantuml"} {
		if !strings.Contains(out, lang) {
			t.Errorf("with auto-install off, baked server %q was not detected — "+
				"PATH setup must not sit behind the opt-out\n%s", lang, out)
		}
	}
}

func TestToolchainFailsSoftWithoutEgress(t *testing.T) {
	t.Skip("blackholing egress under sbx means `sbx policy init deny-all`, which is host-wide " +
		"state shared with every other sandbox in this suite; run it on a host pinned to the " +
		"deny-all baseline, where the run banner stops warning that the Kit allowlist only adds reach")
}

func TestToolchainFloorInvariantsHoldForEveryHarness(t *testing.T) {
	if os.Getenv("PROVEO_TOOLCHAIN_TEST") != "1" {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to probe the floor inside a sandbox per harness")
	}
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			out, err := runLib(t, runOpts{target: name, timeout: 2 * time.Minute}, `
for b in plantuml plantuml-lsp mise jq git; do
  command -v "$b" >/dev/null 2>&1 && echo "HAVE $b" || echo "MISS $b"
done
echo "PLANTUML_COPIES=$(type -a plantuml 2>/dev/null | awk '{print $NF}' \
  | xargs -r -n1 readlink -f 2>/dev/null | sort -u | wc -l | tr -d ' ')"`)
			if err != nil {
				t.Fatalf("probe %s: %v\n%s", name, err, out)
			}
			for _, want := range []string{"plantuml", "plantuml-lsp", "mise", "jq", "git"} {
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
	if os.Getenv("PROVEO_TOOLCHAIN_TEST") != "1" {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to read the baked lib inside a sandbox per harness")
	}
	required := []string{
		"_proveo_lock_installs",            // §7/§8 concurrency guard
		"_go_current_version",              // honours a go.mod toolchain pin
		"ensure_language_servers",          // §8 provisioning
		"_lsp_mise_spec",                   // the recipe table
		"_proveo_github_token",             // GitHub API auth for ubi recipes
		"_proveo_walk",                     // the prune list §7c, §7d and §8 must share
		"_proveo_project_roots",            // NESTED project discovery, every language
		"ensure_dependency_trees",          // §7d host-built dependency probe
		"_dep_lang_class",                  // the per-language remedy table
		"proveo_provision_toolchain",       // §7 install-shaped work, reached by BOTH backends
		"_proveo_agent_home",               // the home the AGENT runs with, not this process's
		"_proveo_persist_tool_env",         // the resolved PATH, written where bash will read it
		"proveo_compose_house_rules",       // §7e proveo's conventions as user-level instructions
		"_proveo_write_block",              // marked-region rewrite that spares operator content
		"proveo_apply_ui_defaults",         // §7g sandbox theme + syntax highlighting
		"proveo_install_claude_hooks",      // §7h the cwd guard that names a vanished working directory
		"proveo_enable_claude_lsp_plugins", // §7i seeded code-intelligence plugins, enabled per binary
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
						"Rebuild it (`proveo build %s`) — the source fix is not live until you do.",
						name, fn, name)
				}
			}
		})
	}
}

func TestToolchainShipsNoDuplicateBinaries(t *testing.T) {
	if os.Getenv("PROVEO_TOOLCHAIN_TEST") != "1" {
		t.Skip("set PROVEO_TOOLCHAIN_TEST=1 to scan the image from inside a sandbox")
	}
	out, err := runLib(t, runOpts{timeout: 2 * time.Minute}, `
for b in plantuml plantuml-lsp mise node npm git gh jq tmux rg; do
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
