//go:build e2e

// SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/internal/sbx/clone-workspace.puml, _spec/internal/sbx/startup-dispatch.puml, _spec/defs/opencode/engine-local-history.puml

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
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
	ready    string   // text the agent's own prompt shows; "" waits for the release and a quiet pane
}

var readySpecs = map[string]readySpec{
	"claudecode": {agents: ".claude/agents", lspFiles: []string{".claude/skills/proveo-lsp/.lsp.json", ".claude/settings.json"}, baked: "typescript|pyright", register: "claude", ready: "bypass permissions"},
	"codex":      {agents: ".codex/agents", ext: "toml", lspFiles: []string{".codex/config.toml"}, baked: "typescript|pyright", ready: "Ask Codex to do anything"},
	"opencode":   {agents: ".config/opencode/agents", lspFiles: []string{".config/opencode/opencode.json"}, baked: "typescript|pyright", register: "opencode", ready: "Ask anything"},
	"cursor":     {agents: ".cursor/agents", lspFiles: []string{".cursor/mcp.json"}, baked: "gopls", ready: "Cursor Agent"},
	"cecli":      {agents: ".cecli/agents"},
	"hermes":     {assist: true},
}

const (
	releaseMarker = "/dev/shm/proveo-instructions-seeded"
	releaseSnap   = "/dev/shm/proveo-release-snapshot"
	seedDone      = "/dev/shm/proveo-seed-done"
)

const opencodeRegistryProbe = `timeout 90 bash -c '
set -eu
probe="$(mktemp -d)"
port="$(python3 -c "import socket; s=socket.socket(); s.bind((\"127.0.0.1\", 0)); print(s.getsockname()[1]); s.close()")"
native=/usr/local/share/npm-global/bin/opencode
export OPENCODE_PASSWORD=synthetic-registry-probe
export OPENCODE_DB="$probe/opencode.db" XDG_DATA_HOME="$probe/data" XDG_STATE_HOME="$probe/state"
"$native" serve --hostname 127.0.0.1 --port "$port" >"$probe/server.log" 2>&1 &
server=$!
trap "kill $server 2>/dev/null || true; wait $server 2>/dev/null || true; rm -rf \"$probe\"" EXIT
url="http://127.0.0.1:$port"
until "$native" api --server "$url" GET /api/config >"$probe/config.json" 2>"$probe/client.log"; do
  kill -0 "$server" 2>/dev/null || { cat "$probe/server.log"; exit 1; }
  sleep 0.2
done
until "$native" api --server "$url" GET /api/agent | node -e "const response = JSON.parse(require(\"node:fs\").readFileSync(0, \"utf8\")); console.log(JSON.stringify(response.data.map(a => ({id:a.id,mode:a.mode})))); process.exit(response.data.some(a => a.id === \"adversarial-reviewer\" && a.mode === \"subagent\") ? 0 : 1)"; do
  kill -0 "$server" 2>/dev/null || { cat "$probe/server.log"; exit 1; }
  sleep 0.2
done
'`

