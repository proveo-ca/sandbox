//go:build e2e && !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const postExitRounds = 12

func TestLivePostExitPromptTakesTheFirstKeysOnTmux(t *testing.T) {
	requireTmux(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for r := range postExitRounds {
		done := filepath.Join(t.TempDir(), "done")
		sess := tmux.New(fmt.Sprintf("proveo-live-postexit-%d-%d", os.Getpid(), r), nil)
		t.Cleanup(sess.Kill)
		if err := sess.Start(120, 40, "env", "TERM=xterm-256color", "PROVEO_LIVE_POSTEXIT_HELPER="+done,
			binary, "-test.run=^TestLivePostExitPromptHelper$", "-test.timeout=60s"); err != nil {
			t.Fatal(err)
		}
		if screen, err := sess.WaitFor("the agent pushed these branches", 10*time.Second); err != nil {
			t.Fatalf("round %d: the question never drew: %v\n%s", r, err, screen)
		}
		time.Sleep(150 * time.Millisecond)
		if err := sess.SendKeys("y"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
		if err := sess.SendKeys("Enter"); err != nil {
			t.Fatal(err)
		}
		answer := "still open"
		for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(40 * time.Millisecond) {
			if b, err := os.ReadFile(done); err == nil && len(b) > 0 {
				answer = strings.TrimSpace(string(b))
				break
			}
		}
		if answer != "yes" {
			screen, _ := sess.Capture()
			t.Fatalf("round %d of %d: y then Enter after the agent exited answered %q, want yes\n%s", r+1, postExitRounds, answer, screen)
		}
		sess.Kill()
	}
}

func TestLivePostExitPromptHelper(t *testing.T) {
	done := os.Getenv("PROVEO_LIVE_POSTEXIT_HELPER")
	if done == "" {
		t.Skip("tmux helper")
	}
	p := ptyproxy.New(os.Stdin, os.Stdout)
	if err := p.Run(exec.Command("true")); err != nil {
		t.Fatal(err)
	}
	screen, err := tcell.NewScreen()
	if err != nil {
		t.Fatal(err)
	}
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	yes := choiceui.ConfirmFetchPushed(screen, []string{"origin/feat-live"}, 30*time.Second)
	screen.Fini()
	answer := "no"
	if yes {
		answer = "yes"
	}
	if err := os.WriteFile(done+".tmp", []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(done+".tmp", done); err != nil {
		t.Fatal(err)
	}
}
