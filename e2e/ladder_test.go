//go:build e2e

// SPEC: _spec/_paradigms/capability-ladder.puml, _spec/internal/sbx/sandbox-backend.puml
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/sbx"
	"gopkg.in/yaml.v3"
)

// rung is one step of the ladder: everything the rung below it had, plus exactly
// ONE new thing.
type rung struct {
	name string
	// adds names the single variable this rung introduces, for the report.
	adds string
	// argv is built per-run because image tags and kit dirs are resolved late.
	argv func(t *testing.T, work string) []string
}

// The ladder answers the question three wrong root causes could not: WHICH layer
// owns the failure. Each rung adds one thing, so the first rung that fails names
// its owner outright instead of leaving it to inference.
//
// Rung 0 matters most and is the one nobody had run: stock sbx, stock image, no
// proveo at all. If a subscription cannot hold a session THERE, then nothing
// above it is proveo's bug — and Docker's own tracker says as much, with
// CLAUDE_CODE_OAUTH_TOKEN unsupported (sbx-releases#11) and the forward proxy
// rewriting the Authorization header on api.anthropic.com (sbx-releases#210).
// ladderTarget is the def under test. It is a knob because the sbx fixes this
// suite proved for claudecode were applied to cursor by SYMMETRY, not by
// measurement — and an image contract that has only ever been checked against one
// harness is a contract with one data point.
func ladderTarget() string { return env("PROVEO_LADDER_TARGET", "claudecode") }

// sbxAgentFor maps the def to the sbx agent that runs it, the same mapping
// internal/sbx uses. Rung 0 needs it to name a STOCK agent with no proveo image.
func sbxAgentFor(t *testing.T, target string) string {
	agent, _ := sbx.AgentFor(target)
	if agent == "" {
		t.Skipf("%s resolves to no sbx agent at all", target)
	}
	return agent
}

// shellAgentTarget reports a def sbx has no BUILT-IN agent for. Those run under
// the stock `shell` agent with the def's own launcher as the COMMAND — proveo
// passes `-- cecli` — which means the def's entrypoint does not execute until
// that command is added.
//
// The ladder used to skip these outright ("no sbx agent — docker only"), so the
// one harness whose failure is still unexplained was also the one the
// instrument could not reach. It gets an extra rung instead: the command is a
// thing being added, so it is a rung of its own rather than a passenger on the
// image's. SPEC: _spec/_paradigms/capability-ladder.puml
func shellAgentTarget(target string) bool {
	// With PROVEO_SBX_AGENT_KIT the def declares a COMPLETE AGENT of its own, so
	// it borrows nothing and there is no command rung to insert: the launch
	// arrives with the Kit at rung 3 instead.
	// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
	return sbx.BuiltinAgent(target) == "" && !sbx.DeclaresOwnAgent(target)
}

// kitAgentFor names the agent for a rung that CARRIES the Kit. Only there can a
// def name an agent of its own: the name is declared by the Kit, so a rung
// without one must still ask sbx for a stock agent.
func kitAgentFor(t *testing.T, target string) string {
	t.Helper()
	if sbx.DeclaresOwnAgent(target) {
		return sbx.AgentName(target)
	}
	return sbxAgentFor(t, target)
}

// agentCommand is what proveo appends after `--` for a shell-agent target, and
// nothing for a built-in one, whose agent name already carries its launch.
func agentCommand(target string) []string {
	_, cmd := sbx.AgentFor(target)
	return cmd
}

// launcherProgram is the binary a shell-agent def's launch ends up exec'ing —
// the def's entrypoint when the image ships one, which is what the wrapper
// prefers. It is what the probe inspects and what the report names, neither of
// which wants the `-c` script the wrapper hands sbx.
func launcherProgram(target string) string {
	if !shellAgentTarget(target) {
		return ""
	}
	return target + "-entrypoint"
}

// withCommand appends the `-- <command>` tail sbx expects.
func withCommand(argv []string, cmd []string) []string {
	if len(cmd) == 0 {
		return argv
	}
	return append(append(argv, "--"), cmd...)
}

func ladderRungs() []rung {
	target := ladderTarget()
	rungs := baseRungs(target)
	if !shellAgentTarget(target) {
		return rungs
	}
	// A shell-agent def's entrypoint does not run until the COMMAND is added, so
	// every rung below carries none and one rung introduces it. Without this the
	// image rungs measure a bare shell in our image — real, but not the thing
	// that failed. It is inserted after the base image so the two are never
	// added together.
	cmd := agentCommand(target)
	withCmd := rung{
		name: "2-agent-command", adds: "the def's own launcher as the sbx COMMAND (-- -c 'exec " +
			launcherProgram(target) + "', wrapped so bash -l survives)",
		argv: func(t *testing.T, work string) []string {
			img := harnessImage(t, target)
			freshTemplate(t, img)
			base := append([]string{"run", "--name", ladderName(t, 2), "-t", img},
				append(credentialArgs(t, target), sbxAgentFor(t, target), work)...)
			return withCommand(base, cmd)
		},
	}
	out := append([]rung{}, rungs[:2]...)
	out = append(out, withCmd)
	for i := 2; i < len(rungs); i++ {
		r := rungs[i]
		inner := r.argv
		r.argv = func(t *testing.T, work string) []string { return withCommand(inner(t, work), cmd) }
		r.name = fmt.Sprintf("%d-%s", i+1, strings.SplitN(r.name, "-", 2)[1])
		out = append(out, r)
	}
	return out
}

