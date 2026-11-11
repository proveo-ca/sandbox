//go:build e2e && !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml

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

// TestCtrlCDialogOverARealAgentGivesItsScreenBackAndRemovesOnYes opens the close dialog over
// a real agent in a real sandbox: n restores the agent's terminal modes, y removes the sandbox.
func TestCtrlCDialogOverARealAgentGivesItsScreenBackAndRemovesOnYes(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "TRACKED.md"), []byte("# tracked\n"))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-closesbx-%d", os.Getpid()), nil)
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
		proveoBin, "run", target, "--clone", "--input", work,
	)
	if err := sess.Start(160, 45, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}
	k := awaitKit(t, sess, before, target, work)
	awaitQuietScreen(sess, 30*time.Second)

	flags := screenFlags(t, sess)
	t.Logf("the agent's terminal flags: %s", flags)
	open := func() {
		t.Helper()
		if err := sess.SendKeys("C-c"); err != nil {
			t.Fatal(err)
		}
		if screen, err := sess.WaitFor("close this sandbox?", 15*time.Second); err != nil {
			t.Fatalf("Ctrl+C over the agent opened no dialog: %v\n%s", err, tail(screen, 40))
		}
	}
	open()
	if err := sess.SendKeys("n", "Enter"); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(15*time.Second, func() bool {
		screen, _ := sess.Capture()
		return !strings.Contains(screen, "close this sandbox?")
	}) {
		screen, _ := sess.Capture()
		t.Fatalf("the dialog stayed up after n\n%s", tail(screen, 40))
	}
	awaitQuietScreen(sess, 15*time.Second)
	if after := screenFlags(t, sess); after != flags {
		t.Errorf("the agent's terminal flags after the dialog = %q, before it = %q", after, flags)
	}
	if now, ok := sbxSandboxNames(); ok && !now[k.name] {
		t.Fatalf("answering n removed sandbox %s", k.name)
	}

	open()
	if err := sess.SendKeys("y", "Enter"); err != nil {
		t.Fatal(err)
	}
	screen, exited := waitSessionExit(sess, durationEnv(t, "PROVEO_TEST_TEARDOWN", 3*time.Minute))
	if !exited {
		t.Fatalf("proveo did not end after y\n%s", tail(screen, 40))
	}
	if now, ok := sbxSandboxNames(); ok && now[k.name] {
		t.Errorf("answering y left sandbox %s in place\n%s", k.name, tail(screen, 40))
	}
}
