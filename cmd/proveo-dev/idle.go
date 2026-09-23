// SPEC: _spec/_devops/release-gate.puml, _spec/internal/sbx/sandbox-backend.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

var idleTargets = []string{"cecli", "claudecode", "cursor", "opencode"}

var idleSkip = regexp.MustCompile(`(?m)^--- SKIP: TestAgentSurvivesIdleAtPrompt [(]`)

var idleNote = []string{
	"NOT a release gate pass. A SKIP counts as not-passed: idle needs",
	"sbx and a live session, which is how a leaked DA1 becomes was-stopped.",
}

func idleStep(idleFor string) step {
	return step{
		Env: []string{
			"PROVEO_IDLE_TEST=1",
			"PROVEO_IDLE_TARGETS=" + strings.Join(idleTargets, ","),
			"PROVEO_IDLE_FOR=" + idleFor,
		},
		Argv: []string{"go", "test", "-tags", "e2e", "-count=1", "-timeout", "30m", "./e2e/", "-run", "TestAgentSurvivesIdleAtPrompt", "-v"},
	}
}

func idleSubtest(result, target string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^    --- ` + result + `: TestAgentSurvivesIdleAtPrompt/` + regexp.QuoteMeta(target))
}

// idleVerdicts reads one sweep's output per def; failed is true unless every def passed.
func idleVerdicts(log string, code int) (vs []verdict, failed bool) {
	if idleSkip.MatchString(log) {
		return []verdict{{"SKIP", "all — the idle suite did not run"}}, true
	}
	for _, t := range idleTargets {
		switch {
		case idleSubtest("PASS", t).MatchString(log):
			vs = append(vs, verdict{"PASS", t})
		case idleSubtest("FAIL", t).MatchString(log):
			vs, failed = append(vs, verdict{"FAIL", t}), true
		case idleSubtest("SKIP", t).MatchString(log):
			vs, failed = append(vs, verdict{"SKIP", t}), true
		default:
			vs, failed = append(vs, verdict{"MISS", t + " — no subtest result"}), true
		}
	}
	if code != 0 && !failed {
		vs, failed = append(vs, verdict{"FAIL", fmt.Sprintf("suite exit %d", code)}), true
	}
	return vs, failed
}

var errNoSbx = errors.New("idle-all needs sbx on PATH (host only; a proveo sandbox has docker and no sbx)")

// runIdle sweeps every def through proveo run idle and prints the table.
func runIdle(root string) error {
	if _, err := exec.LookPath("sbx"); err != nil {
		ui.Failf("%v", errNoSbx)
		return &exitError{code: 1}
	}
	s := idleStep(envOr("PROVEO_IDLE_FOR", "45s"))
	ui.Section(ui.SectionRun)
	ui.Appf("idle: %s", strings.Join(idleTargets, ", "))
	ui.Notef("%s", s)
	log, code := teeRun(root, s)
	vs, failed := idleVerdicts(log, code)
	printVerdicts("idle verdict, all defs", vs, failed, idleNote)
	if failed {
		return &exitError{code: 1}
	}
	return nil
}

func init() {
	var printOnly bool
	c := &cobra.Command{
		Use:   "idle",
		Short: "Idle-survival sweep for every def (host only: needs sbx; catches silent deaths)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printOnly {
				printPlan(cmd.OutOrStdout(), []step{idleStep(envOr("PROVEO_IDLE_FOR", "45s"))})
				return nil
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runIdle(root)
		},
	}
	c.Flags().BoolVar(&printOnly, "print", false, "print the go test invocation instead of running it")
	register(c)
}
