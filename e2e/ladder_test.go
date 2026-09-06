//go:build e2e

// SPEC: _spec/_paradigms/capability-ladder.puml, _spec/internal/sbx/sandbox-backend.puml
package e2e

import (
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
	agent := sbx.BuiltinAgent(target)
	if agent == "" {
		t.Skipf("%s has no sbx agent — it runs on the docker backend only", target)
	}
	return agent
}

func ladderRungs() []rung {
	target := ladderTarget()
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
					append(credentialArgs(t, target), sbxAgentFor(t, target), work, home)...)
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
	t.Helper()
	bin := buildProveo(t)
	out, err := exec.Command("env", append(childEnvArgsNoCredential(t),
		bin, "run", target, "--input", work, "--print")...).CombinedOutput()
	if err != nil {
		t.Skipf("could not render the posture kit: %v\n%s", err, out)
	}
	for _, line := range strings.Split(plain(string(out)), " ") {
		if strings.HasSuffix(line, "/sbx/kit/spec.yaml") {
			return strings.TrimSuffix(line, "/spec.yaml")
		}
	}
	t.Skip("proveo --print named no kit directory")
	return ""
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
