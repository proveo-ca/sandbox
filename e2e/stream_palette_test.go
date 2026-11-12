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

const volatileImage = "proveo/hermes:volatile-e2e"

// TestStreamPaletteE2E renders a real start + exit stream in tmux.
func TestStreamPaletteE2E(t *testing.T) {
	requireTmux(t)
	requireSbx(t)
	buildVolatileImage(t, harnessImage(t, "hermes"))
	proveoBin := buildProveo(t)
	out := os.Getenv("PROVEO_E2E_CAPTURE_DIR")

	screen := runVolatile(t, proveoBin, "")
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
	if lineWith(screen, "error: agent exited with code 3") == "" {
		t.Errorf("sbx's own exit line is missing:\n%s", screen)
	}
	for _, l := range strings.Split(screen, "\n") {
		if plain := stripSGR(l); strings.HasPrefix(plain, "× ") && strings.Contains(plain, "exited with code") {
			t.Errorf("proveo repeated the exit sbx already printed: %q", l)
		}
	}
	if n := whiteRun(screen); n > 2 {
		t.Errorf("run screen prints %d consecutive default-colour proveo lines, want ≤ 2:\n%s", n, screen)
	}
}

// TestNoProveoExitLineWhenOnlyStdinIsRedirected runs the same failing agent with stdout on the
// terminal and stdin from /dev/null, the mode with no pty proxy: sbx's own error line still counts.
func TestNoProveoExitLineWhenOnlyStdinIsRedirected(t *testing.T) {
	requireTmux(t)
	requireSbx(t)
	buildVolatileImage(t, harnessImage(t, "hermes"))
	screen := runVolatile(t, buildProveo(t), " </dev/null")
	if strings.Contains(screen, "PROVEO_EXIT=0") || lineWith(screen, "error: ") == "" {
		t.Fatalf("want a failed run whose own error line sbx printed:\n%s", screen)
	}
	for _, l := range strings.Split(screen, "\n") {
		if plain := stripSGR(l); strings.HasPrefix(plain, "× ") && strings.Contains(plain, "exited with code") {
			t.Errorf("proveo repeated the exit sbx already printed: %q\n%s", l, screen)
		}
	}
	if !strings.Contains(screen, "PROVEO_EXIT=3") {
		t.Errorf("proveo exits with the agent's code sbx named, 3; got %q:\n%s", lineWith(screen, "PROVEO_EXIT="), screen)
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
		"⚠ WARNING: template proveo/hermes:volatile-e2e built for the \"shell\" agent",
		"warning: passing a sandbox kit through --kit is deprecated",
		"proveo entrypoint: hermes",
		"agent: booting",
		"agent: fatal",
		"error: agent exited with code 3",
	}
	wrapped := []string{ui.ANSI(ui.ColorHost) + "● " + ui.ANSIReset + ui.ANSI(ui.ColorMuted) + "Toolbox: `jetbrains://gateway/ssh/env",
		"  ironment?h=x.sbx&launchIde=true&p=22&projectHint=%2Fwork", "  %2Frepo&u=_default_", "  user_`" + ui.ANSIReset}
	sbxCreate := []string{"  KITS", "    ✓ /state/runs/sid/sbx/kit/proveo-claudecode — cached", "  SANDBOX",
		"    Name:       proveo-claudecode-1a2b3c4d", "    Workspace:  /work (rw)", "                /home/.claude (rw)",
		"                /home/toolchains (rw)", "                /state/runs/sid/sbx/home-files (rw)"}
	loud := []string{"  head one", "  head two", "  head three"}
	templateLoad := []string{"Loading template proveo/hermes:volatile-e2e", "Template loaded.", "Ready."}
	store := func(s string) string { return ui.ANSI(ui.ColorDB) + "● " + ui.ANSIReset + grey(s) }
	dim := func(s string) string { return ui.ANSI(ui.ColorRule) + s + ui.ANSIReset }
	ok := func(head, tail string) string {
		return ui.ANSI(ui.ColorSuccess) + "✓ " + ui.ANSIReset + head + grey(tail)
	}
	async := func(s string) string { return ui.ANSI(ui.ColorAsync) + "● " + ui.ANSIReset + grey(s) }
	prelude := func(extra ...string) []string {
		return append([]string{rule(ui.SectionRun), store("log:   ~/.proveo/logs/proveo-1-2.log"), "", rule(ui.SectionSbx),
			async("sbx create")}, extra...)
	}
	facts := []string{"", rule(ui.SectionWorkspace),
		store("checkout: ~/w/hotfix (linked worktree)"), "            " + dim("cloned from ~/w/main at dev"),
		store(".env:     ~/w/main/.env (copy, rw)"), "            " + dim("edits merge back at teardown"),
		store("links:    _spec → ~/Syncd/_spec"), "          " + grey(".idea/ → ~/w/main/.idea/ (20 links)"),
		ok("private clone", " — your checkout is not written"), "    " + dim("the agent's commits come back at teardown")}
	iface := []string{"", rule(ui.SectionInterface), host("port 8083: next in apps/web"),
		"           " + dim("log /tmp/proveo-dev-8083.log"), host("ide:       ssh proveo-claudecode-1a2b3c4d.sbx"),
		async("claudecode 2.1.312 — starting")}
	warn := func(head, tail string) string {
		return ui.ANSI(ui.ColorWarn) + "⚠ " + ui.ANSIReset + head + grey(tail)
	}
	results := func(extra ...string) []string {
		lines := append([]string{"", rule(ui.SectionResults), store("output: nothing under reports/")}, extra...)
		return append(lines, warn("proveo-hermes-1a2b3c4d", " — kept for diagnosis"),
			"    "+dim("`proveo rm --stop proveo-hermes-1a2b3c4d` carries its work home and removes it"),
			"", rule(ui.SectionIDE), host("ssh:       proveo-hermes-1a2b3c4d.sbx"), "           "+dim("`sbx setup ssh` once"),
			host("project:   /work/repo"), "           "+dim("IDE saves write the host checkout directly"),
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
		{"sbx 0.49's indented create blocks are not proveo's", [][]string{prelude(), sbxCreate, results()}, 0},
		{"a wrapped grey line stays grey on its continuations", [][]string{prelude(wrapped...), sbxOut, results()}, 0},
		{"fact blocks, an outcome and dim detail are not white", [][]string{facts, prelude(), sbxCreate, results()}, 0},
		{"the interface block after sbx's create block is proveo's and grey", [][]string{prelude(), sbxCreate, iface, sbxOut, results()}, 0},
		{"a loud line under the interface heading still counts", [][]string{prelude(), sbxCreate, append(iface, loud...), sbxOut, results()}, 3},
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

// buildVolatileImage replaces base's hermes binary with one that prints two lines
// and exits 3; the image entrypoint, seed and kit stay base's own.
func buildVolatileImage(t *testing.T, base string) {
	t.Helper()
	dir := t.TempDir()
	df := "FROM " + base + "\nUSER root\n" +
		"RUN rm -f /opt/hermes/bin/hermes && printf '#!/bin/sh\\necho \"agent: booting\"\\necho \"agent: fatal\"\\nexit 3\\n' > /opt/hermes/bin/hermes && chmod 0755 /opt/hermes/bin/hermes\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-q", "-t", volatileImage, dir).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", volatileImage, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", volatileImage).Run() })
}