func TestOpencodeV2SeedReadyAgentRegistry(t *testing.T) {
	requireDocker(t)
	image := harnessImageName("opencode")
	if !imageExists(image) {
		t.Skipf("harness image %s not built", image)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none",
		"--env", "OPENCODE_DISABLE_MODELS_FETCH=1",
		"--entrypoint", "bash", image, "-c", `set -eu
source /entrypoint-lib.sh
proveo_bootstrap_opencode_config
render_subagents opencode "$HOME/.config/opencode/agents"
`+opencodeRegistryProbe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode v2 registry probe: %v\n%s", err, out)
	}
}

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
			t.Cleanup(func() { <-gate })
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
	run := launchReadyKit(t, proveoBin, target, work, spec.ready)

	launch, status := run(readyLaunchScript(spec), 5*time.Minute)
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
		after, status := run(fmt.Sprintf(`for i in $(seq 1 300); do [ -e %[1]s ] && break; sleep 1; done
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
	after, status := run(readyInstallScript(spec), durationEnv(t, "PROVEO_TEST_SEED_TIMEOUT", 15*time.Minute))
	if status != 0 {
		t.Fatalf("install probe exited %d:\n%s", status, after)
	}
	for _, w := range []string{"seed_done=yes", "gopls=yes", "gopls_wired=yes"} {
		if !strings.Contains(after, w) {
			t.Errorf("%s after the seed: probe lacks %q — the seed's own install, not a hand call, must leave gopls wired\n%s", target, w, after)
		}
	}
}

// readyProbe runs a probe script where the agent runs and returns its output and exit status.
type readyProbe func(script string, timeout time.Duration) (string, int)

var kitSandboxRE = regexp.MustCompile(`(?:retained engine |new engine |Created sandbox |in sandbox ')(proveo-[a-z0-9-]+)`)

// launchReadyKit starts `proveo run <target>` through its kit under a fresh
// PROVEO_HOME, names the sandbox it runs in, times the agent's prompt against
// the release and keystroke echo in that prompt, and probes through `sbx exec`
// as the agent's user. ready is the prompt's text; "" waits for the release
// marker and a quiet pane instead.
func launchReadyKit(t *testing.T, proveoBin, target, work, ready string) readyProbe {
	t.Helper()
	home := t.TempDir()
	before, _ := sbxSandboxNames()
	var engine string
	name := func(screen string) string {
		if m := kitSandboxRE.FindStringSubmatch(screen); m != nil && strings.HasPrefix(m[1], "proveo-"+target+"-") {
			return m[1]
		}
		var mine []string
		for _, n := range newSandboxes(before) {
			if strings.HasPrefix(n, "proveo-"+target+"-") && !strings.HasSuffix(n, "-prepare") {
				mine = append(mine, n)
			}
		}
		if len(mine) == 1 {
			return mine[0]
		}
		return ""
	}
	t.Cleanup(func() {
		if engine == "" {
			engine = name("")
		}
		if engine == "" {
			return
		}
		if out, err := exec.Command(sbx.Binary, sbx.RemoveArgs(engine)...).CombinedOutput(); err != nil && !sbx.NotFound(string(out)) {
			t.Logf("cleanup: %s rm %s: %v\n%s", sbx.Binary, engine, err, out)
		}
		if cache, err := os.UserCacheDir(); err == nil {
			_ = os.Remove(filepath.Join(cache, "proveo", "sbx-sandboxes", engine+".json"))
		}
	})
	sess := tmux.New(fmt.Sprintf("proveo-ready-%s-%d", target, time.Now().UnixNano()), nil)
	cmd := []string{"env", "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_ENV_FILE=/dev/null",
		"PROVEO_HOME=" + home, proveoBin, "run", target, "--input", work}
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start %s: %v", target, err)
	}
	pid, _ := sess.PanePID()
	t.Cleanup(func() { killAndAwait(t, sess, pid, durationEnv(t, "PROVEO_TEST_TEARDOWN", 3*time.Minute)) })
	w := newWatcher(t, sess)
	var promptAt time.Time
	w.until("the "+target+" prompt", durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute), func() bool {
		if engine == "" {
			engine = name(w.Screen())
		}
		if engine == "" {
			return false
		}
		if ready == "" {
			out, _ := sbxExecStatus(t, engine, "test -e "+releaseMarker+" && echo released", time.Minute)
			return strings.Contains(out, "released")
		}
		pane, _ := sess.Capture()
		for _, d := range gitSyncDialogs {
			if strings.Contains(pane, d) && !strings.Contains(pane, ready) {
				_ = sess.Enter()
				time.Sleep(2 * time.Second)
				return false
			}
		}
		if strings.Contains(pane, ready) {
			promptAt = time.Now()
			return true
		}
		return false
	})
	if ready == "" {
		awaitQuietScreen(sess, 30*time.Second)
	}

	cd := "export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0='*'\n" + gitSyncCd(work) + "\n"
	run := func(script string, timeout time.Duration) (string, int) {
		return sbxExecStatus(t, engine, cd+script, timeout+time.Minute)
	}
	if out, status := sbxExecStatus(t, engine, "id -u 2>/dev/null", time.Minute); status != 0 || strings.TrimSpace(out) != agentUID {
		t.Fatalf("sbx exec -u %s into %s runs as uid %q (exit %d), not the agent's %s — the probes would not see what the agent sees",
			agentUID, engine, strings.TrimSpace(out), status, agentUID)
	}
	if !promptAt.IsZero() && sbx.BuiltinAgent(target) != "" {
		assertPromptSoonAfterRelease(t, run, target, promptAt)
	}
	out, _ := run("test -e "+seedDone+" && echo installed || echo seeding", time.Minute)
	assertEchoes(t, sess, target, strings.Contains(out, "seeding"))
	return run
}

// assertPromptSoonAfterRelease fails when the agent's prompt appeared later
// after the seed's release than the budget allows.
func assertPromptSoonAfterRelease(t *testing.T, run readyProbe, target string, promptAt time.Time) {
	t.Helper()
	readyMax := durationEnv(t, "PROVEO_TEST_READY_AFTER_RELEASE", 20*time.Second)
	out, _ := run("stat -c %Y "+releaseMarker, time.Minute)
	released, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		t.Logf("%s: no release marker time (%q) — the release gap is unmeasured", target, strings.TrimSpace(out))
		return
	}
	if gap := promptAt.Sub(time.Unix(released, 0)); gap > readyMax {
		t.Errorf("%s: the prompt appeared %s after the release, want <= %s — it waited on the installs again",
			target, gap.Round(time.Second), readyMax)
	}
}

