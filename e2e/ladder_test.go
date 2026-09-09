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

// SPEC: _spec/_paradigms/capability-ladder.puml
func shellAgentTarget(target string) bool {
	// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
	return sbx.BuiltinAgent(target) == "" && !sbx.DeclaresOwnAgent(target)
}

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
				home := proveohome.Root(os.Getenv)
				return append([]string{"run", "--name", ladderName(t, 3), "-t", img, "--kit", kit},
					append(credentialArgs(t, target), kitAgentFor(t, target), work, home)...)
			},
		},
	}
}

func freshTemplate(t *testing.T, image string) {
	t.Helper()
	if err := sbx.ReloadTemplate(image, func(f string, a ...any) { t.Logf(f, a...) }); err != nil {
		t.Skipf("could not load %s into the sandbox store: %v", image, err)
	}
}

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

			res := holdSbxSession(t, argv, startup, hold, ladderPrompt())
			switch {
			case res.authFailure != "":
				v.detail = fmt.Sprintf("never authenticated (%q)", res.authFailure)
				t.Skipf("rung adds %s — but the session never authenticated (%q). "+
					"Every rung above this one is untestable until it does", r.adds, res.authFailure)
			case res.death != "":
				v.detail = fmt.Sprintf("died %q after %s", res.death, res.aliveFor.Round(time.Second))
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
			case res.promptFailure != "":
				// The verdict this rung was added for. Everything above it
				// measures whether a session STARTS; this measures whether it
				// can be USED, which is where a Kit that grants reach without a
				// credential first becomes visible.
				// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
				v.detail = fmt.Sprintf("held a prompt, then refused %q on %q",
					res.promptFailure, res.prompted)
				t.Fatalf("RUNG BROKE ON FIRST USE — this rung adds %s. The session reached a prompt "+
					"and stayed alive, so every start-up assertion passes; typing %q then rendered "+
					"%q. A rung that only watched for a prompt would have reported PASS.\n%s",
					r.adds, res.prompted, res.promptFailure, lastLines(res.out, 25))
			}
			used := ""
			if res.prompted != "" {
				used = fmt.Sprintf(", took %q", res.prompted)
			}
			t.Logf("✅ rung holds: adds %s — reached a prompt%s and stayed alive %s",
				r.adds, used, hold)
		})
	}
}

// rungVerdict is one rung's outcome, kept so the ladder can name the owner.
type rungVerdict struct{ name, adds, verdict, detail string }

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

// SPEC: _spec/_paradigms/capability-ladder.puml
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
	// promptFailure is a refusal the render carried AFTER we typed something.
	// It is separate from authFailure because the two have different owners: an
	// auth failure before the prompt makes every rung above it untestable
	// (Skip), while a refusal on first use is a rung that held a session it
	// cannot actually use (Fail).
	promptFailure string
	// prompted is what was typed, echoed back into the verdict so a report
	// says which use broke rather than just "something broke".
	prompted string
	aliveFor time.Duration
	out      string
}

// ladderPrompt is what the ladder types into the agent once it reaches a
// prompt. EMPTY IS THE DEFAULT AND MEANS TODAY'S LADDER: reach a prompt, hold,
// assert nothing about what the agent can do.
//
// It is free text rather than a fixed probe on purpose. `/model xai` is the
// string that reproduces session proveo-1788935973-18915 — a cecli session that
// reached a prompt, held for the full window, and then froze on a 403 from
// api.x.ai the first time its completer touched a provider. The next incident
// will need a different string, and a rung that only knows one probe would have
// to be edited to catch it.
//
// The value is typed VERBATIM, so it does not submit. Completion fires on the
// buffer change, which is where this class of failure lives; append a newline
// (PROVEO_LADDER_PROMPT=$'…\r') when a rung needs the input actually sent.
// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
func ladderPrompt() string { return env("PROVEO_LADDER_PROMPT", "") }

