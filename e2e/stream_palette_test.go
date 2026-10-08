//go:build e2e

// SPEC: _spec/internal/ui/output-vocabulary.puml (GREY DETAIL)

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
	"github.com/proveo-ca/proveo/internal/ui"
)

const volatileImage = "proveo/claudecode:volatile-e2e"

// TestStreamPaletteE2E renders a real start + exit stream in tmux.
func TestStreamPaletteE2E(t *testing.T) {
	requireTmux(t)
	requireDocker(t)
	buildVolatileImage(t)
	proveoBin := buildProveo(t)
	out := os.Getenv("PROVEO_E2E_CAPTURE_DIR")

	screen := runVolatile(t, proveoBin)
	if out != "" {
		if err := os.WriteFile(filepath.Join(out, "signal.ansi"), []byte(screen), 0o644); err != nil {
			t.Errorf("write capture: %v", err)
		}
	}
	for _, want := range []string{
		ui.ANSI(ui.ColorRule),
		ui.ANSI(ui.ColorMuted),
		"PROVEO_EXIT=3",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("run screen lacks %q:\n%s", want, screen)
		}
	}
	if l := lineWith(screen, "the agent exited with code 3"); !strings.Contains(l, ui.ANSIBold) {
		t.Errorf("exit line is not bold: %q", l)
	}
	if n := whiteRun(screen); n > 2 {
		t.Errorf("run screen prints %d consecutive default-colour proveo lines, want ≤ 2:\n%s", n, screen)
	}
}

func buildVolatileImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	df := "FROM busybox\nCMD [\"sh\",\"-c\",\"echo 'agent: booting'; echo 'agent: fatal'; exit 3\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-q", "-t", volatileImage, dir).CombinedOutput(); err != nil {
		t.Skipf("build %s: %v\n%s", volatileImage, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", volatileImage).Run() })
}

func runVolatile(t *testing.T, proveoBin string) string {
	t.Helper()
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "-c", "user.email=e2e@proveo.test", "-c", "user.name=proveo e2e", "commit", "-q", "--allow-empty", "-m", "seed")

	sess := tmux.New(fmt.Sprintf("proveo-palette-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)
	script := fmt.Sprintf("%s run claudecode --image %s --egress-mode open --credentials forward --input %s --scope . --yes; echo PROVEO_EXIT=$?; sleep 300",
		proveoBin, volatileImage, work)
	if err := sess.Start(110, 60, "env", "TERM=xterm-256color", "COLORTERM=truecolor", "PROVEO_WIZARD=off",
		"PROVEO_HOME="+t.TempDir(), "sh", "-c", script); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}
	if _, err := sess.WaitFor("PROVEO_EXIT=", 3*time.Minute); err != nil {
		screen, _ := sess.CaptureAll()
		t.Fatalf("run never exited: %v\n%s", err, screen)
	}
	got, err := exec.Command("tmux", "capture-pane", "-e", "-p", "-J", "-S", "-", "-t", sess.Name).Output()
	if err != nil {
		t.Fatalf("capture-pane -e: %v", err)
	}
	return string(got)
}

func lineWith(screen, sub string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(stripSGR(l), sub) {
			return l
		}
	}
	return ""
}

func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// whiteRun counts the longest run of proveo lines whose text carries no SGR.
func whiteRun(screen string) int {
	run, best := 0, 0
	for _, l := range strings.Split(screen, "\n") {
		plain := strings.TrimSpace(stripSGR(l))
		agent := strings.HasPrefix(plain, "agent:") || strings.HasPrefix(plain, "PROVEO_EXIT")
		if plain == "" || agent || strings.Contains(l, "\x1b[") {
			run = 0
			continue
		}
		run++
		best = max(best, run)
	}
	return best
}
