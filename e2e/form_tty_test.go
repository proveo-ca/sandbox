//go:build e2e && !windows

// SPEC: _spec/internal/choiceui/tui-stack.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/schedule"
)

// TestTheRunFormDrawsOnTheTerminalWhenStdoutIsRedirected runs `proveo run … >/dev/null`
// on a real terminal: the form must still appear, take keys, and cancel on Esc.
func TestTheRunFormDrawsOnTheTerminalWhenStdoutIsRedirected(t *testing.T) {
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "TRACKED.md"), []byte("# tracked\n"))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := schedule.NewTmux(fmt.Sprintf("proveo-form-tty-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})
	cmd := append([]string{"env"}, childEnvArgsNoCredential(t)...)
	cmd = append(cmd,
		"PROVEO_WIZARD=on",
		"CLAUDE_CODE_OAUTH_TOKEN=fixture-plan-token",
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_STATE_ROOT="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		"sh", "-c", proveoBin+" run "+target+" --input "+work+" > /dev/null; echo PROVEO_EXIT=$?; sleep 300",
	)
	if err := sess.Start(160, 45, cmd...); err != nil {
		t.Fatalf("start the session: %v", err)
	}
	screen, err := sess.WaitFor("enter accept", durationEnv(t, "PROVEO_TEST_TIMEOUT", 2*time.Minute))
	if err != nil {
		t.Fatalf("the form never drew with stdout redirected: %v\n%s", err, tail(screen, 45))
	}
	for _, want := range []string{"confirm or change this run", "esc cancel"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the form lacks %q\n%s", want, tail(screen, 45))
		}
	}
	models, group := "", ""
	switch capacity := sbx.HostAcceleratorCapacity(); {
	case capacity >= 96<<30:
		models, group = "[^\n]*Nemotron[^\n]*Flash Next", "local (96 GB VRAM tier)"
		if runtime.GOOS == "darwin" {
			group = "local (96 GB unified)"
		}
	case capacity >= 24<<30:
		models, group = "[^\n]*(Inkling|Glimmer)[^\n]*Qwen 3.8", "local (needs 24 GB VRAM)"
		if runtime.GOOS == "darwin" {
			group = "local (24 GB unified)"
		}
	}
	modelLayout := regexp.MustCompile(`credentials[^\n]*API keys[^\n]*OAuth[^\n]*\n[^\n]*model[^\n]*API keys[^\n]*plan` +
		models + `[^\n]*\n[^\n]*cloud[^\n]*` + regexp.QuoteMeta(group))
	if !modelLayout.MatchString(screen) {
		t.Errorf("expected the model choices and their group labels below credentials\n%s", tail(screen, 45))
	}
	if !regexp.MustCompile(`\bOS\b[^\n]*host[^\n]*sandbox[^\n]*cloud`).MatchString(screen) {
		t.Errorf("expected the cloud option after sandbox in the OS row\n%s", tail(screen, 45))
	}
	if !regexp.MustCompile(`\bdevices\b[^\n]*android[^\n]*ios\n[^\n]*\bbrowser\b[^\n]*\boff\b[^\n]*\bisolated\b[^\n]*\bnamed\b[^\n]*\bhost\b`).MatchString(screen) {
		t.Errorf("expected mobile devices followed by the four browser choices\n%s", tail(screen, 45))
	}
	if err := sess.SendKeys("End", "Up", "Right", "Right"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("profile name", 10*time.Second)
	if err != nil {
		t.Fatalf("selecting named did not reveal the Browser profile field: %v\n%s", err, tail(screen, 45))
	}
	column := func(row, value string) int {
		t.Helper()
		for line := range strings.SplitSeq(screen, "\n") {
			if strings.Contains(line, row) {
				if at := strings.Index(line, value); at >= 0 {
					return utf8.RuneCountInString(line[:at])
				}
			}
		}
		t.Fatalf("missing %q on %q\n%s", value, row, tail(screen, 45))
		return 0
	}
	if got, want := column("browser", "isolated"), column("devices", "ios"); got != want {
		t.Errorf("isolated at column %d, want column %d with ios\n%s", got, want, tail(screen, 45))
	}
	if err := sess.SendKeys("Escape"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("PROVEO_EXIT=", 30*time.Second)
	if err != nil {
		t.Fatalf("Esc did not end the form: %v\n%s", err, tail(screen, 45))
	}
	if strings.Contains(screen, "enter accept") {
		t.Errorf("the form stayed on the terminal after it ended\n%s", tail(screen, 45))
	}
	if made := newSandboxes(t, before); canList && len(made) > 0 {
		t.Errorf("a cancelled run created %v", made)
	}
}