// providerRefusals are renders that mean the agent is alive at its prompt and
// still cannot reach the provider it was configured for. Every entry names the
// thing that emits it, because a marker list that grows by guess is a liability
// rather than a test.
// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
var providerRefusals = []string{
	// sandboxd's own verdict, in the three wordings the sbx docs give it.
	"Blocked by network policy",
	"Blocked by org policy",
	"Blocked by local rule for",
	// The detail line a default-deny block carries.
	"no matching allow rule",
	// What raise_for_status renders in any Python agent — and NOT something a
	// provider says about a credential: api.x.ai answers 401 for missing or bad
	// credentials and 400 for an incorrect key, never 403.
	"403 Client Error",
	// cecli's provider model-list probe, which is the render this rung was
	// written from.
	"Failed to fetch",
	// The load-bearing one: it is the only render that separates "the proxy
	// blocked me" from "the proxy let me through WITHOUT injecting", and those
	// have different owners.
	//
	// It is safe as a rung failure BECAUSE of what a climb is: childEnvArgs-
	// NoCredential unsets every provider.DetectVars() entry, so the agent is
	// meant to hold no key at all. A provider judging a key therefore means one
	// arrived by a path the climb never chose — a workspace .env sourced with
	// `set -a`, or a stale entry in the host-wide secret store — which is
	// exactly the invisible second credential path this rung exists to expose.
	"Incorrect API key provided",
}

// firstProviderRefusal returns the refusal that appears EARLIEST IN THE RENDER,
// not the earliest in the list above. One failure usually prints several of
// these at once — cecli's probe renders "Failed to fetch … 403 Client Error …"
// in one line — and scanning in list order would make the slice's arrangement a
// hidden priority, so reordering it for readability would silently change what
// every report blames. Position in the transcript is the agent's own ordering,
// which is the one an operator reads.
// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
func firstProviderRefusal(out string) string {
	best, at := "", -1
	for _, r := range providerRefusals {
		i := strings.Index(out, r)
		if i < 0 || (at >= 0 && i >= at) {
			continue
		}
		best, at = r, i
	}
	return best
}