// killAndAwait ends the tmux session, then waits up to timeout for the
// process that ran in its pane to finish its own teardown.
func killAndAwait(t *testing.T, sess *tmux.Session, pid string, timeout time.Duration) {
	t.Helper()
	sess.Kill()
	n, err := strconv.Atoi(strings.TrimSpace(pid))
	if err != nil || n <= 0 {
		return
	}
	proc, err := os.FindProcess(n)
	if err != nil {
		return
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
		if proc.Signal(syscall.Signal(0)) != nil {
			return
		}
	}
	t.Logf("pid %d still tearing down %s after the session ended", n, timeout)
}

// agentUID is the uid every kit step and the agent run as.
// SPEC: _spec/tests/testing-strategy.puml
const agentUID = "1000"

// sbxExecStatus runs script under bash in the named sandbox as agentUID and
// returns its combined output and exit status.
func sbxExecStatus(t *testing.T, name, script string, timeout time.Duration) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sbx.Binary, "exec", "-u", agentUID, "-w", "/", name, "--", "bash", "-c", script)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit) && ctx.Err() == nil:
		return string(out), exit.ExitCode()
	}
	t.Fatalf("sbx exec %s: %v\n%s", name, err, out)
	return "", -1
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
		b.WriteString("echo \"registered=$(timeout 90 claude -p probe --output-format stream-json --verbose --max-turns 1 2>/dev/null | grep -m1 -E '\"subtype\": ?\"init\"' | grep -q '\"adversarial-reviewer\"' && echo yes || echo no)\"\n")
	case "opencode":
		fmt.Fprintf(&b, "echo \"registered=$(%s >&2 && echo yes || echo no)\"\n", opencodeRegistryProbe)
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

// assertEchoes types into the session while the background seed may still be
// installing and asserts every keystroke echoes promptly through proveo's PTY
// proxy and sbx, then erases what it typed.
func assertEchoes(t *testing.T, sess *tmux.Session, target string, seeding bool) {
	t.Helper()
	p90Max := durationEnv(t, "PROVEO_TEST_ECHO_P90", 250*time.Millisecond)
	echoMax := durationEnv(t, "PROVEO_TEST_ECHO_MAX", time.Second)

	typed, letters := "zqx", "jkvwpbmhgfdtlcy"
	if err := sess.SendText(typed); err != nil {
		t.Fatal(err)
	}
	if !paneShows(sess, typed, 30*time.Second) {
		t.Fatalf("%s: typed %q never echoed", target, typed)
	}
	var lat []time.Duration
	for _, c := range letters {
		typed += string(c)
		start := time.Now()
		if err := sess.SendText(string(c)); err != nil {
			t.Fatal(err)
		}
		if !paneShows(sess, typed, 5*time.Second) {
			t.Fatalf("%s dropped keystroke %q: %q never appeared", target, string(c), typed)
		}
		lat = append(lat, time.Since(start))
		time.Sleep(150 * time.Millisecond)
	}
	_ = sess.SendKeys(slices.Repeat([]string{"BSpace"}, len(typed))...)

	sorted := append([]time.Duration(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p90, worst := sorted[len(sorted)*9/10], sorted[len(sorted)-1]
	t.Logf("%s echo p90=%s max=%s (background seed still installing: %v)", target, p90.Round(time.Millisecond), worst.Round(time.Millisecond), seeding)
	if p90 > p90Max {
		t.Errorf("%s echo latency p90 = %s, want <= %s — keystrokes lag while the seed runs", target, p90.Round(time.Millisecond), p90Max)
	}
	if worst > echoMax {
		t.Errorf("%s echo latency max = %s, want <= %s", target, worst.Round(time.Millisecond), echoMax)
	}
}

// paneShows polls the visible pane every 10ms until it contains s.
func paneShows(sess *tmux.Session, s string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if screen, err := sess.Capture(); err == nil && strings.Contains(screen, s) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