func baseRungs(target string) []rung {
	return []rung{
		{
			name: "0-bare-sbx-agent", adds: "nothing — stock sbx agent and stock image",
			argv: func(t *testing.T, work string) []string {
				return append([]string{"run", "--name", ladderName(t, 0)}, append(credentialArgs(t, target), sbxAgentFor(t, target), work)...)
			},
		},
		{
			name: "1-proveo-base-image", adds: "the proveo harness image (-t)",
			argv: func(t *testing.T, work string) []string {
				img := harnessImage(t, target)
				freshTemplate(t, img)
				return append([]string{"run", "--name", ladderName(t, 1), "-t", img}, append(credentialArgs(t, target), sbxAgentFor(t, target), work)...)
			},
		},
		{
			name: "2-proveo-browser-image", adds: "the browser variant instead of the base image",
			argv: func(t *testing.T, work string) []string {
				img := harnessImageName(target + "-browser")
				if !imageExists(img) {
					t.Skipf("browser image %s not built", img)
				}
				freshTemplate(t, img)
				return append([]string{"run", "--name", ladderName(t, 2), "-t", img}, append(credentialArgs(t, target), sbxAgentFor(t, target), work)...)
			},
		},
		{
			name: "3-proveo-mixin-and-seed", adds: "proveo's posture Kit — allowlist, env, and the seed (LSPs + toolchain)",
			argv: func(t *testing.T, work string) []string {
				kit := renderPostureKit(t, work, target)
				img := harnessImage(t, target)
				freshTemplate(t, img)
				// The proveo home is a WORKSPACE in a real run, and the mixin points
				// HOME at it. Without the mount, HOME names a path that does not
				// exist, Claude Code finds no config and opens its first-run theme
				// picker — which blocks on a keypress that never comes. That is a
				// faithful reproduction of nothing, so the rung mounts it too.
				home := proveohome.Root(os.Getenv)
				return append([]string{"run", "--name", ladderName(t, 3), "-t", img, "--kit", kit},
					append(credentialArgs(t, target), kitAgentFor(t, target), work, home)...)
			},
		},
	}
}

// freshTemplate hands the CURRENT image to sbx's own store before a rung uses it.
//
// sbx keeps its own copy, so a `docker build` does not reach it: the first climb
// of this ladder reported rung 1 failing against a template SIXTEEN HOURS old,
// which is a test proving something about an artifact nobody was shipping. It
// goes through proveo's production reload rather than comparing ids by hand,
// because `sbx create` re-bakes a template and rewrites the id column — the very
// reason internal/sbx keeps receipts instead of trusting that column.
func freshTemplate(t *testing.T, image string) {
	t.Helper()
	if err := sbx.ReloadTemplate(image, func(f string, a ...any) { t.Logf(f, a...) }); err != nil {
		t.Skipf("could not load %s into the sandbox store: %v", image, err)
	}
}

// credentialArgs forwards the harness's declared secrets the way proveo does.
//
// The rungs drive sbx DIRECTLY, so none of proveo's credential work happens: the
// first cursor climb reported "not logged in" on every rung above 0 while
// CURSOR_API_KEY sat in both the host env and sbx's store, because nothing was
// passing it. `-e NAME` with no value is sbx's take-it-from-the-environment form,
// which is exactly what `proveo run cursor --print` emits for a
// `credentials: [forward]` harness.
//
// Secrets absent from the environment are skipped rather than passed empty: an
// empty value overrides sbx's own stored secret and turns a working credential
// into a broken one.
func credentialArgs(t *testing.T, target string) []string {
	t.Helper()
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range ms {
		if m.Name != target {
			continue
		}
		for _, e := range m.Env {
			if e.Secret && strings.TrimSpace(os.Getenv(e.Name)) != "" {
				out = append(out, "-e", e.Name)
			}
		}
	}
	return out
}

func ladderName(t *testing.T, i int) string {
	return fmt.Sprintf("proveo-ladder-%s-%d-%d", ladderTarget(), i, os.Getpid())
}

// renderPostureKit asks proveo for the Kit it would write, so the rung tests the REAL
// mixin rather than a hand-copied approximation that can drift from it.
func renderPostureKit(t *testing.T, work, target string) string {
	return renderPostureKitEnv(t, work, target, nil)
}

// renderPostureKitEnv renders the Kit with EXTRA environment applied after the
// unsets. `env -u X X=v` sets X: the assignment is processed after the removal,
// which is what lets a caller put a credential back that
// childEnvArgsNoCredential deliberately took away.
func renderPostureKitEnv(t *testing.T, work, target string, extra []string) string {
	t.Helper()
	bin := buildProveo(t)
	args := append(childEnvArgsNoCredential(t), extra...)
	out, err := exec.Command("env", append(args,
		bin, "run", target, "--input", work, "--print")...).CombinedOutput()
	if err != nil {
		t.Skipf("could not render the posture kit: %v\n%s", err, out)
	}
	for _, line := range strings.Split(plain(string(out)), " ") {
		if strings.HasSuffix(line, "/sbx/kit/spec.yaml") {
			assertKitShape(t, line, target)
			return strings.TrimSuffix(line, "/spec.yaml")
		}
	}
	t.Skip("proveo --print named no kit directory")
	return ""
}

