//go:build e2e

// SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/internal/sbx/clone-workspace.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// readySpec is what one harness must hold when the seed releases its agent.
type readySpec struct {
	agents   string   // subagent dir under the agent's home; "" when the harness renders none
	ext      string   // subagent file extension; "md" unless the harness reads another
	lspFiles []string // startup-read LSP wiring under the agent's home; nil when it wires none
	baked    string   // regexp of a server the image bakes, so the first wiring pass names it
	register string   // CLI that lists registered subagents offline; "" when none does
	assist   bool     // a general assistant: the seed must provision nothing
}

var readySpecs = map[string]readySpec{
	"claudecode": {agents: ".claude/agents", lspFiles: []string{".claude/skills/proveo-lsp/.lsp.json", ".claude/settings.json"}, baked: "typescript|pyright", register: "claude"},
	"codex":      {agents: ".codex/agents", ext: "toml", lspFiles: []string{".codex/config.toml"}, baked: "typescript|pyright"},
	"opencode":   {agents: ".config/opencode/agents", lspFiles: []string{".config/opencode/opencode.json"}, baked: "typescript|pyright", register: "opencode"},
	"cursor":     {agents: ".cursor/agents", lspFiles: []string{".cursor/mcp.json"}, baked: "gopls"},
	"cecli":      {agents: ".cecli/agents"},
	"hermes":     {assist: true},
}

const (
	releaseMarker = "/dev/shm/proveo-instructions-seeded"
	releaseSnap   = "/dev/shm/proveo-release-snapshot"
	seedDone      = "/dev/shm/proveo-seed-done"
)

// TestSeedReleasesEveryHarnessReady launches every sbx harness on a fresh clone
// under the default broker credentials and asserts what its agent has AT
// LAUNCH — read from the snapshot the seed takes as it releases the agent —
// then what the seed installs after it.
func TestSeedReleasesEveryHarnessReady(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	proveoBin := buildProveo(t)
	// One VM at a time: every seed installs into the shared ~/.proveo/toolchains
	// under an flock, so two cold seeds queued behind each other past the
	// install probe's budget (measured 2026-09-29; cursor alone: 29s).
	gate := make(chan struct{}, 1)
	for _, target := range sandboxHarnesses {
		spec, known := readySpecs[target]
		t.Run(target, func(t *testing.T) {
			if !known {
				t.Fatalf("%s runs on sbx but readySpecs has no row for it — a missing row reads as an oversight", target)
			}
			t.Parallel()
			gate <- struct{}{}
			defer func() { <-gate }()
			requireHarness(t, target)
			seedReadyProbe(t, proveoBin, target, spec)
		})
	}
}

func readyWorkspace(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	writeFile(t, filepath.Join(work, ".gitignore"), []byte(".env\n_spec\n"))
	writeFile(t, filepath.Join(work, "index.ts"), []byte("export const a: number = 1\n"))
	writeFile(t, filepath.Join(work, "main.py"), []byte("print('probe')\n"))
	writeFile(t, filepath.Join(work, "go.mod"), []byte("module probe\n\ngo 1.22\n"))
	writeFile(t, filepath.Join(work, "main.go"), []byte("package main\n\nfunc main() {}\n"))
	gitInit(t, work)
	writeFile(t, filepath.Join(work, ".env"), []byte("DB_URL=postgres://probe\nANTHROPIC_API_KEY=sk-e2e-must-be-stripped\n"))
	specs := t.TempDir()
	writeFile(t, filepath.Join(specs, "overview.puml"), []byte("@startuml\n@enduml\n"))
	if err := os.Symlink(specs, filepath.Join(work, "_spec")); err != nil {
		t.Fatal(err)
	}
	return work
}

