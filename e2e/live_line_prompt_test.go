//go:build e2e && !windows

// SPEC: _spec/internal/ui/step-lines.puml
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/tmux"
	"github.com/proveo-ca/proveo/internal/ui"
)

const liveStepFrame = "step 3/9 · pushed branches"

// TestALiveStepLineStaysOffAQuestionAndComesBackAfterIt runs a ticking step line
// and a question on one real terminal: the line must not draw over the question.
func TestALiveStepLineStaysOffAQuestionAndComesBackAfterIt(t *testing.T) {
	requireTmux(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := filepath.Join(t.TempDir(), "done")
	sess := tmux.New(fmt.Sprintf("proveo-live-step-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)
	if err := sess.Start(120, 40, "env", "TERM=xterm-256color", "PROVEO_LIVE_STEP_HELPER="+done,
		binary, "-test.run=^TestLiveStepLineHelper$", "-test.timeout=60s"); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor(liveStepFrame, 10*time.Second); err != nil {
		t.Fatalf("the step's live line never drew: %v\n%s", err, screen)
	}
	if screen, err := sess.WaitFor("the agent pushed these branches", 10*time.Second); err != nil {
		t.Fatalf("the question never drew: %v\n%s", err, screen)
	}
	for i := 0; i < 10; i++ {
		time.Sleep(50 * time.Millisecond)
		if screen, _ := sess.Capture(); strings.Contains(screen, liveStepFrame) {
			t.Fatalf("the step line drew over the question:\n%s", screen)
		}
	}
	if err := sess.SendKeys("Enter"); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor(liveStepFrame, 10*time.Second); err != nil {
		t.Fatalf("the step line did not come back after the question: %v\n%s", err, screen)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(40 * time.Millisecond) {
		if _, err := os.Stat(done); err == nil {
			break
		}
	}
	if screen, _ := sess.Capture(); strings.Contains(screen, liveStepFrame) {
		t.Errorf("a finished step left its line on the terminal:\n%s", screen)
	}
}

func TestLiveStepLineHelper(t *testing.T) {
	done := os.Getenv("PROVEO_LIVE_STEP_HELPER")
	if done == "" {
		t.Skip("tmux helper")
	}
	ui.StepClock = 20 * time.Millisecond
	end := ui.Running(func(ran time.Duration) string {
		return liveStepFrame + " · " + ran.Round(time.Second).String()
	}, liveStepFrame)
	time.Sleep(500 * time.Millisecond)
	choiceui.AskFetchPushed([]string{"origin/feat-live"}, 30*time.Second)
	time.Sleep(800 * time.Millisecond)
	end()
	if err := os.WriteFile(done, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
}