// assertKitShape reads the Kit the rung is about to hand sbx and states which
// shape it actually is, because the rung's PASS does not say so on its own.
//
// With PROVEO_SBX_AGENT_KIT the rung names agent proveo-<target>, which only a
// `kind: sandbox` Kit declares. A mixin plus that agent name would be an agent
// sbx cannot resolve — it would drop in seconds rather than hold a prompt, so a
// 45s PASS already implies the sandbox Kit. Implies is not measures: the gate
// travels through `env` into a subprocess, and a green rung that silently
// rendered a MIXIN would be this suite's fourth test measuring its own setup.
// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func assertKitShape(t *testing.T, specPath, target string) {
	t.Helper()
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("rendered kit unreadable: %v", err)
	}
	body := string(b)
	wantKind, wantName := "kind: mixin", ""
	if sbx.DeclaresOwnAgent(target) {
		wantKind, wantName = "kind: sandbox", "name: "+sbx.AgentName(target)
	}
	if !strings.Contains(body, wantKind) {
		t.Fatalf("rendered Kit is not %q — the gate did not reach the renderer, so the "+
			"rung would prove nothing about it:\n%s", wantKind, body)
	}
	if wantName != "" && !strings.Contains(body, wantName) {
		t.Fatalf("sandbox Kit does not declare %q; the agent name IS the selector:\n%s",
			wantName, body)
	}
	t.Logf("kit shape measured: %s%s", wantKind, map[bool]string{true: ", " + wantName}[wantName != ""])
}

func TestSandboxLadder(t *testing.T) {
	if os.Getenv("PROVEO_LADDER_TEST") != "1" {
		t.Skip("set PROVEO_LADDER_TEST=1 to climb the sandbox ladder (starts a real sandbox per rung)")
	}
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	hold := durationEnv(t, "PROVEO_LADDER_HOLD", 45*time.Second)
	startup := durationEnv(t, "PROVEO_LADDER_STARTUP", 5*time.Minute)

	// The ladder's whole promise is that the FIRST failing rung names its owner,
	// and Go's own output does not deliver it: four subtest lines say which rungs
	// failed but not which failure is the one that matters, and every rung above
	// the first inherits its breakage. So the verdict is printed here, once.
	var climbed []rungVerdict
	t.Cleanup(func() { reportLadder(t, climbed) })

	for _, r := range ladderRungs() {
		t.Run(r.name, func(t *testing.T) {
			v := rungVerdict{name: r.name, adds: r.adds}
			t.Cleanup(func() {
				switch {
				case t.Skipped():
					v.verdict = "SKIP"
				case t.Failed():
					v.verdict = "FAIL"
				default:
					v.verdict = "PASS"
				}
				climbed = append(climbed, v)
			})
			work := t.TempDir()
			argv := r.argv(t, work)
			name := argvName(argv)
			t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

			res := holdSbxSession(t, argv, startup, hold)
			switch {
			case res.authFailure != "":
				v.detail = fmt.Sprintf("never authenticated (%q)", res.authFailure)
				t.Skipf("rung adds %s — but the session never authenticated (%q). "+
					"Every rung above this one is untestable until it does", r.adds, res.authFailure)
			case res.death != "":
				v.detail = fmt.Sprintf("died %q after %s", res.death, res.aliveFor.Round(time.Second))
				// The sandbox outlives the session, so ask it why while it is
				// still there. A rung that dies leaves the reader with a symptom
				// and a manual command to run later; by then the sandbox is gone
				// and the next answer is another climb away.
				if probe := probeLaunch(t, name, launcherProgram(ladderTarget())); probe != "" {
					t.Logf("-- why the command could not be exec'd --\n%s", probe)
				}
				t.Fatalf("RUNG FAILED — this rung adds %s, and it is the first layer that could not "+
					"hold a session: %q after %s\n%s", r.adds, res.death, res.aliveFor, lastLines(res.out, 25))
			case res.blocked != "":
				v.detail = fmt.Sprintf("blocked on %q — alive but cannot proceed unattended", res.blocked)
				t.Fatalf("RUNG BLOCKED — this rung adds %s, and the session stopped at a screen that "+
					"waits for a human (%q). It is alive but cannot proceed unattended.", r.adds, res.blocked)
			case !res.reachedPrompt:
				v.detail = fmt.Sprintf("never reached a prompt in %s", startup)
				t.Fatalf("RUNG FAILED — this rung adds %s, and the agent never reached a prompt in %s\n%s",
					r.adds, startup, lastLines(res.out, 25))
			}
			t.Logf("✅ rung holds: adds %s — reached a prompt and stayed alive %s", r.adds, hold)
		})
	}
}

// rungVerdict is one rung's outcome, kept so the ladder can name the owner.
type rungVerdict struct{ name, adds, verdict, detail string }

// reportLadder prints the table and the attribution. Only the FIRST failure is
// an accusation: every rung contains all the rungs below it, so the ones above
// the first break inherit that break and say nothing of their own.
func reportLadder(t *testing.T, climbed []rungVerdict) {
	if len(climbed) == 0 {
		return
	}
	t.Logf("%s", ladderReport(climbed))
}

