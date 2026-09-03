//go:build e2e

// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestCloneModeLandsTheCloneAndLiftsTheOutputDir drives `proveo run` itself, not
// `sbx create`, because the failure it guards lived in proveo's mount assembly.
// Two claims: the clone LANDED, and what the agent wrote under reports/ is on
// the host after teardown. SPEC: _spec/internal/sbx/clone-workspace.puml
func TestCloneModeLandsTheCloneAndLiftsTheOutputDir(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode" // the sbx harness that declares an output dir
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "config", "user.email", "e2e@proveo.test")
	mustRun(t, work, "git", "config", "user.name", "proveo e2e")
	if err := os.WriteFile(filepath.Join(work, "TRACKED.md"), []byte("# tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", "-A")
	mustRun(t, work, "git", "commit", "-q", "-m", "init")

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-cloneout-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})

	cmd := []string{"env"}
	if secrets := harnessSecrets(t, target); len(secrets) > 0 {
		cmd = append(cmd, childEnvArgsFor(t, secrets[0])...)
	} else {
		cmd = append(cmd, childEnvArgs(t)...)
	}
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		// Clone is the default on sbx; said explicitly so a changed default cannot
		// turn this into a test of the mounted-checkout path.
		proveoBin, "run", target, "--clone", "--shell", "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}
	w := newWatcher(t, sess)
	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 6*time.Minute)

	w.until("the sbx backend line", 2*time.Minute, func() bool {
		s := w.Screen()
		if strings.Contains(s, "falling back to docker+egress") {
			w.Fatalf("fell back to docker+egress on a host where sbx.Available() said yes")
		}
		return strings.Contains(s, "backend: docker sandboxes (sbx)")
	})
	// proveo must SAY the output dir is not mounted live: a nested positional that
	// slipped through is the whole failure, and it would otherwise be invisible
	// until the clone probe below.
	w.until("the clone output line", 2*time.Minute, func() bool {
		return strings.Contains(w.Screen(), "lifts it back here at teardown")
	})
	w.until("the agent shell prompt", timeout, func() bool { return promptReady(w.Screen()) })

	// Claim 1, asked of the shell where the agent would run: a work tree with the
	// tracked file, an origin (the clone, not the mount), and no stray stub.
	probe := `printf 'CLONE=%s TRACKED=%s ORIGIN=%s\n' ` +
		`"$(git rev-parse --is-inside-work-tree 2>&1)" ` +
		`"$(test -f TRACKED.md && echo yes || echo no)" ` +
		`"$(git remote get-url origin 2>&1)"`
	if err := sess.SendText(probe); err != nil {
		t.Fatalf("send clone probe: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatal(err)
	}
	// The answer is matched by shape, at column 0: the shell echoes the command
	// first, wrapped across lines, and that echo carries the same "CLONE=" text.
	w.until("the clone probe to answer", 90*time.Second, func() bool {
		return cloneProbeAnswer(w.Screen()) != ""
	})
	line := cloneProbeAnswer(w.Screen())
	if !strings.Contains(line, "CLONE=true") || !strings.Contains(line, "TRACKED=yes") {
		w.Fatalf("the clone did not land: %q — the agent's cwd is not the cloned work tree", line)
	}
	if strings.Contains(line, "ORIGIN=fatal") {
		w.Fatalf("no origin in the agent's cwd: %q — this is the mounted checkout, not a clone", line)
	}

	// Claim 2: write a deliverable where the harness writes them, leave, and expect
	// it on the host once teardown has lifted it.
	const mark = "LIFT-OK"
	if err := sess.SendText("mkdir -p reports && printf %s " + mark + " > reports/lift.txt && exit"); err != nil {
		t.Fatalf("send deliverable: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatal(err)
	}
	screen, exited := waitSessionExit(sess, timeout)
	if !exited {
		t.Fatalf("proveo did not exit after the shell left\n%s", screen)
	}
	got := readIn(work, filepath.Join("reports", "lift.txt"))
	if !strings.Contains(got, mark) {
		t.Fatalf("reports/lift.txt did not reach the host after teardown (got %q)\n--- screen ---\n%s", got, screen)
	}
	if !strings.Contains(screen, "deliverables lifted") {
		t.Errorf("teardown did not report the lift — the file arrived, but the operator was not told\n%s", screen)
	}
}

// cloneProbeAnswerRE is the probe's OUTPUT line: it starts the line, where the
// echoed command never does (that one begins with the prompt).
var cloneProbeAnswerRE = regexp.MustCompile(`(?m)^CLONE=\S+ TRACKED=\S+ ORIGIN=\S*`)

func cloneProbeAnswer(screen string) string {
	return strings.TrimSpace(cloneProbeAnswerRE.FindString(screen))
}
