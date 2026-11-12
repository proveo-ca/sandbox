// SPEC: _spec/packages/lib/seed-and-launch.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// SPEC: _spec/packages/lib/instruction-injection.puml
func TestInstructionSeedPutsDefaultsInVMLocalContextAndNeverInTheWorkspace(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	lib := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	defaults := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(defaults, []byte("proveo defaults\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	type boot struct{ work, managed, shm, marker string }
	newBoot := func(t *testing.T) boot {
		dir := t.TempDir()
		b := boot{work: t.TempDir(), managed: filepath.Join(dir, "CLAUDE.md"),
			shm: filepath.Join(dir, "proveo-default-instructions.md"), marker: filepath.Join(dir, "seeded")}
		if err := os.WriteFile(b.managed, []byte("house rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return b
	}
	seed := func(t *testing.T, b boot, target string) {
		t.Helper()
		cmd := exec.Command(bash, "-c", `source "$1"; proveo_seed_instructions "$2"; proveo_release_agent "$2"`, "bash", lib, target)
		cmd.Env = append(cmd.Environ(), "PROVEO_WORKDIR="+b.work, "PROVEO_INSTRUCTIONS_DEFAULTS="+defaults,
			"PROVEO_INSTRUCTIONS_MARKER="+b.marker, "PROVEO_CLAUDE_MANAGED_MD="+b.managed, "PROVEO_OPENCODE_INSTRUCTIONS="+b.shm)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("proveo_seed_instructions %s: %v\n%s", target, err, out)
		}
	}
	carry := func(t *testing.T, b boot, f string) {
		if err := os.WriteFile(filepath.Join(b.work, f), []byte("project\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	untouched := func(t *testing.T, b boot, own ...string) {
		t.Helper()
		entries, _ := os.ReadDir(b.work)
		if len(entries) != len(own) {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("the seed wrote into the workspace: %q, want only %q", names, own)
		}
		if _, err := os.Stat(b.marker); err != nil {
			t.Errorf("the seed must still mark the boot seeded: %v", err)
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	b := newBoot(t)
	seed(t, b, "claudecode")
	seed(t, b, "claudecode")
	untouched(t, b)
	if got := read(b.managed); !strings.HasPrefix(got, "house rules\n") || strings.Count(got, "proveo defaults\n") != 1 {
		t.Errorf("a bare workspace must put the defaults once in the managed CLAUDE.md, after the house rules:\n%s", got)
	}
	carry(t, b, "AGENTS.md")
	seed(t, b, "claudecode")
	untouched(t, b, "AGENTS.md")
	if got := read(b.managed); got != "house rules\n" {
		t.Errorf("a workspace that now carries AGENTS.md must take the defaults back out:\n%q", got)
	}

	for _, own := range []string{"AGENTS.md", "CLAUDE.md"} {
		b := newBoot(t)
		carry(t, b, own)
		seed(t, b, "claudecode")
		seed(t, b, "opencode")
		untouched(t, b, own)
		if read(b.managed) != "house rules\n" || fileExists(b.shm) {
			t.Errorf("a project carrying %s must get no defaults", own)
		}
	}

	b = newBoot(t)
	seed(t, b, "opencode")
	untouched(t, b)
	if read(b.shm) != "proveo defaults\n" {
		t.Errorf("a bare opencode workspace must get the defaults in the file /etc/opencode lists, got %q", read(b.shm))
	}
	carry(t, b, "AGENTS.md")
	seed(t, b, "opencode")
	if fileExists(b.shm) {
		t.Error("an opencode workspace that now carries AGENTS.md must lose the defaults file")
	}

	b = newBoot(t)
	seed(t, b, "codex")
	untouched(t, b)
	if read(b.managed) != "house rules\n" || fileExists(b.shm) {
		t.Error("codex ships no default instructions and must change nothing")
	}
}

// TestTheOpencodeImageListsTheDefaultsWhereOpencodeReadsThem pins the image file the opencode seed relies on.
func TestTheOpencodeImageListsTheDefaultsWhereOpencodeReadsThem(t *testing.T) {
	t.Parallel()
	oc := readRepoFile(t, "defs/opencode/managed/opencode.json")
	if !json.Valid([]byte(oc)) {
		t.Fatal("a system opencode.json that does not parse stops opencode from starting")
	}
	if strings.TrimSpace(oc) != "{\n  \"instructions\": [\"/dev/shm/proveo-default-instructions.md\"]\n}" {
		t.Errorf("/etc/opencode/opencode.json must hold only the instructions entry; a managed scalar overrides user config:\n%s", oc)
	}
	if df := readRepoFile(t, "defs/opencode/Dockerfile"); !strings.Contains(df, "COPY defs/opencode/managed/opencode.json /etc/opencode/opencode.json") {
		t.Error("the opencode image must bake /etc/opencode/opencode.json")
	}
	if ep := readRepoFile(t, "defs/opencode/entrypoint.sh"); strings.Contains(ep, "seed_project_agents_md") || regexp.MustCompile(`cp [^\n]*AGENTS\.md`).MatchString(ep) {
		t.Error("the opencode entrypoint must not write AGENTS.md into the workspace")
	}
}

// TestTheSeedSetsClaudeCodeToLoadAgentsMdBesideClaudeMd pins the user-settings key the seed merges.
func TestTheSeedSetsClaudeCodeToLoadAgentsMdBesideClaudeMd(t *testing.T) {
	t.Parallel()
	settings := func(home string) map[string]any {
		var j map[string]any
		b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		_ = json.Unmarshal(b, &j)
		return j
	}
	choice := func(j map[string]any) any {
		pc, _ := j["pluginConfigs"].(map[string]any)
		p, _ := pc["cc-plugin-agents-md@builtin"].(map[string]any)
		o, _ := p["options"].(map[string]any)
		return o["instructionFiles"]
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"),
		[]byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"x"}]}]},"pluginConfigs":{"other@builtin":{"options":{"k":1}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	libRun(t, home, `proveo_claude_reads_agents_md claudecode`)
	libRun(t, home, `proveo_claude_reads_agents_md claudecode`)
	j := settings(home)
	if choice(j) != "claude-md-and-agents-md" {
		t.Errorf("claudecode settings must load AGENTS.md beside CLAUDE.md, got %v", choice(j))
	}
	if _, ok := j["hooks"]; !ok {
		t.Error("the merge dropped the existing hooks")
	}
	if pc, _ := j["pluginConfigs"].(map[string]any); pc["other@builtin"] == nil {
		t.Error("the merge dropped another plugin's options")
	}

	chosen := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chosen, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chosen, ".claude", "settings.json"),
		[]byte(`{"pluginConfigs":{"cc-plugin-agents-md@builtin":{"options":{"instructionFiles":"managed-only"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	libRun(t, chosen, `proveo_claude_reads_agents_md claudecode`)
	if got := choice(settings(chosen)); got != "managed-only" {
		t.Errorf("an operator's own instructionFiles choice must stand, got %v", got)
	}

	other := t.TempDir()
	libRun(t, other, `proveo_claude_reads_agents_md opencode`)
	if fileExists(filepath.Join(other, ".claude", "settings.json")) {
		t.Error("opencode got a Claude Code settings file")
	}
}

// TestSeedReleasesTheAgentAfterConfigAndBeforeInstalls pins the release point:
// Claude Code reads subagents, plugins, hooks and instructions once, at startup.
func TestSeedReleasesTheAgentAfterConfigAndBeforeInstalls(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "packages/lib/entrypoint-lib.sh")
	_, body, ok := strings.Cut(src, "\nproveo_seed() {\n")
	if !ok {
		t.Fatal("proveo_seed() not found in the shape this guard reads")
	}
	body, _, _ = strings.Cut(body, "\n}\n")
	at := func(step string, from int) int {
		i := strings.Index(body[from:], step)
		if i < 0 {
			t.Fatalf("proveo_seed no longer calls %s after offset %d", step, from)
		}
		return from + i
	}
	release := at("\n proveo_release_agent \"$target\"\n", 0)
	pos := 0
	for _, before := range []string{"proveo_clone_checkout", "proveo_clone_env", "proveo_clone_links", "proveo_seed_instructions",
		"render_subagents claudecode", "proveo_wire_config", "proveo_compose_house_rules", "proveo_install_claude_hooks",
		"proveo_claude_reads_agents_md"} {
		pos = at(before, pos)
		if pos > release {
			t.Errorf("%s runs after proveo_release_agent — the agent would start without it", before)
		}
	}
	at(`_proveo_seed_provision "$target"`, release)
	for _, install := range []string{"proveo_provision_toolchain"} {
		if strings.Contains(body[:release], install) {
			t.Errorf("%s runs before the release: installs are what the agent must not wait on", install)
		}
	}
	if !strings.Contains(body[:release], "if proveo_env_on PROVEO_MISE_TRUST; then\n  proveo_seed_step setup \"restoring toolchain folders before mise trust\"") {
		t.Error("the foreground tool restore must require the explicit mise trust choice")
	}
	prov := provisionBody(t, src)
	last := -1
	for _, step := range []string{"proveo_sync_tools restore", "proveo_provision_toolchain"} {
		i := strings.Index(prov, step)
		if i <= last {
			t.Errorf("_proveo_seed_provision lost the restore → provision order at %s", step)
		}
		last = i
	}
	wrapper := readRepoFile(t, "packages/lib/proveo-seed")
	if !strings.Contains(wrapper, "proveo_seed \"$@\" || rc=$?\n[[ -e \"$PROVEO_INSTRUCTIONS_MARKER\" ]] || proveo_release_agent") {
		t.Error("proveo-seed must release the agent even when proveo_seed fails, or the agent waits out the full limit")
	}
	if strings.Contains(wrapper, "proveo-opencode-runtime") {
		t.Error("the seed must not gate the release on OpenCode history; the engine owns its database")
	}
}

func TestAwaitSeedGatesOnlyProveoLaunchedSbxSessions(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	await := filepath.Join(repoRoot(t), "packages", "lib", "proveo-await-seed")
	run := func(vmID, workdir, marker, wait string) (string, time.Duration) {
		cmd := exec.Command(sh, await, "echo", "LAUNCHED")
		cmd.Env = append(outsideSbx(cmd.Environ()), "SANDBOX_VM_ID="+vmID, "PROVEO_WORKDIR="+workdir,
			"PROVEO_INSTRUCTIONS_MARKER="+marker, "PROVEO_INSTRUCTIONS_WAIT="+wait)
		start := time.Now()
		out, _ := cmd.CombinedOutput()
		return string(out), time.Since(start)
	}

	missing := filepath.Join(t.TempDir(), "seeded")
	if out, took := run("", "/w", missing, "5"); !strings.Contains(out, "LAUNCHED") || took > 2*time.Second {
		t.Errorf("without sbx the launch must not wait (took %s):\n%s", took, out)
	}
	if out, took := run("proveo-claudecode-0cae7b86", "", missing, "5"); !strings.Contains(out, "LAUNCHED") || took > 2*time.Second {
		t.Errorf("a bare sbx run of the image has no proveo seed to wait for and must not wait (took %s):\n%s", took, out)
	}

	late := filepath.Join(t.TempDir(), "seeded")
	go func() {
		time.Sleep(time.Second)
		_ = os.WriteFile(late, nil, 0o644)
	}()
	if out, took := run("proveo-claudecode-0cae7b86", "/w", late, "10"); !strings.Contains(out, "LAUNCHED") ||
		took < 800*time.Millisecond || strings.Contains(out, "launching anyway") {
		t.Errorf("under sbx the launch must wait for the marker, then run (took %s):\n%s", took, out)
	}

	if out, _ := run("proveo-claudecode-0cae7b86", "/w", missing, "1"); !strings.Contains(out, "launching anyway") ||
		!strings.Contains(out, "LAUNCHED") {
		t.Errorf("a seed that never lands must warn and launch, never hang:\n%s", out)
	}
}

func TestAwaitSeedStopsOnlyOnASeedRefusal(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	await := filepath.Join(repoRoot(t), "packages", "lib", "proveo-await-seed")
	dir := t.TempDir()
	marker := filepath.Join(dir, "seeded")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	refused := filepath.Join(dir, "refused")
	launch := func(body string) (string, error) {
		if err := os.WriteFile(refused, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, await, "echo", "LAUNCHED")
		cmd.Env = append(cmd.Environ(), "SANDBOX_VM_ID=proveo-opencode-177e7812", "PROVEO_WORKDIR=/w",
			"PROVEO_INSTRUCTIONS_MARKER="+marker, "PROVEO_SEED_REFUSED="+refused)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := launch(""); err != nil || !strings.Contains(out, "LAUNCHED") || strings.Contains(out, "did not release") {
		t.Fatalf("an empty refusal file must not block the launch: %v\n%s", err, out)
	}
	if out, err := launch("shares: the workspace mount is missing\n"); err == nil || !strings.Contains(out, "did not release") {
		t.Fatalf("a seed refusal must still stop the launch: %v\n%s", err, out)
	}
}

func TestClaudecodeImageRoutesLaunchesThroughAwaitSeed(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/claudecode/mcp/Dockerfile")
	for _, need := range []string{
		"packages/lib/proveo-await-seed /usr/local/bin/proveo-await-seed",
		"> /opt/proveo/shims/claude",
		">> /etc/bash.bashrc",
		"ENV PATH=/opt/proveo/shims:${PATH}",
	} {
		if !strings.Contains(df, need) {
			t.Errorf("defs/claudecode/mcp/Dockerfile must carry %q so every sbx launch waits for the seed", need)
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestReleaseSnapshotsWhatTheAgentReadsAtStartup(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	home, snap, marker := t.TempDir(), filepath.Join(t.TempDir(), "snap"), filepath.Join(t.TempDir(), "released")
	for _, f := range []string{".config/opencode/agents/adversarial-reviewer.md", ".config/opencode/opencode.json", ".claude/agents/x.md"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(home, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, f), []byte(`{"model":"x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bash, "-c", `set -euo pipefail; source "$1/packages/lib/entrypoint-lib.sh"; proveo_release_agent opencode; echo DONE`, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "HOME="+home, "PROVEO_HOME=", "PROVEO_RELEASE_SNAPSHOT="+snap, "PROVEO_INSTRUCTIONS_MARKER="+marker)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("proveo_release_agent: %v\n%s", err, out)
	}
	for _, f := range []string{".config/opencode/agents/adversarial-reviewer.md", ".config/opencode/opencode.json"} {
		if _, err := os.Stat(filepath.Join(snap, home, f)); err != nil {
			t.Errorf("snapshot lacks %s — the e2e reads what the agent saw at launch from it: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(snap, home, ".claude")); err == nil {
		t.Error("opencode's snapshot carried another harness's config")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the release marker was not written: %v", err)
	}
}

func TestAnAssistantSeedProvisionsNothing(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	home, ws := t.TempDir(), t.TempDir()
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export HOME="$2" PROVEO_WORKDIR="$3" PROVEO_INSTRUCTIONS_MARKER="$2/released" PROVEO_RELEASE_SNAPSHOT="$2/snap"
proveo_provision_toolchain() { echo PROVISIONED; }
proveo_sync_tools() { echo TOOLS_SYNCED; }
proveo_seed hermes
echo SEED_END`
	run := func(kind string) string {
		cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, ws)
		cmd.Env = append(os.Environ(), "PROVEO_AGENT_KIND="+kind, "PROVEO_HOME=")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "SEED_END") {
			t.Fatalf("proveo_seed (kind=%q): %v\n%s", kind, err, out)
		}
		return string(out)
	}
	out := run("assistant")
	if strings.Contains(out, "PROVISIONED") || strings.Contains(out, "TOOLS_SYNCED") {
		t.Errorf("an assistant seed provisioned a toolchain — hermes is not a coding agent:\n%s", out)
	}
	if !strings.Contains(out, "assistant: no toolchain") {
		t.Errorf("the skip must be said out loud:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "released")); err != nil {
		t.Errorf("an assistant must still be released: %v", err)
	}
	if out := run(""); !strings.Contains(out, "PROVISIONED") {
		t.Errorf("a coding harness lost its provisioning:\n%s", out)
	}
}

// proveo-seed runs under `set -euo pipefail`, so one bare $VAR a harness
// entrypoint exports but the Kit does not aborts the WHOLE seed.
func TestEverySeedSurvivesAnUnsetHarnessHome(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	for _, target := range []string{"claudecode", "codex", "cursor", "opencode", "hermes"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			home, ws, bin := t.TempDir(), t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(ws, "index.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export HOME="$2" PROVEO_WORKDIR="$3" PROVEO_INSTRUCTIONS_MARKER="$2/released" PROVEO_RELEASE_SNAPSHOT="$2/snap" PROVEO_HOOKS_MARKER="$2/hooks" PATH="$5:$PATH"
export PROVEO_OPENCODE_DEFAULTS_DIR="$1/defs/opencode/defaults"
unset CODEX_HOME CURSOR_CONFIG_DIR PROVEO_HOME
proveo_provision_toolchain() { :; }
proveo_sync_tools() { :; }
proveo_sync_state() { :; }
proveo_sync_config() { :; }
proveo_seed "$4"
echo SEED_END`
			out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, ws, target, bin).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "SEED_END") {
				t.Fatalf("proveo_seed %s aborted under set -u with no harness home exported: %v\n%s", target, err, out)
			}
		})
	}
}

// sbx runs kit startup steps through su, which resets PATH to Debian's default,
// so the seed could not see the image's npm-global binaries.
func TestSeedRestoresTheImagePathSuDropped(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	image := "/opt/proveo/shims:/usr/local/share/npm-global/bin:/usr/bin:/bin"
	if err := os.WriteFile(filepath.Join(proc, "1", "environ"), []byte("HOME=/root\x00PATH="+image+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", `set -euo pipefail; source "$1/packages/lib/entrypoint-lib.sh"; proveo_restore_image_path; proveo_restore_image_path; printf '%s' "$PATH"`, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_PROC_ROOT="+proc, "PATH=/usr/local/bin:/usr/bin:/bin")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.HasPrefix(got, image+":") || strings.Count(got, "npm-global") != 1 {
		t.Errorf("PATH = %q — the image's PATH must lead, once", got)
	}
	if !strings.Contains(readRepoFile(t, "packages/lib/proveo-seed"), "proveo_restore_image_path\nproveo_seed") {
		t.Error("proveo-seed must restore the image PATH before the seed runs")
	}
}