func ladderReport(climbed []rungVerdict) string {
	first := -1
	for i, v := range climbed {
		if v.verdict == "FAIL" && first < 0 {
			first = i
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n-- ladder verdict: %s --\n", ladderTarget())
	for i, v := range climbed {
		note := v.adds
		if first >= 0 && i > first {
			note = "(inherits rung " + climbed[first].name + " — says nothing of its own)"
		}
		marker := ""
		if i == first {
			marker = "   <-- OWNS THE FAILURE"
		}
		fmt.Fprintf(&b, "  %-6s %-26s %s%s\n", v.verdict, v.name, note, marker)
		if v.detail != "" && (first < 0 || i <= first) {
			fmt.Fprintf(&b, "         %s\n", v.detail)
		}
	}
	switch {
	case first < 0:
		fmt.Fprintf(&b, "\nEvery rung held. Nothing in this stack breaks the session on its own.\n")
	case first == 0:
		fmt.Fprintf(&b, "\nOWNER: upstream. Rung 0 is stock sbx with a stock image and no proveo, "+
			"so this is not proveo's bug and no change here can fix it.\n")
	default:
		fmt.Fprintf(&b, "\nOWNER: %s\n  Rung %s held, so everything below this is exonerated.\n",
			climbed[first].adds, climbed[first-1].name)
	}
	return b.String()
}

// probeLaunch asks a dead rung's sandbox why its command would not exec.
//
// bash falling back to INTERPRETING a script — "line 2: import: command not
// found" for a Python entry point — is what it does when execve returns ENOEXEC,
// and every cause of that is a fact about the file and its interpreter: a
// dangling shebang, an interpreter that is not executable, a wrong path, or a
// uid that cannot traverse the directory. All four are one `ls` apart, and none
// of them survives the sandbox being torn down.
//
// It is best-effort and never fails a rung: a probe that cannot run tells us
// nothing, and turning that into a second failure would bury the first.
// SPEC: _spec/_paradigms/capability-ladder.puml
// probeLaunch inspects the LAUNCHER BINARY, so it takes the program name rather
// than the sbx command — those stopped being the same word once the shell-agent
// launch became a `-c` script, and passing cmd[0] here would have probed "-c".
func probeLaunch(t *testing.T, sandbox string, prog string) string {
	t.Helper()
	if sandbox == "" || prog == "" {
		return "" // a built-in agent: no command of ours to resolve
	}
	script := `set -u
prog=` + quoteWord(prog) + `
echo "id:      $(id)"
echo "PATH:    $PATH"
path="$(command -v "$prog" 2>/dev/null || true)"
echo "resolved: ${path:-<not found on PATH>}"
[ -n "$path" ] || exit 0
echo "file:    $(ls -l "$path" 2>&1)"
shebang="$(head -1 "$path" 2>/dev/null)"
echo "shebang: ${shebang}"
case "$shebang" in
  '#!'*) interp="$(printf '%s' "${shebang#\#!}" | awk '{print $1}')"
         echo "interp:  $(ls -l "$interp" 2>&1)"
         echo "target:  $(readlink -f "$interp" 2>&1)"
         echo "runs:    $("$interp" -V 2>&1 || echo '<cannot execute>')" ;;
  *)     echo "interp:  <no shebang — not a script>" ;;
esac
# The decisive one. Everything above describes the file; this ASKS THE KERNEL.
# If execve succeeds here but the agent session still fell back to interpreting
# the file, then nothing is wrong with it and the fault is in how the session
# invoked it — a different question, and the only one left.
"$path" --version >/dev/null 2>&1 \
  && echo "execve:  OK — the kernel runs it, so the shebang is honoured here" \
  || echo "execve:  FAILED ($?) — the kernel will not run it; that is the ENOEXEC"
# A BOM or stray byte before #! makes the kernel reject a shebang that head(1)
# still prints, and is invisible in every check above.
echo "first4:  $(od -An -c -N4 "$path" 2>/dev/null | tr -s ' ')"
# THE TWO INVOCATIONS, side by side. "sh -c prog" execs and honours the shebang;
# "sh file" hands the file to the shell as a SCRIPT and ignores it entirely. If
# the second reproduces the session's error and the first does not, the fault is
# not in the file — it is in how the launcher invoked it.
echo "as -c:   $(sh -c "$prog --version" 2>&1 | head -1)"
echo "as file: $(sh "$path" --version 2>&1 | head -1)"`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "sbx", "exec", sandbox, "--", "sh", "-c", script)
	c.WaitDelay = 5 * time.Second
	out, err := c.CombinedOutput()
	if len(out) == 0 && err != nil {
		return "" // the sandbox is already gone; say nothing rather than guess
	}
	return strings.TrimRight(string(out), "\n")
}

// quoteWord makes one value safe inside the probe script.
func quoteWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

type sessionResult struct {
	reachedPrompt bool
	blocked       string
	authFailure   string
	death         string
	aliveFor      time.Duration
	out           string
}

// holdSbxSession drives sbx on a REAL pty — tmux cannot host an sbx session, which
// e2e/sbx_test.go already records — reaches a prompt, then holds with zero
// input and reports what happened.
func holdSbxSession(t *testing.T, argv []string, startup, hold time.Duration) sessionResult {
	target := ladderTarget()
	t.Helper()
	cmd := exec.Command("sbx", argv...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	defer func() { _ = ptmx.Close(); _ = cmd.Process.Kill() }()
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 200})

	var mu sync.Mutex
	var buf strings.Builder
	seen := func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	started := time.Now()
	res := sessionResult{}
	deadline := time.Now().Add(startup)
	for !res.reachedPrompt {
		out := plain(seen())
		for _, b := range blockedMarkers {
			if strings.Contains(out, b) {
				res.blocked, res.out = b, seen()
				return res
			}
		}
		for _, f := range authFailures {
			if strings.Contains(out, f) {
				res.authFailure, res.out = f, seen()
				return res
			}
		}
		if reachedPromptFor(seen(), target) {
			res.reachedPrompt = true
			break
		}
		select {
		case <-exited:
			res.out, res.aliveFor = seen(), time.Since(started)
			res.death = firstMarker(plain(res.out))
			return res
		default:
		}
		if time.Now().After(deadline) {
			res.out = seen()
			return res
		}
		time.Sleep(2 * time.Second)
	}

	select {
	case <-exited:
		res.out, res.aliveFor = seen(), time.Since(started)
		res.death = firstMarker(plain(res.out))
		if res.death == "" {
			res.death = "the session ended on its own"
		}
		if f := firstAuthFailure(plain(res.out)); f != "" {
			res.authFailure, res.death = f, ""
		}
	case <-time.After(hold):
		res.aliveFor, res.out = time.Since(started), seen()
	}
	return res
}

func firstMarker(out string) string {
	for _, m := range deathMarkers {
		if strings.Contains(out, m) {
			return m
		}
	}
	return ""
}

