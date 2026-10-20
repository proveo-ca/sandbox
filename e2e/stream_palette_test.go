//go:build e2e

// SPEC: _spec/internal/ui/output-vocabulary.puml (GREY DETAIL)

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
	"github.com/proveo-ca/proveo/internal/ui"
)

const volatileImage = "proveo/cecli:volatile-e2e"

// TestStreamPaletteE2E renders a real start + exit stream in tmux.
func TestStreamPaletteE2E(t *testing.T) {
	requireTmux(t)
	requireSbx(t)
	buildVolatileImage(t, harnessImage(t, "cecli"))
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

// TestWhiteRunSkipsSbxsOwnLines pins whiteRun's boundaries on a captured-shape screen, without sbx.
func TestWhiteRunSkipsSbxsOwnLines(t *testing.T) {
	t.Parallel()
	grey := func(s string) string { return ui.ANSI(ui.ColorMuted) + s + ui.ANSIReset }
	rule := func(label string) string {
		return "  " + ui.ANSI(ui.ColorRule) + "──────" + ui.ANSIReset + ui.ANSIBold + " " + label + " " + ui.ANSIReset +
			ui.ANSI(ui.ColorRule) + "──────────" + ui.ANSIReset
	}
	host := func(s string) string { return ui.ANSI(ui.ColorHost) + "● " + ui.ANSIReset + grey(s) }
	sbxOut := []string{
		"── RESOLVE SETUP ─────────",
		"  kit: proveo",
		"⚠ WARNING: template proveo/cecli:volatile-e2e built for the \"shell\" agent",
		"warning: passing a sandbox kit through --kit is deprecated",
		"proveo entrypoint: cecli",
		"agent: booting",
		"agent: fatal",
		"error: agent exited with code 3",
	}
	loud := []string{"  head one", "  head two", "  head three"}
	templateLoad := []string{"Loading template proveo/cecli:volatile-e2e", "Template loaded.", "Ready."}
	prelude := func(extra ...string) []string {
		return append([]string{rule(ui.SectionRun), host("image proveo/cecli:volatile-e2e"), "", rule(ui.SectionStarting),
			host("template loaded")}, extra...)
	}
	results := func(extra ...string) []string {
		lines := append([]string{"", rule(ui.SectionResults), host("last output from the agent:"), grey("  agent: fatal")}, extra...)
		return append(lines, ui.ANSI(ui.ColorFailure)+"× "+ui.ANSIReset+ui.ANSIBold+"the agent exited with code 3"+ui.ANSIReset,
			"PROVEO_EXIT=3")
	}
	afterLoad := append(append(append([]string{}, templateLoad...), "", rule(ui.SectionInterface)), loud...)
	for _, tc := range []struct {
		name  string
		lines [][]string
		want  int
	}{
		{"sbx's lines are not proveo's", [][]string{prelude(), sbxOut, results()}, 0},
		{"a loud prelude still counts", [][]string{prelude(loud...), sbxOut, results()}, 3},
		{"loud results still count", [][]string{prelude(), sbxOut, results(loud...)}, 3},
		{"template-load output is not proveo's", [][]string{prelude(templateLoad...), sbxOut, results()}, 0},
		{"a heading after sbx output reopens proveo's stream", [][]string{prelude(afterLoad...), sbxOut, results()}, 3},
	} {
		var screen []string
		for _, part := range tc.lines {
			screen = append(screen, part...)
		}
		if n := whiteRun(strings.Join(screen, "\n")); n != tc.want {
			t.Errorf("%s: whiteRun = %d, want %d", tc.name, n, tc.want)
		}
	}
}

// buildVolatileImage replaces base's cecli binary with one that prints two lines
// and exits 3; the image entrypoint, seed and kit stay base's own.
func buildVolatileImage(t *testing.T, base string) {
	t.Helper()
	dir := t.TempDir()
	df := "FROM " + base + "\nUSER root\n" +
		"RUN printf '#!/bin/sh\\necho \"agent: booting\"\\necho \"agent: fatal\"\\nexit 3\\n' > /opt/cecli/bin/cecli && chmod 0755 /opt/cecli/bin/cecli\n" +
		"USER cecli\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-q", "-t", volatileImage, dir).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", volatileImage, err, out)
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
	script := fmt.Sprintf("%s run cecli --image %s --credentials forward --input %s --scope . --yes; echo PROVEO_EXIT=$?; sleep 300",
		shellQuote([]string{proveoBin}), volatileImage, shellQuote([]string{work}))
	if err := sess.Start(110, 60, "env", "TERM=xterm-256color", "COLORTERM=truecolor", "PROVEO_WIZARD=off",
		"PROVEO_HOME="+t.TempDir(), "sh", "-c", script); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}
	if _, err := sess.WaitFor("PROVEO_EXIT=", durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute)); err != nil {
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
	for _, l := range proveoLines(screen) {
		if strings.TrimSpace(stripSGR(l)) == "" || strings.Contains(l, "\x1b[") {
			run = 0
			continue
		}
		run++
		best = max(best, run)
	}
	return best
}

// proveoLines keeps the stretches ui.Printer wrote: from the top, and from each of its section headings, to the first line it could not have written.
// SPEC: _spec/internal/ui/output-vocabulary.puml (GREY DETAIL)
func proveoLines(screen string) []string {
	var out []string
	open := true
	for _, l := range strings.Split(screen, "\n") {
		plain := stripSGR(l)
		switch {
		case proveoHeading.MatchString(plain):
			if !open {
				out = append(out, "")
			}
			open = true
		case open && strings.TrimSpace(plain) != "" && !printerLine(plain):
			open = false
		}
		if open {
			out = append(out, l)
		}
	}
	return out
}

var (
	proveoHeading = regexp.MustCompile(`^\s*[─-]{6} [a-z]+ [─-]+\s*$`)
	printerMarks  = []string{"  ", "● ", "✓ ", "⚠ ", "× ", "› "}
)

// printerLine reports whether plain opens with a ui.Printer mark or its text-column indent.
func printerLine(plain string) bool {
	for _, m := range printerMarks {
		if strings.HasPrefix(plain, m) {
			return true
		}
	}
	return false
}
