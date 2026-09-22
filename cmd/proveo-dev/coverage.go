// SPEC: _spec/tests/testing-strategy.puml, _spec/tests/00-testing-overview.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

const coverageUsage = "usage: proveo-dev coverage {unit|merge|coverage|integration|e2e|all} [go test args]"

func init() {
	register(&cobra.Command{
		Use:   "coverage {unit|merge|coverage|integration|e2e|all} [go test args]",
		Short: "Go test lanes with GOCOVERDIR data, and the covdata merge/report",
		Long: `Modes:
  unit         go test -race -cover ./... into $PROVEO_COV_DIR/unit (default)
  merge        go tool covdata merge unit (+ integration) → coverage.out; alias: coverage
  integration  -tags=integration ./internal/egress/ (needs PROVEO_EGRESS_INTEGRATION=1)
  e2e          -tags=e2e ./e2e/ (timeout PROVEO_TEST_GO_TIMEOUT, default 45m)
  all          unit then merge
integration and e2e forward trailing args to go test.`,
		DisableFlagParsing: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return c.Help()
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return coverage(root, covPaths(root, os.Getenv("PROVEO_COV_DIR")), args)
		},
	})
}

type covLayout struct{ cov, unit, merged, profile, integration string }

func covPaths(root, override string) covLayout {
	cov := override
	if cov == "" {
		cov = filepath.Join(root, "cov")
	}
	return covLayout{
		cov:         cov,
		unit:        filepath.Join(cov, "unit"),
		merged:      filepath.Join(cov, "merged"),
		profile:     filepath.Join(cov, "coverage.out"),
		integration: filepath.Join(cov, "integration"),
	}
}

func coverage(root string, l covLayout, args []string) error {
	mode := "unit"
	if len(args) > 0 {
		mode, args = args[0], args[1:]
	}
	switch mode {
	case "unit", "merge", "coverage", "integration", "e2e", "all":
	default:
		ui.Failf("%s", coverageUsage)
		return &exitError{code: 2}
	}
	if _, err := exec.LookPath("go"); err != nil {
		ui.Failf("go toolchain required")
		return &exitError{code: 127}
	}
	switch mode {
	case "unit":
		return covUnit(root, l)
	case "merge", "coverage":
		return covMerge(root, l)
	case "integration":
		if os.Getenv("PROVEO_EGRESS_INTEGRATION") != "1" {
			ui.Failf("set PROVEO_EGRESS_INTEGRATION=1 to run Layer 3")
			return &exitError{code: 1}
		}
		return run(root, nil, "go", integrationArgs(args)...)
	case "e2e":
		return run(root, nil, "go", e2eArgs(os.Getenv("PROVEO_TEST_GO_TIMEOUT"), args)...)
	}
	if err := covUnit(root, l); err != nil {
		return err
	}
	return covMerge(root, l)
}

func unitArgs(race bool, dir string) []string {
	a := []string{"test"}
	if race {
		a = append(a, "-race")
	}
	return append(a, "-cover", "-covermode=atomic", "./...", "-args", "-test.gocoverdir="+dir)
}

func integrationArgs(extra []string) []string {
	return append([]string{"test", "-tags=integration", "-race", "./internal/egress/", "-count=1", "-timeout", "120s"}, extra...)
}

func e2eArgs(timeout string, extra []string) []string {
	if timeout == "" {
		timeout = "45m"
	}
	return append([]string{"test", "-tags=e2e", "./e2e/", "-count=1", "-timeout", timeout}, extra...)
}

func mergeInputs(l covLayout) string {
	if nonEmptyDir(l.integration) {
		return l.unit + "," + l.integration
	}
	return l.unit
}

func covUnit(root string, l covLayout) error {
	if err := os.RemoveAll(l.unit); err != nil {
		return err
	}
	if err := os.MkdirAll(l.unit, 0o755); err != nil {
		return err
	}
	probe := exec.Command("go", "test", "-race", "-cover", "-covermode=atomic", "./internal/runner", "-c", "-o", os.DevNull)
	probe.Dir = root
	race := probe.Run() == nil
	if !race {
		ui.Warnf("-race unavailable; running without race detector")
	}
	if err := run(root, nil, "go", unitArgs(race, l.unit)...); err != nil {
		return err
	}
	ui.Storef("unit coverage data → %s", l.unit)
	return nil
}

func covMerge(root string, l covLayout) error {
	if !nonEmptyDir(l.unit) {
		ui.Failf("no unit coverage in %s — run: proveo-dev coverage unit", l.unit)
		return &exitError{code: 1}
	}
	if err := os.RemoveAll(l.merged); err != nil {
		return err
	}
	if err := os.MkdirAll(l.merged, 0o755); err != nil {
		return err
	}
	steps := [][]string{
		{"tool", "covdata", "merge", "-i=" + mergeInputs(l), "-o=" + l.merged},
		{"tool", "covdata", "percent", "-i=" + l.merged},
		{"tool", "covdata", "textfmt", "-i=" + l.merged, "-o=" + l.profile},
	}
	for _, s := range steps {
		if err := run(root, nil, "go", s...); err != nil {
			return err
		}
	}
	ui.Storef("merged profile → %s", l.profile)
	ui.Notef("html: go tool cover -html=%s", l.profile)
	return nil
}

func nonEmptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}