func firstAuthFailure(out string) string {
	for _, f := range authFailures {
		if strings.Contains(out, f) {
			return f
		}
	}
	return ""
}

func argvName(argv []string) string {
	for i, a := range argv {
		if a == "--name" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func sbxReadyForTests() (bool, string) {
	if _, err := exec.LookPath("sbx"); err != nil {
		return false, "sbx not on PATH"
	}
	if err := exec.Command("sbx", "ls").Run(); err != nil {
		return false, "sbx daemon not responding"
	}
	return true, ""
}

// The ladder's promise is that the FIRST failing rung names its owner, and Go's
// own output does not deliver it: four subtest lines say WHICH rungs failed but
// not which failure matters, nor why. A real run against opencode printed
//
//	--- PASS: TestSandboxLadder/0-bare-sbx-agent
//	--- FAIL: TestSandboxLadder/1-proveo-base-image
//	--- FAIL: TestSandboxLadder/2-proveo-browser-image
//	--- FAIL: TestSandboxLadder/3-proveo-mixin-and-seed
//
// and left the reader to work out that only rung 1 is an accusation and the two
// above it merely inherit its breakage.
// SPEC: _spec/_paradigms/capability-ladder.puml
func TestLadderReportNamesTheFirstFailure(t *testing.T) {
	t.Parallel()
	climbed := []rungVerdict{
		{name: "0-bare-sbx-agent", adds: "nothing — stock sbx", verdict: "PASS"},
		{name: "1-proveo-base-image", adds: "the proveo harness image (-t)", verdict: "FAIL",
			detail: `died "was stopped" after 9s`},
		{name: "2-proveo-browser-image", adds: "the browser variant", verdict: "FAIL"},
		{name: "3-proveo-mixin-and-seed", adds: "the posture Kit", verdict: "FAIL"},
	}
	got := ladderReport(climbed)

	if !strings.Contains(got, "OWNS THE FAILURE") {
		t.Errorf("the first failure is not marked:\n%s", got)
	}
	if !strings.Contains(got, "OWNER: the proveo harness image (-t)") {
		t.Errorf("the owner is not named:\n%s", got)
	}
	if !strings.Contains(got, `died "was stopped" after 9s`) {
		t.Errorf("the REASON is missing — which is the half a bare PASS/FAIL list already gave:\n%s", got)
	}
	// Rungs above the first failure contain it, so they accuse nothing.
	if !strings.Contains(got, "inherits rung 1-proveo-base-image") {
		t.Errorf("rungs above the first failure are presented as findings of their own:\n%s", got)
	}
	// Rung 0 held, so everything below the first failure is exonerated by name.
	if !strings.Contains(got, "0-bare-sbx-agent held") {
		t.Errorf("the report does not say what was exonerated:\n%s", got)
	}
}

// Rung 0 failing is the one case that is NOT proveo's, and the report has to say
// so outright rather than leave it to the reader.
func TestLadderReportSendsRungZeroUpstream(t *testing.T) {
	t.Parallel()
	got := ladderReport([]rungVerdict{
		{name: "0-bare-sbx-agent", adds: "nothing — stock sbx", verdict: "FAIL", detail: "died"},
	})
	if !strings.Contains(got, "OWNER: upstream") || !strings.Contains(got, "not proveo's bug") {
		t.Errorf("a rung-0 failure must be attributed upstream:\n%s", got)
	}
}

// Every rung holding is a real outcome and must not read as an absent verdict.
func TestLadderReportSaysWhenNothingBroke(t *testing.T) {
	t.Parallel()
	got := ladderReport([]rungVerdict{{name: "0", adds: "x", verdict: "PASS"}})
	if !strings.Contains(got, "Every rung held") {
		t.Errorf("a clean climb reports nothing:\n%s", got)
	}
}

// A def sbx has no built-in agent for runs under the stock `shell` agent with
// its own launcher as the COMMAND — proveo passes `-- cecli`. The ladder used to
// skip those outright, so cecli, whose failure is still unexplained, was the one
// harness the instrument could not reach.
//
// The command is a thing being added, so it earns a rung. Folding it into the
// image rung would add two things at once and forfeit the attribution the whole
// method rests on. SPEC: _spec/_paradigms/capability-ladder.puml
// Opt OUT explicitly: with the kit path defaulted on, cecli declares its own
// agent and this shape stops applying — which is correct, and would otherwise
// make the test skip silently rather than assert the borrowed ladder still works.
func TestLadderGivesShellAgentTargetsTheirOwnCommandRung(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "0")
	if !shellAgentTarget("cecli") {
		t.Skip("cecli gained a built-in sbx agent; this shape no longer applies")
	}
	t.Setenv("PROVEO_LADDER_TARGET", "cecli")
	rungs := ladderRungs()

	if len(rungs) != 5 {
		t.Fatalf("shell-agent ladder has %d rungs, want 5 (the four base rungs plus the command)", len(rungs))
	}
	want := []string{"0-bare-sbx-agent", "1-proveo-base-image", "2-agent-command",
		"3-proveo-browser-image", "4-proveo-mixin-and-seed"}
	for i, w := range want {
		if rungs[i].name != w {
			t.Errorf("rung %d is %q, want %q — the command must come after the image and before the rest",
				i, rungs[i].name, w)
		}
	}

	// Rung 0 must stay stock: no image, and NO command. `cecli` does not exist in
	// the stock image, so passing it there would fail for a reason that is not
	// the one under test. (Rung 0 needs no docker, so it is safe to build here.)
	zero := rungs[0].argv(t, t.TempDir())
	if contains(zero, "-t") || contains(zero, "--") {
		t.Errorf("rung 0 is not stock — it names an image or a command: %v", zero)
	}

	// The command itself, and how it is appended, are pure and testable without
	// a daemon; the image-bearing rungs are exercised by a real climb.
	// This used to assert the command was the bare word "cecli", which is what
	// sbx documents as REPLACING `bash -l` — the ladder was pinning the defect it
	// was built to find. What matters after `--` is that the first word is a
	// flag, so sbx appends it to the login shell instead.
	cmd := agentCommand("cecli")
	if len(cmd) == 0 || !strings.HasPrefix(cmd[0], "-") {
		t.Errorf("agentCommand(cecli) = %v, want a flag-leading command — a bare word makes "+
			"sbx run `bash <launcher>` and read it as a shell script", cmd)
	}
	got := withCommand([]string{"run", "shell", "/w"}, cmd)
	if len(got) != 3+1+len(cmd) || got[3] != "--" {
		t.Errorf("withCommand = %v, want the `-- <command>` tail sbx expects", got)
	}
	if got[4] != cmd[0] {
		t.Errorf("withCommand reordered the command: %v", got)
	}
	if again := withCommand([]string{"run"}, nil); len(again) != 1 {
		t.Errorf("withCommand with no command appended something: %v", again)
	}
}

func TestLadderLeavesBuiltinTargetsAtFourRungs(t *testing.T) {
	t.Setenv("PROVEO_LADDER_TARGET", "opencode")
	rungs := ladderRungs()
	if len(rungs) != 4 {
		t.Fatalf("built-in ladder has %d rungs, want 4", len(rungs))
	}
	if cmd := agentCommand("opencode"); len(cmd) != 0 {
		t.Errorf("opencode is a built-in agent and must carry no command, got %v", cmd)
	}
}

// The probe is best-effort and must never turn a rung's failure into two. A
// sandbox that is already gone, or an sbx that is not on PATH, tells us nothing
// — and reporting that as a second failure would bury the first.
func TestProbeLaunchIsBestEffort(t *testing.T) {
	t.Parallel()
	// A built-in agent supplies no launcher of ours, so there is nothing to resolve.
	if got := probeLaunch(t, "some-sandbox", ""); got != "" {
		t.Errorf("probed with no command to resolve: %q", got)
	}
	if got := probeLaunch(t, "", "cecli"); got != "" {
		t.Errorf("probed with no sandbox named: %q", got)
	}
	// A sandbox that does not exist returns nothing rather than erroring.
	if got := probeLaunch(t, "proveo-ladder-does-not-exist-0-0", "cecli"); got != "" &&
		!strings.Contains(got, "id:") {
		t.Logf("probe on a missing sandbox returned %q — acceptable, it must simply not fail", got)
	}
}

// The script has to survive a command name with a quote in it, because it is
// interpolated into a shell script and the def names it, not us.
func TestQuoteWordSurvivesAQuote(t *testing.T) {
	t.Parallel()
	if got := quoteWord("ce'cli"); got != `'ce'\''cli'` {
		t.Errorf("quoteWord = %s, want the escaped single-quote form", got)
	}
	if got := quoteWord("cecli"); got != "'cecli'" {
		t.Errorf("quoteWord = %s, want 'cecli'", got)
	}
}

// The gate rearranges the ladder rather than adding to it: with an agent of its
// own a def borrows nothing, so the command rung that existed to introduce the
// borrowed launch has nothing left to introduce.
// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestAgentKitGateCollapsesTheCommandRung(t *testing.T) {
	t.Setenv("PROVEO_LADDER_TARGET", "cecli")

	t.Setenv(sbx.EnvAgentKit, "0") // the kit path is the default now; opt OUT to borrow
	borrowed := ladderRungs()
	if len(borrowed) != 5 {
		t.Fatalf("borrowed-shell ladder has %d rungs, want 5", len(borrowed))
	}
	if !shellAgentTarget("cecli") {
		t.Error("without the gate cecli must borrow the shell agent")
	}

	t.Setenv(sbx.EnvAgentKit, "1")
	own := ladderRungs()
	if len(own) != 4 {
		t.Fatalf("own-agent ladder has %d rungs, want 4 — the command rung should be gone", len(own))
	}
	for _, r := range own {
		if strings.Contains(r.name, "agent-command") {
			t.Errorf("rung %q survived: with its own agent there is no borrowed command", r.name)
		}
	}
	if shellAgentTarget("cecli") {
		t.Error("with the gate cecli declares its own agent and borrows nothing")
	}
}