// runVolatile runs the volatile agent in tmux, its stdin redirected by redirect ("" keeps the
// terminal), and removes the sandbox the failed run keeps.
func runVolatile(t *testing.T, proveoBin, redirect string) string {
	t.Helper()
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "-c", "user.email=e2e@proveo.test", "-c", "user.name=proveo e2e", "commit", "-q", "--allow-empty", "-m", "seed")

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-palette-%d-%d", os.Getpid(), len(redirect)), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})
	script := fmt.Sprintf("%s run hermes --image %s --credentials forward --input %s --scope . --yes%s; echo PROVEO_EXIT=$?; sleep 300",
		shellQuote([]string{proveoBin}), volatileImage, shellQuote([]string{work}), redirect)
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
	run, best, painted := 0, 0, false
	for _, l := range proveoLines(screen) {
		inherited := painted
		painted = colourOpenAfter(l, painted)
		if strings.TrimSpace(stripSGR(l)) == "" || strings.Contains(l, "\x1b[") || inherited {
			run = 0
			continue
		}
		run++
		best = max(best, run)
	}
	return best
}

// colourOpenAfter reports whether a colour is still set after l; tmux's capture repeats no SGR on a line that continues one.
func colourOpenAfter(l string, open bool) bool {
	if l == "" {
		return false
	}
	for _, m := range sgr.FindAllString(l, -1) {
		open = m != "\x1b[0m" && m != "\x1b[m" && m != "\x1b[39m"
	}
	return open
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
		case sbxHeading.MatchString(plain):
			open = false
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
	sgr           = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	sbxHeading    = regexp.MustCompile(`^\s+[A-Z][A-Z ]+$`) // sbx 0.49's create blocks: "  KITS", "  SANDBOX", "  IMAGE"
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
