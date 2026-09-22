// SPEC: _spec/_devops/release-gate.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// e2eFullEnv is exported for the whole run, so the idle and ladder halves inherit it.
var e2eFullEnv = []string{"PROVEO_IDLE_TEST=1", "PROVEO_SIGNAL_TEST=1", "PROVEO_TOOLCHAIN_TEST=1"}

func e2eStep(timeout string, extra []string) step {
	argv := []string{"go", "test", "-tags=e2e", "./e2e/", "-count=1", "-timeout", timeout}
	return step{Argv: append(argv, extra...)}
}

// splitDevFlags pulls proveo-dev's own flags out of args forwarded to go test.
func splitDevFlags(args []string) (printOnly, help bool, rest []string) {
	for i, a := range args {
		switch a {
		case "--":
			return printOnly, help, append(rest, args[i+1:]...)
		case "--print":
			printOnly = true
		case "-h", "--help":
			help = true
		default:
			rest = append(rest, a)
		}
	}
	return printOnly, help, rest
}

func e2eFullPlan(timeout, idleFor string, extra []string) []step {
	e := e2eStep(timeout, extra)
	e.Env = e2eFullEnv
	return append(append([]step{e}, idleStep(idleFor)), ladderPlan()...)
}

// runE2EFull runs the e2e suite, then idle, then the ladder; every half runs, any red fails.
func runE2EFull(root string, extra []string) error {
	for _, kv := range e2eFullEnv {
		k, v, _ := strings.Cut(kv, "=")
		_ = os.Setenv(k, v)
	}
	timeout := envOr("PROVEO_TEST_GO_TIMEOUT", "150m")
	_ = os.Setenv("PROVEO_TEST_GO_TIMEOUT", timeout)
	s := e2eStep(timeout, extra)
	failed := run(root, nil, s.Argv[0], s.Argv[1:]...) != nil
	failed = runIdle(root) != nil || failed
	failed = runLadder(root) != nil || failed
	if failed {
		return &exitError{code: 1}
	}
	return nil
}

func init() {
	register(&cobra.Command{
		Use:                "e2e-full [--print] [go test args...]",
		Short:              "e2e suite plus tier 4: idle/signal/toolchain gates, per-def idle sweep, then the sbx ladder",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			printOnly, help, extra := splitDevFlags(args)
			if help {
				return cmd.Help()
			}
			if printOnly {
				printPlan(cmd.OutOrStdout(), e2eFullPlan(envOr("PROVEO_TEST_GO_TIMEOUT", "150m"), envOr("PROVEO_IDLE_FOR", "45s"), extra))
				return nil
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runE2EFull(root, extra)
		},
	})
}