// Only a rung carrying the Kit may name an agent of ours: the Kit is what
// declares the name, so a rung without one must ask sbx for a stock agent or
// sbx has nothing to resolve.
func TestOnlyTheKitRungNamesOurOwnAgent(t *testing.T) {
	t.Setenv("PROVEO_LADDER_TARGET", "cecli")
	t.Setenv(sbx.EnvAgentKit, "1")
	if got := kitAgentFor(t, "cecli"); got != sbx.AgentName("cecli") {
		t.Errorf("kit rung agent = %q, want %q", got, sbx.AgentName("cecli"))
	}
	if got, _ := sbx.AgentFor("cecli"); got != sbx.ShellAgent {
		t.Errorf("rungs below the Kit must still use a stock agent, got %q", got)
	}
}

// dummyAPIKey is deliberately not a working credential. proxyManaged means the
// agent never receives the value, so the sentinel arrives whether or not the key
// is real — which is exactly what makes this assertion free to run.
const dummyAPIKey = "sk-ant-proveo-ladder-dummy-do-not-use"

// credOpen/credClose DELIMIT the probed value. Session output reaches the test
// through plain(), which collapses every run of whitespace — newlines included —
// into single spaces, so there are no lines left to parse and an unterminated
// marker would swallow whatever the session printed next. Delimiters also let an
// EMPTY value be distinguished from a missing one.
//
// The test asserts the marker was seen before it looks at any value. The first
// version of this test read the variable with `sbx exec` AFTER the session had
// ended, got sbx's "ERROR: no sandbox named …" back as if it were the value, and
// PASSED — because its switch named three failures and treated everything else
// as success. A probe that cannot run must fail, never pass.
const (
	credOpen  = "PROVEO_CRED_VALUE["
	credClose = "]"
)

