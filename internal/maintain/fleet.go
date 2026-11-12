// SPEC: _spec/internal/maintain/image-build-deploy.puml
package maintain

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

// PlanIO routes one plan: PrintOnly writes each argv to Out instead of running it.
type PlanIO struct {
	PrintOnly bool
	Out       io.Writer
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Env       []string
}

// RunPlan runs cmds in order and stops at the first error.
func RunPlan(cmds []Command, pio PlanIO) error {
	if pio.Out == nil {
		pio.Out = os.Stdout
	}
	if pio.Stdout == nil {
		pio.Stdout = os.Stdout
	}
	if pio.Stderr == nil {
		pio.Stderr = os.Stderr
	}
	if pio.Stdin == nil && !pio.PrintOnly {
		pio.Stdin = os.Stdin
	}
	for _, c := range cmds {
		if pio.PrintOnly {
			prefix := ""
			if c.Dir != "" {
				prefix = "(cd " + c.Dir + ") "
			}
			fmt.Fprintf(pio.Out, "%s%s\n", prefix, strings.Join(c.Argv, " "))
			continue
		}
		if c.Run != nil {
			if err := c.Run(pio.Stdout, pio.Stderr, pio.Env); err != nil {
				return fmt.Errorf("%s: %w", strings.Join(c.Argv, " "), err)
			}
			continue
		}
		ex := exec.Command(c.Argv[0], c.Argv[1:]...)
		ex.Dir = c.Dir
		ex.Stdin, ex.Stdout, ex.Stderr = pio.Stdin, pio.Stdout, pio.Stderr
		if c.Quiet {
			ex.Stdout = io.Discard
		}
		if len(pio.Env) > 0 {
			ex.Env = append(os.Environ(), pio.Env...)
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

// prefixWriter writes each whole line to w behind prefix; flush writes the partial rest.
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

func runTimed(t Target, mu sync.Locker, run func(Target) error) targetTiming {
	start := time.Now()
	err := run(t)
	d := time.Since(start)
	line, ferr := FormatTargetElapsed(t.Name, &d)
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

// RunFleet runs ts in Schedule waves: each base alone, then harness waves concurrently; it stops after the first red wave.
func RunFleet(verb string, ts []Target, printOnly bool, out io.Writer, start func(Target), run func(Target, PlanIO) error) error {
	if out == nil {
		out = os.Stdout
	}
	waves := Schedule(ts)
	wall0 := time.Now()
	var results []targetTiming
	for _, w := range waves {
		if printOnly {
			fmt.Fprintln(out, FormatWaveHeader(w))
			for _, t := range w.Targets {
				start(t)
				if err := run(t, PlanIO{PrintOnly: true, Out: out}); err != nil {
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
				go func(i int, t Target) {
					defer wg.Done()
					mu.Lock()
					start(t)
					mu.Unlock()
					pw := &prefixWriter{mu: &mu, w: os.Stdout, prefix: "[" + t.Name + "] "}
					pe := &prefixWriter{mu: &mu, w: os.Stderr, prefix: "[" + t.Name + "] "}
					waveOut[i] = runTimed(t, &mu, func(tgt Target) error {
						err := run(tgt, PlanIO{
							Stdout: pw,
							Stderr: pe,
							Env:    []string{"BUILDKIT_PROGRESS=plain"},
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
			r := runTimed(t, nil, func(tgt Target) error {
				return run(tgt, PlanIO{})
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
	summary, err := FormatRunSummary(verb, len(results), &wall)
	if err != nil {
		return err
	}
	ui.Appf("%s", summary)
	return nil
}

// DefsDir is PROVEO_DEFS_DIR, else <cwd>/defs when that is a directory.
func DefsDir(getenv func(string) string, cwd string) (string, error) {
	if d := getenv("PROVEO_DEFS_DIR"); d != "" {
		return d, nil
	}
	d := filepath.Join(cwd, "defs")
	if fi, err := os.Stat(d); err == nil && fi.IsDir() {
		return d, nil
	}
	return "", errors.New("no defs/ tree found (run inside the repo or set PROVEO_DEFS_DIR)")
}

func targetNames(reg []Target) string {
	ns := make([]string, len(reg))
	for i, t := range reg {
		ns[i] = t.Name
	}
	return strings.Join(ns, ", ")
}

// Select returns every target for all, else the one target named arg.
func Select(reg []Target, arg string) ([]Target, error) {
	arg = strings.TrimSpace(arg)
	switch arg {
	case "all":
		return reg, nil
	case "":
		return nil, fmt.Errorf("no target given; pass a target name or all (targets: %s)", targetNames(reg))
	}
	for _, t := range reg {
		if t.Name == arg {
			return []Target{t}, nil
		}
	}
	return nil, fmt.Errorf("unknown target %q — use 'all' or one of: %s", arg, targetNames(reg))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Fleet runs verb (build, deploy or test) over arg, a registry target or all, from the defs under defsDir.
func Fleet(verb, arg, tag string, noCache, printOnly bool, defsDir string, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	switch verb {
	case "build":
		tag = normTag(tag, LocalTag)
	case "deploy":
		tag = normTag(tag, PublishTag)
	case "test":
	default:
		return fmt.Errorf("unknown verb %q; want build, deploy or test", verb)
	}
	ms, err := manifest.Load(defsDir)
	if err != nil {
		return fmt.Errorf("loading manifests from %s: %w", defsDir, err)
	}
	ts, err := Select(Registry(ms, defsDir), arg)
	if err != nil {
		return err
	}
	switch verb {
	case "build":
		return RunFleet(verb, ts, printOnly, out,
			func(t Target) { ui.Appf("building %s (%s:%s)", t.Name, t.Image, tag) },
			func(t Target, pio PlanIO) error { return RunPlan(t.BuildPlan(tag, noCache), pio) })
	case "deploy":
		return RunFleet(verb, ts, printOnly, out,
			func(t Target) { ui.Cloudf("deploying %s:%s", t.Image, tag) },
			func(t Target, pio PlanIO) error { return RunPlan(t.DeployPlan(tag), pio) })
	}
	return testSuites(ts, printOnly, out)
}

// testSuites runs each distinct image suite of ts once, in order, and stops at the first red one.
func testSuites(ts []Target, printOnly bool, out io.Writer) error {
	ran := map[string]bool{}
	for _, t := range ts {
		plan := t.TestPlan(fileExists)
		if len(plan) == 0 {
			ui.Notef("no image suite for %s — skipping", t.Name)
			continue
		}
		if ran[t.Suite] {
			continue
		}
		ran[t.Suite] = true
		ui.Appf("testing %s", t.Name)
		if printOnly {
			if err := RunPlan(plan, PlanIO{PrintOnly: true, Out: out}); err != nil {
				return fmt.Errorf("test %s: %w", t.Name, err)
			}
			continue
		}
		r := runTimed(t, nil, func(Target) error { return RunPlan(plan, PlanIO{}) })
		if r.err != nil {
			return fmt.Errorf("test %s: %w", t.Name, r.err)
		}
	}
	return nil
}
