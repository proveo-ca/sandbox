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
		cmd := exec.Command(bash, "-c", `source "$1"; proveo_seed_instructions "$2"`, "bash", lib, target)
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

func TestInstructionSeedRunsFirstInProveoSeed(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "packages/lib/entrypoint-lib.sh")
	_, body, ok := strings.Cut(src, "\nproveo_seed() {\n")
	if !ok {
		t.Fatal("proveo_seed() not found in the shape this guard reads")
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[1]) != `proveo_seed_instructions "$target"` {
		t.Errorf("proveo_seed must seed instructions right after resolving its target, before any install; got %q", lines[:2])
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