// TestSandboxKitProxyManagesTheCredential asserts the property E1 would
// otherwise silently drop.
//
// A def declaring its OWN agent must declare its own credentials, or it loses
// what every built-in-backed def gets free: sbx sets the variable to a sentinel
// and injects the real value host-side per request. Without it the variable is
// UNSET — measured by this test's own control pass — so the agent cannot
// authenticate at all.
//
// The dummyAPIKey branch below stays even though no run has produced it. It
// guards the WORSE outcome: a leak would be silent where an unset variable is
// loud, and an assertion that only covers what has been seen is how a suite
// stops noticing.
//
// The ladder cannot catch that. childEnvArgsNoCredential unsets every
// provider.DetectVars() entry so a climb never spends a key, so nothing is
// detected and the Kit renders with zero credentials: every rung passes while
// proving nothing. Measured: `credentials in kit: 0`.
//
// So this puts ONE credential back — a dummy — and reads the variable IN THE
// AGENT PROCESS, which is the only place it means anything. The Kit under test
// is the one proveo renders; only the entrypoint is swapped, so the credentials
// block is the real one rather than a hand-copy that can drift.
// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestSandboxKitProxyManagesTheCredential(t *testing.T) {
	if os.Getenv("PROVEO_LADDER_TEST") != "1" {
		t.Skip("set PROVEO_LADDER_TEST=1 — this starts a real sandbox")
	}
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	target := ladderTarget()
	if !sbx.DeclaresOwnAgent(target) {
		t.Skipf("%s does not declare its own agent (set %s=1); a mixin MUST NOT declare "+
			"credentials — sbx refuses a service the built-in agent already declares",
			target, sbx.EnvAgentKit)
	}

	work := t.TempDir()
	kitDir := renderPostureKitEnv(t, work, target, []string{"ANTHROPIC_API_KEY=" + dummyAPIKey})
	raw, err := os.ReadFile(filepath.Join(kitDir, "spec.yaml"))
	if err != nil {
		t.Fatalf("read rendered kit: %v", err)
	}
	var kit sbx.Kit
	if err := yaml.Unmarshal(raw, &kit); err != nil {
		t.Fatalf("the rendered Kit does not round-trip through sbx.Kit: %v\n%s", err, raw)
	}
	if len(kit.Credentials) == 0 {
		t.Fatalf("rendered Kit declares no credentials even with a key present — the agent "+
			"would hold the real value:\n%s", raw)
	}
	var proxied bool
	for _, c := range kit.Credentials {
		if c.APIKey != nil && c.APIKey.ProxyManaged {
			proxied = true
		}
	}
	if !proxied {
		t.Fatalf("credentials declared without proxyManaged, which is the entire point:\n%s", raw)
	}
	if strings.Contains(string(raw), dummyAPIKey) {
		t.Fatalf("the Kit embeds the credential VALUE; it must name the variable only:\n%s", raw)
	}

	// Keep only services sbx already holds a secret for. Declaring one it does
	// not know makes sbx ask a human for consent, and the run blocks — measured.
	// The alternative, storing the secret from here, is not available: `sbx
	// secret set` is HOST-WIDE and outlives the run, so a test doing it would
	// overwrite the operator's real credential with a dummy.
	known := sbxKnownServices(t)
	kept := kit.Credentials[:0]
	var dropped []string
	for _, c := range kit.Credentials {
		if known[c.Service] {
			kept = append(kept, c)
			continue
		}
		dropped = append(dropped, c.Service)
	}
	kit.Credentials = kept
	if len(dropped) > 0 {
		t.Logf("not declaring %v: sbx holds no secret for them, and declaring one it does "+
			"not know makes it prompt", dropped)
	}
	if len(kit.Credentials) == 0 {
		t.Skipf("sbx holds no secret for any service proveo declares (%v) — nothing to "+
			"measure without writing to the host-wide store", dropped)
	}

	// Swap ONLY the entrypoint, so the value is observable in the agent process.
	// The seed goes with it: proveo-seed expects the harness, not a bare shell.
	kit.Sandbox.Entrypoint = []string{"bash", "-lc",
		`printf '` + credOpen + `%s` + credClose + `\n' "${ANTHROPIC_API_KEY-<unset>}"; sleep 5`}
	kit.Sandbox.Command = nil
	kit.Setup = nil
	got := runCredProbe(t, target, work, kit, "declared")
	switch got {
	case dummyAPIKey:
		t.Fatalf("the agent holds the REAL credential — proxyManaged did not take effect, and " +
			"this def is worse off declaring its own agent than borrowing one")
	case "<unset>", "":
		t.Fatalf("the variable is %q in the agent: the declaration resolved against no stored "+
			"secret, so the agent cannot authenticate at all", got)
	}

	// THE CONTROL. "A sentinel appeared" is not the claim; "our declaration
	// CAUSED it" is. This suite has produced five green results that measured
	// something other than their subject, so run the identical Kit with the
	// credentials block removed. If the value is unchanged it came from
	// somewhere else, and the assertion above proves nothing.
	bare := kit
	bare.Credentials = nil
	ctl := runCredProbe(t, target, work, bare, "control")
	if ctl == got {
		t.Fatalf("the SAME value (%q) appears with and WITHOUT the credentials block, so the "+
			"declaration is not what produces it — this test would pass with the feature "+
			"deleted", got)
	}
	t.Logf("✅ proxy-managed BECAUSE we declare it: %q with the block, %q without", got, ctl)
}

