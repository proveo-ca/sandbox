// SPEC: _spec/internal/maintain/image-build-deploy.puml, _spec/_devops/image-lineage-and-publish.puml
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

func loadMaintainRegistry() ([]maintain.Target, error) {
	defsDir, err := maintainerDefsDir()
	if err != nil {
		return nil, err
	}
	ms, err := manifest.Load(defsDir)
	if err != nil {
		return nil, fmt.Errorf("loading manifests from %s: %w", defsDir, err)
	}
	return maintain.Registry(ms, defsDir), nil
}

func targetNames(reg []maintain.Target) string {
	ns := make([]string, len(reg))
	for i, t := range reg {
		ns[i] = t.Name
	}
	return strings.Join(ns, ", ")
}

func selectTargets(reg []maintain.Target, arg, verb string) ([]maintain.Target, error) {
	arg = strings.TrimSpace(arg)
	if arg == "all" {
		return reg, nil
	}
	if arg != "" {
		for _, t := range reg {
			if t.Name == arg {
				return []maintain.Target{t}, nil
			}
		}
		return nil, fmt.Errorf("unknown target %q — use 'all' or one of: %s", arg, targetNames(reg))
	}
	if agentio.IsStdinTTY() {
		return pickTargets(reg, verb, os.Stdin, os.Stderr)
	}
	return nil, fmt.Errorf("no target given; pass a target name or 'all' (targets: %s)", targetNames(reg))
}

func pickTargets(reg []maintain.Target, verb string, in io.Reader, out io.Writer) ([]maintain.Target, error) {
	if agentio.IsReaderTTY(in) {
		return fuzzyPickTargets(reg, verb)
	}
	return pickTargetsNumbered(reg, verb, in, out)
}

// fuzzyPickTargets shows an interactive finder with "all" as entry 0.
func fuzzyPickTargets(reg []maintain.Target, verb string) ([]maintain.Target, error) {
	labels := make([]string, 0, len(reg)+1)
	labels = append(labels, "all")
	for _, t := range reg {
		labels = append(labels, t.Name)
	}
	idxs, err := fuzzyfinder.FindMulti(labels, func(i int) string { return labels[i] },
		fuzzyfinder.WithPromptString(verb+" [tab=multi]> "))
	if errors.Is(err, fuzzyfinder.ErrAbort) {
		return nil, fmt.Errorf("no target selected")
	}
	if err != nil {
		return nil, err
	}
	sort.Ints(idxs)
	out := make([]maintain.Target, 0, len(idxs))
	for _, i := range idxs {
		if i == 0 { // "all" selected (alone or alongside others) → every target
			return reg, nil
		}
		out = append(out, reg[i-1])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no target selected")
	}
	return out, nil
}

// pickTargetsNumbered prints a numbered menu (0 = all) and returns the choice.
func pickTargetsNumbered(reg []maintain.Target, verb string, in io.Reader, out io.Writer) ([]maintain.Target, error) {
	fmt.Fprintf(out, "Select a target to %s:\n", verb)
	fmt.Fprintln(out, "   0) all")
	for i, t := range reg {
		fmt.Fprintf(out, "  %2d) %s\n", i+1, t.Name)
	}
	fmt.Fprint(out, "target [0]: ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" || line == "0" {
		return reg, nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(reg) {
		return nil, fmt.Errorf("invalid selection %q", line)
	}
	return []maintain.Target{reg[n-1]}, nil
}

// runPlan executes (or prints, when printOnly) each command in order, streaming
// stdio, stopping at the first failure.
func runPlan(cmds []maintain.Command, printOnly bool) error {
	for _, c := range cmds {
		if printOnly {
			prefix := ""
			if c.Dir != "" {
				prefix = "(cd " + c.Dir + ") "
			}
			fmt.Printf("%s%s\n", prefix, strings.Join(c.Argv, " "))
			continue
		}
		ex := exec.Command(c.Argv[0], c.Argv[1:]...)
		ex.Dir = c.Dir
		ex.Stdin, ex.Stdout, ex.Stderr = os.Stdin, os.Stdout, os.Stderr
		if c.Quiet {
			ex.Stdout = io.Discard
		}
		if err := ex.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(c.Argv, " "), err)
		}
	}
	return nil
}

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func buildCmd() *cobra.Command {
	var tag string
	var noCache, printOnly bool
	cmd := &cobra.Command{
		Use:    "build [target|all]",
		Short:  "Build harness/sidecar image(s) (maintainer)",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			reg, err := loadMaintainRegistry()
			if err != nil {
				return err
			}
			ts, err := selectTargets(reg, firstArg(args), "build")
			if err != nil {
				return err
			}
			for _, t := range ts {
				ui.Iconf("🔨", "building %s (%s:%s)", t.Name, t.Image, tag)
				if err := runPlan(t.BuildPlan(tag, noCache), printOnly); err != nil {
					return fmt.Errorf("build %s: %w", t.Name, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tag, "tag", maintain.LocalTag,
		"image tag to build/verify (local builds are :local; :latest means published and is refused for --load)")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "pass --no-cache to docker build")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the plan instead of running it")
	return cmd
}

func deployCmd() *cobra.Command {
	var tag string
	var printOnly bool
	cmd := &cobra.Command{
		Use:    "deploy [target|all]",
		Short:  "Build+push multi-arch harness/sidecar image(s) to the registry (maintainer)",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			reg, err := loadMaintainRegistry()
			if err != nil {
				return err
			}
			ts, err := selectTargets(reg, firstArg(args), "deploy")
			if err != nil {
				return err
			}
			for _, t := range ts {
				ui.Iconf("📤", "deploying %s:%s", t.Image, tag)
				if err := runPlan(t.DeployPlan(tag), printOnly); err != nil {
					return fmt.Errorf("deploy %s: %w", t.Name, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tag, "tag", maintain.PublishTag,
		"image tag to publish (promoted from the :local build)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the plan instead of running it")
	return cmd
}

func testCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:    "test [target|all]",
		Short:  "Run a harness/sidecar def's image test suite (maintainer)",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			reg, err := loadMaintainRegistry()
			if err != nil {
				return err
			}
			ts, err := selectTargets(reg, firstArg(args), "test")
			if err != nil {
				return err
			}
			for _, t := range ts {
				plan := t.TestPlan(fileExists)
				if len(plan) == 0 {
					ui.Notef("no test.sh for %s — skipping", t.Name)
					continue
				}
				ui.Iconf("🧪", "testing %s", t.Name)
				if err := runPlan(plan, printOnly); err != nil {
					return fmt.Errorf("test %s: %w", t.Name, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the plan instead of running it")
	return cmd
}
