// SPEC: _spec/cmd/proveo/usage.puml
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

func helpTree() (*cobra.Command, *cobra.Command) {
	root := &cobra.Command{Use: "proveo", RunE: func(*cobra.Command, []string) error { return nil }}
	root.Flags().Bool("ls", false, "List available harness targets")
	sched := &cobra.Command{Use: "schedule", Short: "Run harnesses unattended", Long: "Jobs live in schedule.yml:\n\n  jobs:\n    lineup:"}
	sched.Flags().String("budget", "45m", "how long a run may last")
	sched.Flags().String("mode", "", "goal|loop (default goal: typed as /goal)")
	sched.Flags().Bool("secret", false, "never shown")
	_ = sched.Flags().MarkHidden("secret")
	attach := &cobra.Command{Use: "attach <job>", Short: "Watch a running job's terminal, which is a long sentence that has to wrap under its column", Run: func(*cobra.Command, []string) {}}
	tick := &cobra.Command{Use: "tick", Short: "hidden", Hidden: true, Run: func(*cobra.Command, []string) {}}
	sched.AddCommand(attach, tick)
	root.AddCommand(sched)
	return root, sched
}

func TestHelpDrawsTheChoicePromptLayout(t *testing.T) {
	t.Parallel()
	_, sched := helpTree()
	var b strings.Builder
	renderHelp(&b, sched)
	out := b.String()
	for _, want := range []string{
		"proveo schedule — Run harnesses unattended\n\n",
		"  usage:    proveo schedule <command>\n",
		"  Jobs live in schedule.yml:\n\n    jobs:\n      lineup:\n",
		"------ commands ------",
		"\n  attach                Watch a running job's terminal",
		"------ flags ------",
		"      --budget string   how long a run may last (default 45m)\n",
		"(default goal: typed as /goal)\n",
		"proveo schedule <command> --help · more on one command",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"tick", "secret", "(default goal: typed as /goal) (default", "S O L U T I O N S"} {
		if strings.Contains(out, gone) {
			t.Errorf("help shows %q:\n%s", gone, out)
		}
	}
	for l := range strings.SplitSeq(out, "\n") {
		if strings.Contains(l, "under its column") && !strings.HasPrefix(l, strings.Repeat(" ", helpDescCol)+"under") {
			t.Errorf("a wrapped description continues under its column: %q", l)
		}
	}
}

func TestHelpRootKeepsTheBannerAndDropsTheTitle(t *testing.T) {
	t.Parallel()
	root, _ := helpTree()
	var b strings.Builder
	renderHelp(&b, root)
	out := b.String()
	if !strings.Contains(out, "S O L U T I O N S") || !strings.Contains(out, ui.BrandTagline) {
		t.Errorf("root help lost the banner:\n%s", out)
	}
	if strings.Contains(out, "proveo —") {
		t.Errorf("the tagline is the root's title; no second one:\n%s", out)
	}
	if !strings.Contains(out, "  schedule              Run harnesses unattended") {
		t.Errorf("root help lists its commands:\n%s", out)
	}
}

func TestHelpPenPaintsMarkersAccentAndContentBody(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	h := helpPen{w: &b, rule: "─", width: 80}
	h.row("--budget string", "how long (default 45m)", helpDescCol)
	out := b.String()
	for _, want := range []string{
		ui.ANSI(ui.ColorAccent) + ui.ANSIBold + "--budget string" + ui.ANSIReset,
		ui.ANSI(ui.ColorSecondary) + "how long" + ui.ANSIReset,
		ui.ANSI(ui.ColorSecondary) + "\033[3m(default 45m)" + ui.ANSIReset,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("row missing %q in %q", want, out)
		}
	}
}

func TestHelpHighlightsAnIndentedYAMLBlock(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	h := helpPen{w: &b, rule: "─", width: 80}
	got := h.yamlLine(`    at: ["mon 18:50", sun]  # weekday + clock`)
	for _, want := range []string{
		ui.ANSI(ui.ColorAccent) + ui.ANSIBold + "at" + ui.ANSIReset,
		ui.ANSI(ui.ColorBrand) + `"mon 18:50"` + ui.ANSIReset,
		ui.ANSI(ui.ColorBrand) + "sun" + ui.ANSIReset,
		ui.ANSI(ui.ColorSecondary) + "\033[3m# weekday + clock" + ui.ANSIReset,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("yaml line missing %q in %q", want, got)
		}
	}
	plain := helpPen{w: &b, plain: true}
	if l := `    at: ["mon 18:50", sun]  # weekday + clock`; plain.yamlLine(l) != l {
		t.Errorf("plain yaml must be the line verbatim: %q", plain.yamlLine(l))
	}
}

func TestAttachHelpNamesTheTaskItTakes(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	renderHelp(&b, scheduleAttachCmd())
	for _, want := range []string{"attach <task>", "key under jobs:", "proveo-sched-<task>",
		"proveo schedule attach sample-task -r", "Ctrl-b d"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("attach help missing %q:\n%s", want, b.String())
		}
	}
}
