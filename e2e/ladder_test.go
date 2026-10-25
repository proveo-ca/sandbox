//go:build e2e

// SPEC: _spec/_paradigms/capability-ladder.puml, _spec/internal/sbx/sandbox-backend.puml, _spec/_experiments/sbx-kit-capabilities.puml, _spec/internal/sbx/kit-sandbox-credential-gap.puml
package e2e

import (
	"cmp"
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

	"gopkg.in/yaml.v3"

	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/termtext"
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

// stockAgent is rung 0's agent: sbx's own built-in for the target, else its shell agent.
func stockAgent(target string) string { return cmp.Or(sbx.BuiltinAgent(target), "shell") }

// imageRecipe launches image the way proveo would: through the published kit's ENTRYPOINT when
// the harness has one, else through the image's own.
func imageRecipe(target, image string) sbx.RecipeV3 {
	r := sbx.RecipeV3{From: image}
	if up, ok, err := sbx.UpstreamKitFor(target, sbx.UpstreamOptions{}); err == nil && ok {
		r.Entrypoint = up.Launch
	}
	return r
}

// launchProgram is the program the workload's launch resolves, for the probe after a death.
func launchProgram(target string) string {
	if up, ok, err := sbx.UpstreamKitFor(target, sbx.UpstreamOptions{}); err == nil && ok && len(up.Launch) > 0 {
		return up.Launch[0]
	}
	return target + "-entrypoint"
}

func ladderRungs() []rung {
	target := ladderTarget()
	return []rung{
		{
			name: "0-bare-sbx-agent", adds: "nothing — stock sbx agent and stock image",
			argv: func(t *testing.T, work string) []string {
				return append([]string{"run", "--name", ladderName(t, 0)}, append(credentialArgs(t, target), stockAgent(target), work)...)
			},
		},
		{
			name: "1-proveo-image-workload", adds: "the proveo harness image, as a v3 workload with sbx@1 only",
			argv: func(t *testing.T, work string) []string {
				kit := probeWorkload(t, sbx.AgentName(target), imageRecipe(target, harnessImage(t, target)))
				return append([]string{"run", "--name", ladderName(t, 1)}, append(credentialArgs(t, target), kit, work)...)
			},
		},
		{
			name: "2-proveo-browser-image", adds: "the browser variant instead of the base image",
			argv: func(t *testing.T, work string) []string {
				img := harnessImageName(target + "-browser")
				if !imageExists(img) {
					t.Skipf("browser image %s not built", img)
				}
				kit := probeWorkload(t, sbx.AgentName(target), imageRecipe(target, img))
				return append([]string{"run", "--name", ladderName(t, 2)}, append(credentialArgs(t, target), kit, work)...)
			},
		},
		{
			name: "3-proveo-workload", adds: "proveo's whole workload — credentials, allowlist, hooks and the seed, as `proveo run` writes it",
			argv: func(t *testing.T, work string) []string {
				_, create := proveoWorkload(t, work, target, nil)
				return append([]string{"run"}, withName(create[1:], ladderName(t, 3))...)
			},
		},
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

// proveoWorkload asks proveo for the workload it would write, so the rung tests the REAL one
// rather than a hand-copied approximation; it returns the directory and the printed create argv.
func proveoWorkload(t *testing.T, work, target string, extra []string) (string, []string) {
	t.Helper()
	bin := buildProveo(t)
	args := append(childEnvArgsNoCredential(t), extra...)
	out, err := exec.Command("env", append(args, bin, "run", target, "--input", work, "--print")...).CombinedOutput()
	if err != nil {
		t.Skipf("could not render the workload: %v\n%s", err, out)
	}
	create := shellWords(printedLine(t, string(out), "# sandbox"))
	if len(create) == 0 || create[0] != "create" {
		t.Fatalf("the printed sandbox line is not an `sbx create`: %q", create)
	}
	return printedKitDir(t, string(out)), create
}

// withName replaces the --name value in argv.
func withName(argv []string, name string) []string {
	out := append([]string{}, argv...)
	for i, a := range out {
		if a == "--name" && i+1 < len(out) {
			out[i+1] = name
		}
	}
	return out
}

func TestSandboxLadder(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_LADDER_TEST")) {
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
				if probe := probeLaunch(t, name, launchProgram(ladderTarget())); probe != "" {
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
				// First use: the session held a prompt, then refused what was typed.
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
	promptFailure string
	// prompted is what was typed, echoed back into the verdict so a report
	// says which use broke rather than just "something broke".
	prompted string
	aliveFor time.Duration
	out      string
}

// ladderPrompt is what the ladder types into the agent once it reaches a
// prompt.
// Empty keeps today's ladder; the value is typed verbatim, so end it in \r to send it.
func ladderPrompt() string { return env("PROVEO_LADDER_PROMPT", "") }

// providerRefusals are renders that mean the agent is alive at its prompt and
// still cannot reach the provider it was configured for. Every entry names the
// thing that emits it, because a marker list that grows by guess is a liability
// rather than a test.
var providerRefusals = []string{
	// sandboxd's own verdict, in the three wordings the sbx docs give it.
	"Blocked by network policy",
	"Blocked by org policy",
	"Blocked by local rule for",
	// The detail line a default-deny block carries.
	"no matching allow rule",
	// What raise_for_status renders in any Python agent; it means "look", not "blocked".
	"403 Client Error",
	// cecli's provider model-list probe, which is the render this rung was
	// written from.
	"Failed to fetch",
	// The load-bearing one: a key the keyless climb never chose reached the provider.
	"Incorrect API key provided",
}

// firstProviderRefusal returns the refusal that appears EARLIEST IN THE RENDER,
// not the earliest in the list above.
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
		out := termtext.Words(seen())
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
			res.death = firstMarker(termtext.Words(res.out))
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
		res.death = firstMarker(termtext.Words(res.out))
		if res.death == "" {
			res.death = "the session ended on its own"
		}
		if f := firstAuthFailure(termtext.Words(res.out)); f != "" {
			res.authFailure, res.death = f, ""
		}
	}

	// FIRST USE: scan only what rendered after `mark`, where we typed.
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
				since := termtext.Words(out[mark:])
				// AUTH FIRST: a dry account must Skip before the 403 marker fails the rung.
				if f := firstAuthFailure(since); f != "" {
					res.authFailure, res.out = f, out
					res.aliveFor = time.Since(started)
					return res
				}
				if f := firstProviderRefusal(since); f != "" {
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

func TestEveryTargetClimbsFourRungs(t *testing.T) {
	for _, target := range []string{"claudecode", "cecli", "opencode"} {
		t.Setenv("PROVEO_LADDER_TARGET", target)
		rungs := ladderRungs()
		want := []string{"0-bare-sbx-agent", "1-proveo-image-workload", "2-proveo-browser-image", "3-proveo-workload"}
		if len(rungs) != len(want) {
			t.Fatalf("%s: %d rungs, want %d — v3 has no borrowed-shell command rung", target, len(rungs), len(want))
		}
		for i, w := range want {
			if rungs[i].name != w {
				t.Errorf("%s: rung %d is %q, want %q", target, i, rungs[i].name, w)
			}
		}
	}
}

func TestRungZeroIsStockAndTheRestAreWorkloads(t *testing.T) {
	t.Setenv("PROVEO_LADDER_TARGET", "cecli")
	zero := ladderRungs()[0].argv(t, t.TempDir())
	if !contains(zero, "shell") || contains(zero, "-t") || contains(zero, "--") {
		t.Errorf("rung 0 for a harness with no built-in agent must be stock sbx's shell, nothing of proveo's: %v", zero)
	}
	if got := stockAgent("claudecode"); got != "claude" {
		t.Errorf("stockAgent(claudecode) = %q, want sbx's built-in claude", got)
	}
	if r := imageRecipe("claudecode", "proveo/claudecode:local"); len(r.Entrypoint) == 0 || r.Entrypoint[0] != "claude" {
		t.Errorf("claudecode's image rung must launch through the published kit's ENTRYPOINT, got %q", r.Entrypoint)
	}
	if r := imageRecipe("cecli", "proveo/cecli:local"); len(r.Entrypoint) != 0 {
		t.Errorf("cecli has no published kit; its image ENTRYPOINT must run untouched, got %q", r.Entrypoint)
	}
	if got := withName([]string{"--name", "a", "/kit", "/w"}, "b"); got[1] != "b" || got[2] != "/kit" {
		t.Errorf("withName = %v", got)
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

const dummyAPIKey = "sk-ant-proveo-ladder-dummy-do-not-use"

const (
	credOpen  = "PROVEO_CRED_VALUE["
	credClose = "]"
)

// TestSandboxWorkloadProxyManagesTheCredential asserts that the agent holds a sentinel, never the
// key, BECAUSE the workload declares credential@1 — measured with and without the declaration.
func TestSandboxWorkloadProxyManagesTheCredential(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_LADDER_TEST")) {
		t.Skip("set PROVEO_LADDER_TEST=1 — this starts a real sandbox")
	}
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	target := ladderTarget()
	work := t.TempDir()
	kitDir, _ := proveoWorkload(t, work, target, []string{"ANTHROPIC_API_KEY=" + dummyAPIKey})
	raw, err := os.ReadFile(filepath.Join(kitDir, filepath.Base(kitDir)+".yaml"))
	if err != nil {
		t.Fatalf("read rendered workload: %v", err)
	}
	if strings.Contains(string(raw), dummyAPIKey) {
		t.Fatalf("the workload embeds the credential VALUE; it must name the variable only:\n%s", raw)
	}
	var w sbx.WorkloadV3
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatalf("the rendered workload does not round-trip through sbx.WorkloadV3: %v\n%s", err, raw)
	}

	known := sbxKnownServices(t)
	var creds []sbx.CapabilityV3
	var dropped []string
	for _, c := range w.Capabilities {
		if c.Type != sbx.CapCredential {
			continue
		}
		m, _ := c.Config.(map[string]any)
		svc, _ := m["service"].(string)
		if known[svc] {
			creds = append(creds, c)
			continue
		}
		dropped = append(dropped, svc)
	}
	if len(dropped) > 0 {
		t.Logf("not declaring %v: sbx holds no secret for them, and declaring one it does "+
			"not know makes it prompt", dropped)
	}
	if len(creds) == 0 {
		t.Skipf("sbx holds no secret for any service the workload declares (%v) — nothing to "+
			"measure without writing to the host-wide store", dropped)
	}

	got := runCredProbe(t, target, work, creds, "declared")
	switch got {
	case dummyAPIKey:
		t.Fatalf("the agent holds the REAL credential — the declaration did not make the proxy manage it")
	case "<unset>", "":
		t.Fatalf("the variable is %q in the agent: the declaration resolved against no stored "+
			"secret, so the agent cannot authenticate at all", got)
	}
	ctl := runCredProbe(t, target, work, nil, "control")
	if ctl == got {
		t.Fatalf("the SAME value (%q) appears with and WITHOUT credential@1, so the declaration "+
			"is not what produces it — this test would pass with the feature deleted", got)
	}
	t.Logf("✅ proxy-managed BECAUSE we declare it: %q with credential@1, %q without", got, ctl)
}

// runCredProbe launches the harness image with creds and an ENTRYPOINT that prints the key variable.
func runCredProbe(t *testing.T, target, work string, creds []sbx.CapabilityV3, label string) string {
	t.Helper()
	kit := probeWorkload(t, sbx.AgentName(target), sbx.RecipeV3{
		From: harnessImage(t, target),
		Entrypoint: []string{"bash", "-lc",
			`printf '` + credOpen + `%s` + credClose + `\n' "${ANTHROPIC_API_KEY-<unset>}"; sleep 5`},
	}, creds...)
	desc, _ := os.ReadFile(filepath.Join(kit, filepath.Base(kit)+".yaml"))
	name := fmt.Sprintf("proveo-credprobe-%s-%s-%d", target, label, os.Getpid())
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

	// No prompt: this probe's entrypoint is a printf and a sleep, not a TUI.
	res := holdSbxSession(t, []string{"run", "--name", name, kit, work},
		durationEnv(t, "PROVEO_LADDER_STARTUP", 5*time.Minute),
		durationEnv(t, "PROVEO_LADDER_HOLD", 15*time.Second), "")

	// A credential sbx has no stored secret for makes it ASK, and an unattended run stops there.
	if strings.Contains(termtext.Words(res.out), "wants to use these credentials") {
		t.Fatalf("[%s] sbx asked for consent instead of starting: a declared credential has "+
			"no stored secret under its SERVICE name, so the run blocks on a human.\n"+
			"-- workload --\n%s\n-- session --\n%s", label, desc, lastLines(res.out, 20))
	}
	v, seen := credValueFrom(res.out)
	if !seen {
		t.Fatalf("[%s] the probe never printed %q, so nothing was measured — the sandbox may "+
			"have refused the workload or never started. death=%q\n-- workload --\n%s\n-- session --\n%s",
			label, credOpen, res.death, desc, lastLines(res.out, 30))
	}
	return v
}

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

		{name: "an out-of-credits body is an authFailure, not a rung failure",
			out: `{"code":"permission-denied","error":"Your team f82f5523 has either used ` +
				`all available credits or reached its monthly spending limit."}`,
			why: "an empty account is nobody's bug; the authFailures scan claims it first " +
				"and the rung must not also claim it"},
		{name: "cecli's render of that SAME 403 is a rung failure, because requests " +
			"discards the body", want: "Failed to fetch",
			out: "Failed to fetch xai model list: 403 Client Error: Forbidden for url: " +
				"https://api.x.ai/v1/models",
			why: "raise_for_status keeps only the status, so the credit wording never " +
				"reaches the screen — which is exactly why this 403 was unattributable, " +
				"and why the marker has to mean 'look' rather than 'blocked'"},

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
			if got := firstProviderRefusal(termtext.Words(tc.out)); got != tc.want {
				t.Errorf("firstProviderRefusal = %q, want %q — %s", got, tc.want, tc.why)
			}
		})
	}
}