func seedReadyProbe(t *testing.T, proveoBin, target string, spec readySpec) {
	t.Helper()
	work := readyWorkspace(t)
	sess := tmux.New(fmt.Sprintf("proveo-ready-%s-%d", target, time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	// Broker is the default under test; an empty key file keeps the project
	// .env's provider key out of sbx's host-wide secret store.
	cmd := []string{"env", "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_EGRESS_ENV_FILE=/dev/null",
		proveoBin, "run", target, "--egress-mode", "open", "--input", work, "--shell"}
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start %s --shell: %v", target, err)
	}
	waitForContainerShell(t, newWatcher(t, sess), durationEnv(t, "PROVEO_TEST_TIMEOUT", 4*time.Minute))

	launch, status := shellExec(t, sess, readyLaunchScript(spec), 5*time.Minute)
	if status != 0 {
		t.Fatalf("launch probe exited %d:\n%s", status, launch)
	}
	want := []string{"released=yes", "env_db=1", "env_key=0", "env_rw=writable", "refused=[]", "spec=overview.puml", "git_clean=0"}
	if spec.agents != "" {
		want = append(want, "agents_late=0")
	}
	if spec.lspFiles != nil {
		want = append(want, "lsp_at_launch=yes")
	}
	if spec.register != "" {
		want = append(want, "registered=yes")
	}
	for _, w := range want {
		if !strings.Contains(launch, w) {
			t.Errorf("%s at launch: probe lacks %q\n%s", target, w, launch)
		}
	}
	if spec.agents != "" && strings.Contains(launch, "agents_at_launch=0") {
		t.Errorf("%s: no subagent in the release snapshot — the agent starts without them\n%s", target, launch)
	}

	if spec.assist {
		after, status := shellExec(t, sess, fmt.Sprintf(`for i in $(seq 1 300); do [ -e %[1]s ] && break; sleep 1; done
echo "seed_done=$([ -e %[1]s ] && echo yes || echo no)"
echo "skipped=$(grep -c 'assistant: no toolchain' /var/log/sbx-kit-startup.log 2>/dev/null)"
echo "installed=$(grep -cE 'Installing |language server' /var/log/sbx-kit-startup.log 2>/dev/null)"
`, seedDone), 6*time.Minute)
		if status != 0 || !strings.Contains(after, "seed_done=yes") || strings.Contains(after, "skipped=0") || !strings.Contains(after, "installed=0") {
			t.Errorf("%s is an assistant: its seed must skip every toolchain, dependency and LSP install (exit %d)\n%s", target, status, after)
		}
		return
	}
	if spec.lspFiles == nil {
		return
	}
	after, status := shellExec(t, sess, readyInstallScript(spec), durationEnv(t, "PROVEO_TEST_SEED_TIMEOUT", 15*time.Minute))
	if status != 0 {
		t.Fatalf("install probe exited %d:\n%s", status, after)
	}
	for _, w := range []string{"seed_done=yes", "gopls=yes", "gopls_wired=yes"} {
		if !strings.Contains(after, w) {
			t.Errorf("%s after the seed: probe lacks %q — the seed's own install, not a hand call, must leave gopls wired\n%s", target, w, after)
		}
	}
}

func readyLaunchScript(spec readySpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "for i in $(seq 1 180); do [ -e %s ] && break; sleep 1; done\n", releaseMarker)
	fmt.Fprintf(&b, "echo \"released=$([ -e %s ] && echo yes || echo no)\"\n", releaseMarker)
	if spec.agents != "" {
		ext := spec.ext
		if ext == "" {
			ext = "md"
		}
		fmt.Fprintf(&b, "echo \"agents_at_launch=$(find %s -path '*/%s/*.%s' 2>/dev/null | wc -l | tr -d ' ')\"\n", releaseSnap, spec.agents, ext)
		fmt.Fprintf(&b, "late=0; for f in $(find /home /app -path '*/%s/*.%s' 2>/dev/null); do "+
			"[ \"$(stat -c %%Z \"$f\")\" -le \"$(stat -c %%Y %s)\" ] || late=$((late+1)); done; echo \"agents_late=$late\"\n",
			spec.agents, ext, releaseMarker)
	}
	if spec.lspFiles != nil {
		var pats []string
		for _, f := range spec.lspFiles {
			pats = append(pats, fmt.Sprintf("-path '*/%s'", f))
		}
		fmt.Fprintf(&b, "files=$(find %s -type f \\( %s \\) 2>/dev/null); "+
			"echo \"lsp_at_launch=$([ -n \"$files\" ] && grep -qE '%s' $files && echo yes || echo no)\"\n",
			releaseSnap, strings.Join(pats, " -o "), spec.baked)
	}
	switch spec.register {
	case "claude":
		b.WriteString("echo \"registered=$(timeout 90 claude -p probe --output-format stream-json --verbose --max-turns 1 2>/dev/null | head -1 | grep -q '\"adversarial-reviewer\"' && echo yes || echo no)\"\n")
	case "opencode":
		b.WriteString("echo \"registered=$(timeout 90 opencode agent list 2>/dev/null | grep -q '^adversarial-reviewer (subagent)' && echo yes || echo no)\"\n")
	}
	b.WriteString(`echo "env_link=$(readlink .env)"
echo "env_db=$(grep -c '^DB_URL=postgres://probe$' .env 2>/dev/null)"
echo "env_key=$(grep -c '^ANTHROPIC_API_KEY=' .env 2>/dev/null)"
echo "env_rw=$(touch .env 2>/dev/null && echo writable || echo readonly)"
echo "refused=[$(cat /dev/shm/proveo-seed-refused 2>/dev/null)]"
echo "spec=$(ls _spec/ 2>/dev/null | tr '\n' ' ')"
echo "git_clean=$(git status --porcelain | grep -cE '\.env|_spec')"
`)
	return b.String()
}

func readyInstallScript(spec readySpec) string {
	var pats []string
	for _, f := range spec.lspFiles {
		pats = append(pats, fmt.Sprintf("-path '*/%s'", f))
	}
	return fmt.Sprintf(`for i in $(seq 1 900); do [ -e %[1]s ] && break; sleep 1; done
echo "seed_done=$([ -e %[1]s ] && echo yes || echo no)"
echo "gopls=$(bash -lic 'command -v gopls' >/dev/null 2>&1 && echo yes || echo no)"
files=$(find /home /app -type f \( %[2]s \) 2>/dev/null)
echo "gopls_wired=$([ -n "$files" ] && grep -qE 'gopls' $files && echo yes || echo no)"
`, seedDone, strings.Join(pats, " -o "))
}
