// SPEC: _spec/tests/testing-strategy.puml, _spec/_devops/release-gate.puml
// Command e2e runs the image gate, then the e2e suite, idle and ladder passes; mise run e2e calls it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/ui"
)

const usage = "usage: e2e [-print] [-idle] [-ladder] [-toolchain] [-full] [-no-images] [-run=<regex>] [agent...]"

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		ui.Failf("%v", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], os.Getenv, cwd, os.Stdout, os.Stderr, pickAgents))
}

// pickAgents opens a checklist of every agent, all ticked, on a terminal; elsewhere it takes them all.
func pickAgents(all []string) ([]string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return all, nil
	}
	on := make([]bool, len(all))
	for i := range on {
		on[i] = true
	}
	f := &choiceui.Form{Title: "Agents for this run", Rows: []choiceui.Row{{Label: "agents", Options: all, Multi: true, On: on}}}
	ok, err := f.Run()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("cancelled: no e2e pass ran")
	}
	var picked []string
	for i, a := range all {
		if f.Rows[0].On[i] {
			picked = append(picked, a)
		}
	}
	if len(picked) == 0 {
		return nil, errors.New("no agent ticked: no e2e pass ran")
	}
	return picked, nil
}

// run parses args, resolves the agents, then runs the image gate and the passes; it returns the exit code.
func run(args []string, getenv func(string) string, cwd string, stdout, stderr io.Writer, pick func([]string) ([]string, error)) int {
	fs := flag.NewFlagSet("e2e", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	var o maintain.E2EOptions
	printOnly := fs.Bool("print", false, "print the passes instead of running them")
	fs.BoolVar(&o.Idle, "idle", false, "idle-survival pass")
	fs.BoolVar(&o.Ladder, "ladder", false, "sbx ladder, one go test process per agent")
	fs.BoolVar(&o.Toolchain, "toolchain", false, "suite with PROVEO_TOOLCHAIN_TEST on")
	fs.BoolVar(&o.Full, "full", false, "toolchain, idle and ladder, with the suite's idle gate on")
	noImages := fs.Bool("no-images", false, "skip the image gate")
	fs.StringVar(&o.Run, "run", "", "go test -run for the suite pass")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	pr := ui.New(stderr)
	o.Agents, o.Getenv = fs.Args(), getenv
	if err := resolveAgents(&o, *printOnly, pick); err != nil {
		pr.Failf("%v", err)
		return 1
	}
	passes, err := maintain.E2EPlan(o)
	if err != nil {
		pr.Failf("%v", err)
		return 2
	}
	if !*noImages {
		if err := maintain.RunImageGate(*printOnly, stdout); err != nil {
			pr.Failf("%v", err)
			return 1
		}
	}
	if err := maintain.RunE2E(passes, cwd, *printOnly, stdout); err != nil {
		pr.Failf("%v", err)
		return 1
	}
	return 0
}

// resolveAgents asks pick for the agents when a pass narrows by agent, none was named and the run is real.
func resolveAgents(o *maintain.E2EOptions, printOnly bool, pick func([]string) ([]string, error)) error {
	if len(o.Agents) > 0 || printOnly || (!o.Idle && !o.Ladder && !o.Full) {
		return nil
	}
	picked, err := pick(slices.Clone(maintain.E2EAgents))
	if err != nil {
		return err
	}
	o.Agents = picked
	return nil
}