// An out-of-credits provider must reach the SKIP path, not the rung-failure
// path, and it arrives as a 403 that looks like a block. The precedence is
// asserted here because it is invisible at the call site and was measured
// getting this exact case wrong.
func TestCreditExhaustionIsAnAuthFailureNotAnEgressDenial(t *testing.T) {
	t.Parallel()
	for _, out := range []string{
		`{"code":"permission-denied","error":"Your team f82f5523 has either used all ` +
			`available credits or reached its monthly spending limit."}`,
		"API Error: 400 Credit balance is too low",
		`{"error":{"code":"insufficient_quota","message":"You exceeded your quota"}}`,
	} {
		if firstAuthFailure(termtext.Words(out)) == "" {
			t.Errorf("no authFailure matched %q — it would be read as an egress denial, "+
				"which is the misreading this whole spec records", out)
		}
		if got := firstProviderRefusal(termtext.Words(out)); got != "" {
			t.Errorf("firstProviderRefusal also claimed %q as %q; a dry account is nobody's "+
				"bug and must not fail a rung", out, got)
		}
	}
}

// credValueFrom returns the probed value and whether the marker was seen at all.
// The bool is the point: absence must fail the test, not fall through it.
func credValueFrom(out string) (string, bool) {
	flat := termtext.Words(out)
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
