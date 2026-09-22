// SPEC: _spec/_devops/release-gate.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"regexp"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

var ladderTargets = []string{"cecli", "claudecode", "codex", "cursor", "opencode"}

var (
	ladderSkip = regexp.MustCompile(`(?m)^--- SKIP: TestSandboxLadder [(]`)
	ladderWhy  = regexp.MustCompile(`(?m)^.*sbx not available: (.*)$`)
)

var ladderNote = []string{
	"NOT a release gate pass. A SKIP counts as not-passed: the ladder needs",
	"sbx, which no proveo sandbox has, so this task must run on the HOST.",
	"A red def names the rung, and the rung names the layer that owns it.",
}

func ladderStep(target string) step {
	return step{
		Env:  []string{"PROVEO_LADDER_TEST=1", "PROVEO_LADDER_TARGET=" + target},
		Argv: []string{"go", "test", "-tags", "e2e", "-count=1", "-timeout", "30m", "./e2e/", "-run", "Ladder", "-v"},
	}
}

func ladderPlan() []step {
	out := make([]step, len(ladderTargets))
	for i, t := range ladderTargets {
		out[i] = ladderStep(t)
	}
	return out
}

// ladderVerdict reads one def's climb: a top-level SKIP exits 0 and is not a pass.
func ladderVerdict(target, log string, code int) verdict {
	if ladderSkip.MatchString(log) {
		why := "the ladder did not run"
		if m := ladderWhy.FindStringSubmatch(log); m != nil && m[1] != "" {
			why = m[1]
		}
		return verdict{"SKIP", target + " " + why}
	}
	if code == 0 {
		return verdict{"PASS", target}
	}
	return verdict{"FAIL", target}
}

// runLadder climbs every def, keeps going past a red one, and reports all.
func runLadder(root string) error {
	var vs []verdict
	failed := false
	for _, t := range ladderTargets {
		s := ladderStep(t)
		ui.Section(ui.SectionRun)
		ui.Appf("ladder: %s", t)
		ui.Notef("%s", s)
		log, code := teeRun(root, s)
		v := ladderVerdict(t, log, code)
		failed = failed || v.Kind != "PASS"
		vs = append(vs, v)
	}
	printVerdicts("ladder verdict, all defs", vs, failed, ladderNote)
	if failed {
		return &exitError{code: 1}
	}
	return nil
}

func init() {
	var printOnly bool
	c := &cobra.Command{
		Use:   "ladder",
		Short: "Climb the sbx ladder for every def (host only: needs sbx)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printOnly {
				printPlan(cmd.OutOrStdout(), ladderPlan())
				return nil
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runLadder(root)
		},
	}
	c.Flags().BoolVar(&printOnly, "print", false, "print the go test invocations instead of running them")
	register(c)
}