func holdSbxSession(t *testing.T, argv []string, startup, hold time.Duration, prompt string) sessionResult {
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

	onExit := func() {
		res.out, res.aliveFor = seen(), time.Since(started)
		res.death = firstMarker(plain(res.out))
		if res.death == "" {
			res.death = "the session ended on its own"
		}
		if f := firstAuthFailure(plain(res.out)); f != "" {
			res.authFailure, res.death = f, ""
		}
	}

	// FIRST USE. `mark` is where the transcript stood before we typed, and the
	// scan below starts there — a host blocked during the SEED is a different
	// fault with a different owner, and attributing it to the prompt would
	// point the report at the wrong rung.
	// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
	mark := len(seen())
	if prompt != "" {
		if _, err := ptmx.WriteString(prompt); err != nil {
			t.Logf("could not type %q into the session: %v", prompt, err)
		} else {
			res.prompted = prompt
			t.Logf("typed %q at the prompt — watching the render for %s", prompt, hold)
		}
	}

	// Poll rather than sleep out the hold: a refusal that arrives at second 3
	// should not wait for second 45, and an empty prompt still just holds
	// because nothing is scanned when nothing was typed.
	deadline = time.Now().Add(hold)
	for {
		select {
		case <-exited:
			onExit()
			return res
		default:
		}
		if res.prompted != "" {
			out := seen()
			if len(out) > mark {
				if f := firstProviderRefusal(plain(out[mark:])); f != "" {
					res.promptFailure, res.out = f, out
					res.aliveFor = time.Since(started)
					return res
				}
			}
		}
		if time.Now().After(deadline) {
			res.aliveFor, res.out = time.Since(started), seen()
			return res
		}
		time.Sleep(2 * time.Second)
	}
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

// SPEC: _spec/_paradigms/capability-ladder.puml
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

	zero := rungs[0].argv(t, t.TempDir())
	if contains(zero, "-t") || contains(zero, "--") {
		t.Errorf("rung 0 is not stock — it names an image or a command: %v", zero)
	}

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

const dummyAPIKey = "sk-ant-proveo-ladder-dummy-do-not-use"

const (
	credOpen  = "PROVEO_CRED_VALUE["
	credClose = "]"
)

// TestSandboxKitProxyManagesTheCredential asserts the property E1 would
// otherwise silently drop.
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
	// No prompt: this probe's entrypoint is a printf and a sleep, not a TUI —
	// there is nothing to type into and nothing that would answer.
	res := holdSbxSession(t, argv,
		durationEnv(t, "PROVEO_LADDER_STARTUP", 5*time.Minute),
		durationEnv(t, "PROVEO_LADDER_HOLD", 15*time.Second), "")

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

// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
func TestLadderPromptIsEmptyUntilAnOperatorNamesOne(t *testing.T) {
	// Not parallel: it sets the variable the default is measured against.
	t.Setenv("PROVEO_LADDER_PROMPT", "")
	if got := ladderPrompt(); got != "" {
		t.Fatalf("ladderPrompt() = %q with nothing set; an empty prompt IS the existing "+
			"ladder, and a default would change every climb", got)
	}
	t.Setenv("PROVEO_LADDER_PROMPT", "/model xai")
	if got := ladderPrompt(); got != "/model xai" {
		t.Errorf("ladderPrompt() = %q, want the string verbatim — a prompt that gets "+
			"rewritten cannot reproduce the incident it names", got)
	}
}

// The marker list is the whole rung, so its boundary is pinned rather than
// trusted: it must fire on a refusal and stay silent on a keyless climb, which
// is the state a climb is deliberately in.
// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
func TestProviderRefusalsFireOnRefusalsAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, out, want, why string
	}{
		{name: "sandboxd default deny", want: "Blocked by network policy",
			out: "Forbidden: Blocked by network policy: domain api.x.ai:443 detail: " +
				"no matching allow rule — blocked by default deny policy",
			why: "the verdict upstream renders when a host is on no allow list"},
		{name: "org policy", want: "Blocked by org policy",
			out: "HTTP 403 Blocked by org policy", why: "centralised deny, details withheld"},
		{name: "local deny rule", want: "Blocked by local rule for",
			out: "Blocked by local rule for api.x.ai", why: "a deny outranks every allow"},
		{name: "cecli's model-list probe", want: "Failed to fetch",
			out: "Failed to fetch xai model list: 403 Client Error: Forbidden for url: " +
				"https://api.x.ai/v1/models",
			why: "the render measured from session proveo-1788935973-18915"},
		{name: "raise_for_status alone", want: "403 Client Error",
			out: "requests.exceptions.HTTPError: 403 Client Error for url: https://api.x.ai/v1/models",
			why: "any Python agent, and 403 is never a provider's answer about a key"},
		{name: "sentinel reached the provider", want: "Incorrect API key provided",
			out: `{"code":"invalid-argument","error":"Incorrect API key provided."}`,
			why: "the proxy let the leg through without injecting"},

		{name: "render order wins, not list order", want: "Blocked by network policy",
			out: "Blocked by network policy: domain api.x.ai:443 … then later a " +
				"403 Client Error from the same leg",
			why: "the slice's arrangement must not be a hidden priority — reordering it " +
				"for readability would change what every report blames"},

		{name: "a clean session says nothing",
			out: "cecli version: 1.4.1 🚀 Launching cecli main model: claude-opus-5",
			why: "a rung that fires on a healthy render is worse than no rung"},
		{name: "no credentials presented is the climb's OWN state",
			out: `401 {"code":"unauthenticated:no-credentials","error":"No credentials presented."}`,
			why: "a climb unsets every provider var on purpose, so this is expected"},
		{name: "a seed-time download is not a first-use refusal",
			out: "npm warn tarball tarball data for foo seems to be corrupted",
			why: "startup faults are scanned from before the prompt, and owned elsewhere"},
		{name: "silence", out: "", why: "nothing typed, nothing rendered, nothing to claim"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := firstProviderRefusal(plain(tc.out)); got != tc.want {
				t.Errorf("firstProviderRefusal = %q, want %q — %s", got, tc.want, tc.why)
			}
		})
	}
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
