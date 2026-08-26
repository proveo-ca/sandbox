//go:build e2e

// SPEC: _spec/internal/sbx/sandbox-backend.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

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
				return []string{"run", "--name", ladderName(t, 0), sbxAgentFor(t, target), work}
			},
		},
		{
			name: "1-proveo-base-image", adds: "the proveo harness image (-t)",
			argv: func(t *testing.T, work string) []string {
				img := harnessImage(t, target)
				freshTemplate(t, img)
				return []string{"run", "--name", ladderName(t, 1), "-t", img, sbxAgentFor(t, target), work}
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
				return []string{"run", "--name", ladderName(t, 2), "-t", img, sbxAgentFor(t, target), work}
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
				return []string{"run", "--name", ladderName(t, 3), "-t", img, "--kit", kit,
					sbxAgentFor(t, target), work, home}
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

	for _, r := range ladderRungs() {
		t.Run(r.name, func(t *testing.T) {
			work := t.TempDir()
			argv := r.argv(t, work)
			name := argvName(argv)
			t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

			res := holdSbxSession(t, argv, startup, hold)
			switch {
			case res.authFailure != "":
				t.Skipf("rung adds %s — but the session never authenticated (%q). "+
					"Every rung above this one is untestable until it does", r.adds, res.authFailure)
			case res.death != "":
				t.Fatalf("RUNG FAILED — this rung adds %s, and it is the first layer that could not "+
					"hold a session: %q after %s\n%s", r.adds, res.death, res.aliveFor, lastLines(res.out, 25))
			case res.blocked != "":
				t.Fatalf("RUNG BLOCKED — this rung adds %s, and the session stopped at a screen that "+
					"waits for a human (%q). It is alive but cannot proceed unattended.", r.adds, res.blocked)
			case !res.reachedPrompt:
				t.Fatalf("RUNG FAILED — this rung adds %s, and the agent never reached a prompt in %s\n%s",
					r.adds, startup, lastLines(res.out, 25))
			}
			t.Logf("✅ rung holds: adds %s — reached a prompt and stayed alive %s", r.adds, hold)
		})
	}
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
// tests/e2e/sbx_test.go already records — reaches a prompt, then holds with zero
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
