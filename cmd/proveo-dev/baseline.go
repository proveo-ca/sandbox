// SPEC: _spec/_devops/unit-baseline.puml
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

//go:embed unit-baseline.txt
var unitBaseline string

const modulePrefix = "github.com/proveo-ca/proveo/"

// packageFailure marks a package that failed with no failing test of its own.
const packageFailure = "(package)"

func init() {
	register(&cobra.Command{
		Use:   "baseline [go test args...]",
		Short: "Unit gate: go test -race ./..., failing only on a test outside unit-baseline.txt",
		RunE: func(_ *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return baseline(root, args)
		},
	})
}

func baseline(root string, extra []string) error {
	c := exec.Command("go", baselineArgs(raceAvailable(root), extra)...)
	c.Dir = root
	c.Stderr = os.Stderr
	var out bytes.Buffer
	c.Stdout = &out
	_ = c.Run()
	added, fixed := newFailures(failedTests(&out), baselineList(unitBaseline))
	for _, f := range fixed {
		ui.Notef("passes here, listed in the baseline: %s", f)
	}
	if len(added) > 0 {
		ui.Failf("%d failing test(s) outside the baseline:", len(added))
		for _, f := range added {
			ui.Notef("%s", f)
		}
		return &exitError{code: 1}
	}
	ui.Okf("no new failing test (%d in the baseline, %d of them pass here).", len(baselineList(unitBaseline)), len(fixed))
	return nil
}

func baselineArgs(race bool, extra []string) []string {
	a := []string{"test"}
	if race {
		a = append(a, "-race")
	}
	a = append(a, "-count=1", "-json")
	if len(extra) == 0 {
		extra = []string{"./..."}
	}
	return append(a, extra...)
}

func raceAvailable(root string) bool {
	probe := exec.Command("go", "test", "-race", "./internal/runner", "-c", "-o", os.DevNull)
	probe.Dir = root
	return probe.Run() == nil
}

// failedTests lists "package Test" for each failing top-level test in a go test -json stream.
func failedTests(r io.Reader) []string {
	type event struct{ Action, Package, Test string }
	tests := map[string]bool{}
	failedPkg := map[string]bool{}
	withTest := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Action != "fail" || e.Package == "" {
			continue
		}
		pkg := strings.TrimPrefix(e.Package, modulePrefix)
		switch {
		case e.Test == "":
			failedPkg[pkg] = true
		case !strings.Contains(e.Test, "/"):
			tests[pkg+" "+e.Test] = true
			withTest[pkg] = true
		}
	}
	var out []string
	for t := range tests {
		out = append(out, t)
	}
	for pkg := range failedPkg {
		if !withTest[pkg] {
			out = append(out, pkg+" "+packageFailure)
		}
	}
	slices.Sort(out)
	return out
}

func baselineList(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

// newFailures splits got against the baseline: added fails outside it, fixed is listed and passes.
func newFailures(got, base []string) (added, fixed []string) {
	for _, g := range got {
		if !slices.Contains(base, g) {
			added = append(added, g)
		}
	}
	for _, b := range base {
		if !slices.Contains(got, b) {
			fixed = append(fixed, b)
		}
	}
	return added, fixed
}
