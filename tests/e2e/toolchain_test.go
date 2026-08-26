//go:build e2e

// SPEC: _spec/tests/43-toolchain-e2e.puml, _spec/_runtimes/toolchain-provisioning.puml

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
	return filepath.Join(wd, "..", "..")
}

func entrypointLibPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRootDir(t), "packages", "lib", "entrypoint-lib.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("entrypoint-lib.sh not found at %s: %v", p, err)
	}
	return p
}

var toolchainHarnesses = []string{"opencode", "claudecode", "cursor", "cecli"}

func toolchainImage(t *testing.T) string {
	t.Helper()
	return harnessImage(t, "opencode")
}

func hostGitHubToken() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	out, err := exec.Command("gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type runOpts struct {
	env      map[string]string
	network  string // "none" to blackhole egress
	homeVol  string // docker volume mounted at the image HOME's .local, for warm runs
	platform string // "linux/amd64" / "linux/arm64"; empty = native
	image    string // harness image; empty = the opencode default
	timeout  time.Duration
}

func runLib(t *testing.T, o runOpts, script string) (string, error) {
	t.Helper()
	image := o.image
	if image == "" {
		image = toolchainImage(t)
	}
	args := []string{"run", "--rm"}
	if o.platform != "" {
		args = append(args, "--platform", o.platform)
	}
	if o.network != "" {
		args = append(args, "--network", o.network)
	}
	for k, v := range o.env {
		args = append(args, "-e", k+"="+v)
	}
	if o.homeVol != "" {
		args = append(args, "-v", o.homeVol+":/home/opencode/.local")
	}
	args = append(args,
		"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
		"-v", fixtureDir(t)+":/work:ro",
		"-w", "/work", "--entrypoint", "bash", image,
		"-c", "source /entrypoint-lib.sh 2>/dev/null\n"+script)

	to := o.timeout
	if to == 0 {
		to = 5 * time.Minute
	}
	cmd := exec.Command("docker", args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(to):
		_ = cmd.Process.Kill()
		t.Fatalf("docker run exceeded %s\n%s", to, out)
	}
	return string(out), err
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
	token := hostGitHubToken()
	if token == "" {
		t.Skip("no GitHub token (gh auth login, or set GITHUB_TOKEN) — the ubi recipes " +
			"would hit the 60/hr anonymous limit and make this flaky rather than failing honestly")
	}
	o := runOpts{env: map[string]string{"GH_TOKEN": token}, timeout: 25 * time.Minute}

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
	token := hostGitHubToken()
	if token == "" {
		t.Skip("no GitHub token — see TestToolchainLanguageMatrix")
	}
	vol := "proveo-toolchain-e2e-" + strings.ReplaceAll(t.Name(), "/", "-")
	mustRunHost(t, "docker", "volume", "rm", "-f", vol)
	mustRunHost(t, "docker", "volume", "create", vol)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", vol).Run() })

	o := runOpts{
		env:     map[string]string{"GH_TOKEN": token, "PROVEO_LSP_INSTALL": "rust,toml"},
		homeVol: vol,
		timeout: 10 * time.Minute,
	}
	cold, err := runLib(t, o, `ensure_language_servers /work`)
	if err != nil {
		t.Fatalf("cold run: %v\n%s", err, cold)
	}
	if !strings.Contains(cold, "Installing") {
		t.Fatalf("cold run installed nothing — the fixture or volume is wrong:\n%s", cold)
	}
	warm, err := runLib(t, o, `ensure_language_servers /work`)
	if err != nil {
		t.Fatalf("warm run: %v\n%s", err, warm)
	}
	if strings.Contains(warm, "Installing") {
		t.Errorf("warm run reinstalled servers — durable PROVEO_HOME is not being reused\n"+
			"--- warm ---\n%s", warm)
	}
}

func mustRunHost(t *testing.T, name string, args ...string) {
	t.Helper()
	_ = exec.Command(name, args...).Run() // best-effort: `volume rm` of a missing volume is fine
}

func TestToolchainRespectsItsOptOuts(t *testing.T) {
	toolchainImage(t)
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"auto_install_tools_false", map[string]string{"PROVEO_AUTO_INSTALL_TOOLS": "false"}},
		{"lsp_install_off", map[string]string{"PROVEO_LSP_INSTALL": "off"}},
		{"min_files_above_fixture", map[string]string{"PROVEO_LSP_MIN_FILES": "5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runLib(t, runOpts{env: tc.env, timeout: 2 * time.Minute},
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
	toolchainImage(t)
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
	toolchainImage(t)
	start := time.Now()
	out, err := runLib(t, runOpts{
		network: "none",
		env:     map[string]string{"PROVEO_LSP_INSTALL_TIMEOUT": "20"},
		timeout: 3 * time.Minute,
	}, `ensure_language_servers /work; echo "EXIT=$?"`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "EXIT=0") {
		t.Errorf("blackholed egress must not fail the boot:\n%s", out)
	}
	if d := time.Since(start); d > 2*time.Minute {
		t.Errorf("blackholed egress took %s — installs are not bounded", d)
	}
}

func TestToolchainFloorInvariantsHoldForEveryHarness(t *testing.T) {
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			img := harnessImage(t, name)
			out, err := runLib(t, runOpts{image: img, timeout: 2 * time.Minute}, `
for b in plantuml plantuml-lsp mise jq git; do
  command -v "$b" >/dev/null 2>&1 && echo "HAVE $b" || echo "MISS $b"
done
echo "PLANTUML_COPIES=$(type -a plantuml 2>/dev/null | awk '{print $NF}' \
  | xargs -r -n1 readlink -f 2>/dev/null | sort -u | wc -l | tr -d ' ')"`)
			if err != nil {
				t.Fatalf("probe %s: %v\n%s", img, err, out)
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
	required := []string{
		"_proveo_lock_installs",      // §7/§8 concurrency guard
		"_go_current_version",        // honours a go.mod toolchain pin
		"ensure_language_servers",    // §8 provisioning
		"_lsp_mise_spec",             // the recipe table
		"_proveo_github_token",       // GitHub API auth for ubi recipes
		"_proveo_walk",               // the prune list §7c, §7d and §8 must share
		"_proveo_project_roots",      // NESTED project discovery, every language
		"ensure_dependency_trees",    // §7d host-built dependency probe
		"_dep_lang_class",            // the per-language remedy table
		"proveo_provision_toolchain", // §7 install-shaped work, reached by BOTH backends
		"_proveo_agent_home",         // the home the AGENT runs with, not this process's
		"_proveo_persist_tool_env",   // the resolved PATH, written where bash will read it
		"proveo_compose_house_rules", // §7e proveo's conventions as user-level instructions
		"_proveo_write_block",        // marked-region rewrite that spares operator content
		"proveo_apply_ui_defaults",   // §7g sandbox theme + syntax highlighting
	}
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			img := harnessImage(t, name)
			for _, fn := range required {
				c := exec.Command("docker", "run", "--rm", "--entrypoint", "bash", img,
					"-c", "grep -q "+fn+" /entrypoint-lib.sh && echo present || echo absent")
				o, e := c.CombinedOutput()
				if e != nil {
					t.Fatalf("probe %s for %s: %v\n%s", img, fn, e, o)
				}
				if !strings.Contains(string(o), "present") {
					t.Errorf("%s ships a STALE entrypoint-lib.sh: %q is missing. "+
						"Rebuild it (`proveo build %s`) — the source fix is not live until you do.",
						name, fn, name)
				}
			}
		})
	}
}

func TestToolchainShipsNoDuplicateBinaries(t *testing.T) {
	toolchainImage(t)
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
