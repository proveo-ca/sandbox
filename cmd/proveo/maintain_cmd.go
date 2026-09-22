// SPEC: _spec/internal/maintain/image-build-deploy.puml, _spec/_devops/image-lineage-and-publish.puml
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

type planIO struct {
	printOnly bool
	out       io.Writer
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	env       []string
}

func runPlan(cmds []maintain.Command, pio planIO) error {
	if pio.out == nil {
		pio.out = os.Stdout
	}
	if pio.stdout == nil {
		pio.stdout = os.Stdout
	}
	if pio.stderr == nil {
		pio.stderr = os.Stderr
	}
	if pio.stdin == nil && !pio.printOnly {
		pio.stdin = os.Stdin
	}
	for _, c := range cmds {
		if pio.printOnly {
			prefix := ""
			if c.Dir != "" {
				prefix = "(cd " + c.Dir + ") "
			}
			fmt.Fprintf(pio.out, "%s%s\n", prefix, strings.Join(c.Argv, " "))
			continue
		}
		ex := exec.Command(c.Argv[0], c.Argv[1:]...)
		ex.Dir = c.Dir
		ex.Stdin, ex.Stdout, ex.Stderr = pio.stdin, pio.stdout, pio.stderr
		if c.Quiet {
			ex.Stdout = io.Discard
		}
		if len(pio.env) > 0 {
			ex.Env = append(os.Environ(), pio.env...)
		}
		if err := ex.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(c.Argv, " "), err)
		}
	}
	return nil
}

type targetTiming struct {
	name     string
	duration time.Duration
	recorded bool
	err      error
}

type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	rest   []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(b)
	p.rest = append(p.rest, b...)
	for {
		i := bytes.IndexByte(p.rest, '\n')
		if i < 0 {
			break
		}
		line := p.rest[:i+1]
		p.rest = p.rest[i+1:]
		if _, err := io.WriteString(p.w, p.prefix); err != nil {
			return n, err
		}
		if _, err := p.w.Write(line); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (p *prefixWriter) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.rest) == 0 {
		return
	}
	_, _ = io.WriteString(p.w, p.prefix)
	_, _ = p.w.Write(p.rest)
	p.rest = nil
}

func runTimed(t maintain.Target, mu sync.Locker, run func(maintain.Target) error) targetTiming {
	start := time.Now()
	err := run(t)
	d := time.Since(start)
	line, ferr := maintain.FormatTargetElapsed(t.Name, &d)
	if ferr != nil {
		return targetTiming{name: t.Name, err: ferr}
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if err != nil {
		ui.Failf("%s failed after %s", t.Name, d.Round(time.Millisecond))
		return targetTiming{name: t.Name, duration: d, recorded: true, err: err}
	}
	ui.Okf("%s", line)
	return targetTiming{name: t.Name, duration: d, recorded: true}
}

func executeFleet(verb string, ts []maintain.Target, printOnly bool, out io.Writer, start func(maintain.Target), run func(maintain.Target, planIO) error) error {
	if out == nil {
		out = os.Stdout
	}
	waves := maintain.Schedule(ts)
	wall0 := time.Now()
	var results []targetTiming
	for _, w := range waves {
		if printOnly {
			fmt.Fprintln(out, maintain.FormatWaveHeader(w))
			for _, t := range w.Targets {
				start(t)
				if err := run(t, planIO{printOnly: true, out: out}); err != nil {
					return err
				}
			}
			continue
		}
		if w.Concurrent && len(w.Targets) > 1 {
			var mu sync.Mutex
			var wg sync.WaitGroup
			waveOut := make([]targetTiming, len(w.Targets))
			for i, t := range w.Targets {
				wg.Add(1)
				go func(i int, t maintain.Target) {
					defer wg.Done()
					mu.Lock()
					start(t)
					mu.Unlock()
					pw := &prefixWriter{mu: &mu, w: os.Stdout, prefix: "[" + t.Name + "] "}
					pe := &prefixWriter{mu: &mu, w: os.Stderr, prefix: "[" + t.Name + "] "}
					waveOut[i] = runTimed(t, &mu, func(tgt maintain.Target) error {
						err := run(tgt, planIO{
							stdout: pw,
							stderr: pe,
							env:    []string{"BUILDKIT_PROGRESS=plain"},
						})
						pw.flush()
						pe.flush()
						return err
					})
				}(i, t)
			}
			wg.Wait()
			results = append(results, waveOut...)
			var errs []error
			for _, r := range waveOut {
				if r.err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", r.name, r.err))
				}
			}
			if len(errs) > 0 {
				return errors.Join(errs...)
			}
			continue
		}
		for _, t := range w.Targets {
			start(t)
			r := runTimed(t, nil, func(tgt maintain.Target) error {
				return run(tgt, planIO{})
			})
			results = append(results, r)
			if r.err != nil {
				return fmt.Errorf("%s %s: %w", verb, t.Name, r.err)
			}
		}
	}
	if printOnly {
		return nil
	}
	for _, r := range results {
		if !r.recorded {
			return fmt.Errorf("%s summary missing duration for %s", verb, r.name)
		}
	}
	wall := time.Since(wall0)
	summary, err := maintain.FormatRunSummary(verb, len(results), &wall)
	if err != nil {
		return err
	}
	ui.Appf("%s", summary)
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
			return executeFleet("build", ts, printOnly, os.Stdout,
				func(t maintain.Target) {
					ui.Appf("building %s (%s:%s)", t.Name, t.Image, tag)
				},
				func(t maintain.Target, pio planIO) error {
					return runPlan(t.BuildPlan(tag, noCache), pio)
				})
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
			return executeFleet("deploy", ts, printOnly, os.Stdout,
				func(t maintain.Target) {
					ui.Cloudf("deploying %s:%s", t.Image, tag)
				},
				func(t maintain.Target, pio planIO) error {
					return runPlan(t.DeployPlan(tag), pio)
				})
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
				ui.Appf("testing %s", t.Name)
				if printOnly {
					if err := runPlan(plan, planIO{printOnly: true, out: os.Stdout}); err != nil {
						return fmt.Errorf("test %s: %w", t.Name, err)
					}
					continue
				}
				r := runTimed(t, nil, func(maintain.Target) error {
					return runPlan(plan, planIO{})
				})
				if r.err != nil {
					return fmt.Errorf("test %s: %w", t.Name, r.err)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the plan instead of running it")
	return cmd
}
