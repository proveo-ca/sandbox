// SPEC: _spec/packages/lib/seed-and-launch.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstructionSeedWritesAgentsMdOnlyIntoABareWorkspace(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	lib := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	defaults := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(defaults, []byte("proveo defaults\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := func(t *testing.T, target string, carries ...string) (work, marker string) {
		t.Helper()
		work, marker = t.TempDir(), filepath.Join(t.TempDir(), "seeded")
		for _, f := range carries {
			if err := os.WriteFile(filepath.Join(work, f), []byte("project\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(bash, "-c", `source "$1"; proveo_seed_instructions "$2"; proveo_release_agent "$2"`, "bash", lib, target)
		cmd.Env = append(cmd.Environ(), "PROVEO_WORKDIR="+work,
			"PROVEO_INSTRUCTIONS_DEFAULTS="+defaults, "PROVEO_INSTRUCTIONS_MARKER="+marker)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("proveo_seed_instructions: %v\n%s", err, out)
		}
		return work, marker
	}

	work, marker := seed(t, "claudecode")
	if b, _ := os.ReadFile(filepath.Join(work, "AGENTS.md")); string(b) != "proveo defaults\n" {
		t.Errorf("a workspace carrying neither file must get the default AGENTS.md, got %q", b)
	}
	if _, err := os.Stat(filepath.Join(work, "CLAUDE.md")); err == nil {
		t.Error("the seed must not write CLAUDE.md: Claude Code reads AGENTS.md natively since 2.1.277")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the seed must mark the boot seeded: %v", err)
	}

	for _, own := range []string{"AGENTS.md", "CLAUDE.md"} {
		work, marker := seed(t, "claudecode", own)
		if b, _ := os.ReadFile(filepath.Join(work, own)); string(b) != "project\n" {
			t.Errorf("the project's own %s must stay untouched, got %q", own, b)
		}
		if own == "CLAUDE.md" {
			if _, err := os.Stat(filepath.Join(work, "AGENTS.md")); err == nil {
				t.Error("a project carrying CLAUDE.md must not get a default AGENTS.md")
			}
		}
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("a project carrying %s must still mark the boot seeded: %v", own, err)
		}
	}

	if work, _ := seed(t, "codex"); fileExists(filepath.Join(work, "AGENTS.md")) {
		t.Error("only claudecode ships default instructions; codex must leave the workspace untouched")
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
		"render_subagents claudecode", "proveo_wire_config", "proveo_compose_house_rules", "proveo_install_claude_hooks"} {
		pos = at(before, pos)
		if pos > release {
			t.Errorf("%s runs after proveo_release_agent — the agent would start without it", before)
		}
	}
	for _, after := range []string{"proveo_sync_tools restore", "proveo_provision_toolchain", "proveo_wire_config"} {
		if at(after, release) < release {
			t.Errorf("%s must run after the release: installs are what the agent must not wait on", after)
		}
	}
	wrapper := readRepoFile(t, "packages/lib/proveo-seed")
	if !strings.Contains(wrapper, "proveo_seed \"$@\" || rc=$?\n[[ -e \"$PROVEO_INSTRUCTIONS_MARKER\" ]] || proveo_release_agent") {
		t.Error("proveo-seed must release the agent even when proveo_seed fails, or the agent waits out the full limit")
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
		cmd.Env = append(cmd.Environ(), "SANDBOX_VM_ID="+vmID, "PROVEO_WORKDIR="+workdir,
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
		if err := os.WriteFile(filepath.Join(home, f), []byte(`{"lsp":{"typescript":{}}}`), 0o644); err != nil {
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
// entrypoint exports but the Kit does not aborts the WHOLE seed. codex's LSP
// wiring read $CODEX_HOME unset and took the release, the snapshot and every
// install with it ("CODEX_HOME: unbound variable", 2026-09-29).
func TestEverySeedSurvivesAnUnsetHarnessHome(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	for _, target := range []string{"claudecode", "codex", "cursor", "cecli", "opencode", "hermes"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			home, ws, bin := t.TempDir(), t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(ws, "index.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Every LSP wiring function returns early without its bridge, so the
			// stub is what lets each one reach the variables it reads.
			for _, b := range []string{"mcp-language-server", "typescript-language-server"} {
				if err := os.WriteFile(filepath.Join(bin, b), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export HOME="$2" PROVEO_WORKDIR="$3" PROVEO_INSTRUCTIONS_MARKER="$2/released" PROVEO_RELEASE_SNAPSHOT="$2/snap" PROVEO_HOOKS_MARKER="$2/hooks" PATH="$5:$PATH"
unset CODEX_HOME CECLI_HOME CURSOR_CONFIG_DIR PROVEO_HOME
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
// so the seed's first LSP wiring pass could not see the npm-baked servers.
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