// runCredProbe writes the Kit, starts a sandbox, and returns what the agent
// process actually held. Every way of failing to MEASURE is fatal — a probe
// that cannot run must never report a value.
func runCredProbe(t *testing.T, target, work string, kit sbx.Kit, label string) string {
	t.Helper()
	out, err := yaml.Marshal(kit)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), out, 0o600); err != nil {
		t.Fatal(err)
	}

	img := harnessImage(t, target)
	freshTemplate(t, img)
	name := fmt.Sprintf("proveo-credprobe-%s-%s-%d", target, label, os.Getpid())
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

	argv := append([]string{"run", "--name", name, "-t", img, "--kit", dir},
		kitAgentFor(t, target), work)
	res := holdSbxSession(t, argv,
		durationEnv(t, "PROVEO_LADDER_STARTUP", 5*time.Minute),
		durationEnv(t, "PROVEO_LADDER_HOLD", 15*time.Second))

	// A credential sbx has no stored secret for makes it ASK, and an unattended
	// run stops there — a distinct fault from a refused block or a dead sandbox.
	if strings.Contains(plain(res.out), "wants to use these credentials") {
		t.Fatalf("[%s] sbx asked for consent instead of starting: a declared credential has "+
			"no stored secret under its SERVICE name, so the run blocks on a human.\n"+
			"-- Kit --\n%s\n-- session --\n%s", label, out, lastLines(res.out, 20))
	}
	v, seen := credValueFrom(res.out)
	if !seen {
		t.Fatalf("[%s] the probe never printed %q, so nothing was measured — the sandbox may "+
			"have refused the Kit or never started. death=%q\n-- Kit --\n%s\n-- session --\n%s",
			label, credOpen, res.death, out, lastLines(res.out, 30))
	}
	return v
}

// credValueFrom returns the probed value and whether the marker was seen at all.
// The bool is the point: absence must fail the test, not fall through it.
func credValueFrom(out string) (string, bool) {
	flat := plain(out)
	i := strings.Index(flat, credOpen)
	if i < 0 {
		return "", false
	}
	rest := flat[i+len(credOpen):]
	j := strings.Index(rest, credClose)
	if j < 0 {
		return "", false // truncated output: a half-seen marker proves nothing
	}
	return strings.TrimSpace(rest[:j]), true
}

// credValueFrom's bool is the whole safety property, so it is pinned here.
//
// The first version of the credential test had no such bool: it read the
// variable with `sbx exec` after the session had ended, sbx answered "ERROR: no
// sandbox named …", and the switch — which named three failures and treated
// everything else as success — PASSED on that error text. A probe that cannot
// run must fail, never pass.
func TestCredValueFromFailsWhenTheMarkerIsAbsent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, out, want string
		wantSeen        bool
	}{
		{name: "sentinel", out: "noise\n" + credOpen + "proxy-managed" + credClose + "\nmore", want: "proxy-managed", wantSeen: true},
		{name: "unset", out: credOpen + "<unset>" + credClose, want: "<unset>", wantSeen: true},
		{name: "empty value is SEEN and empty, not missing", out: credOpen + credClose, want: "", wantSeen: true},
		{name: "unterminated marker proves nothing", out: "x " + credOpen + "proxy-managed"},
		{name: "sbx error text is NOT a value", out: "ERROR: no sandbox named 'x'\n\nTo create one:\n  sbx create AGENT WORKSPACE"},
		{name: "silence", out: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, seen := credValueFrom(tc.out)
			if seen != tc.wantSeen {
				t.Fatalf("seen = %v, want %v — an unseen marker must fail the test, not pass it", seen, tc.wantSeen)
			}
			if seen && got != tc.want {
				t.Errorf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

// sbxKnownServices lists the service names sbx already holds a secret for, so a
// probe can declare only those. Reading the store is safe; writing to it is not
// — `sbx secret set` is host-wide and outlives the run.
func sbxKnownServices(t *testing.T) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sbx", "secret", "ls").CombinedOutput()
	if err != nil {
		t.Skipf("cannot read the sbx secret store: %v\n%s", err, out)
	}
	known := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// SCOPE TYPE NAME SECRET — the name is the third column.
		if len(f) >= 3 && f[1] == "service" {
			known[f[2]] = true
		}
	}
	return known
}

// sbxKnownServices parses `sbx secret ls`, whose shape is a real dependency and
// not obvious from the call site. Both naming schemes appear in it side by side:
// sbx's own service names (anthropic) and the ENV-VAR-named entries proveo has
// always written (ANTHROPIC_API_KEY). A kit's credentials[].service resolves
// against the former.
func TestSbxSecretLsParsesServiceNames(t *testing.T) {
	t.Parallel()
	const sample = `SCOPE      TYPE      NAME                      SECRET
(global)   service   ANTHROPIC_API_KEY         (stored)
(global)   service   anthropic                 (oauth configured)
(global)   service   github                    (stored)
(global)   env       SOME_ENV                  (stored)
`
	known := map[string]bool{}
	for _, line := range strings.Split(sample, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[1] == "service" {
			known[f[2]] = true
		}
	}
	for _, want := range []string{"anthropic", "ANTHROPIC_API_KEY", "github"} {
		if !known[want] {
			t.Errorf("did not parse %q out of the store listing", want)
		}
	}
	if known["SOME_ENV"] {
		t.Error("parsed a non-service row as a service")
	}
	if known["NAME"] {
		t.Error("parsed the header row as a service")
	}
}
