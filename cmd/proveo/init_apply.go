// SPEC: _spec/internal/ui/step-lines.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// applyStep is one thing setup's Enter runs. A step with no name runs unlisted.
type applyStep struct {
	name  string
	hands bool // it gives the terminal to another program, or to a prompt of its own
	run   func() error
}

// applyCtx is what one apply acts on.
type applyCtx struct {
	plan     sbx.Plan // the release plan: what to download and how to place it
	applied  sbx.Plan // the plan setup acts on once sbx is in place
	download string
	host     sbx.Host
}

// applyPlan lists what setup runs for d, in the order it runs it. The setup page
// reads the names; the apply runs the steps.
func applyPlan(d initDecisions, c applyCtx) []applyStep {
	var out []applyStep
	add := func(name string, hands bool, run func() error) {
		out = append(out, applyStep{name, hands, run})
	}
	bin := func() string { return resolveBin(c.applied) }
	switch d.Install {
	case installPackaged:
		if p := c.plan.Packaged; p != nil {
			add("download the distro package", false, func() error { return fetchPackage(*p, c.download) })
			add("install the distro package", true, func() error { return runPackage(*p, c.download) })
		}
	case installTarball:
		add("download sbx "+sbx.Release, false, func() error { return fetchRelease(c.plan, c.download) })
		add("install sbx into "+c.plan.Prefix, true, func() error { return placeRelease(c.plan, c.download) })
	}
	pathLine := ""
	if dir := filepath.Dir(c.applied.Bin); d.AddPath && c.applied.Bin != "" && !onPath(dir) {
		pathLine = "add " + dir + " to PATH"
	}
	add(pathLine, false, func() error {
		if dir := sbxPathDir(c.applied); dir != "" {
			ensureSbxOnPath(dir, d.AddPath)
		}
		return nil
	})
	if d.Login {
		add("sign in to Docker (sbx login)", true, func() error {
			ui.Section(ui.SectionCredentials)
			ui.Hostf("signing in to Docker — `%s login` owns this, and proveo never sees the credential", sbx.Binary)
			if err := interactive(bin(), sbx.LoginArgs()); err != nil {
				return fmt.Errorf("sbx login: %w", err)
			}
			return nil
		})
	}
	if d.Wizard {
		add("run sbx's first-run wizard (sbx setup)", true, func() error {
			ui.Section(ui.SectionStarting)
			ui.Appf("running the first-run wizard (`%s setup`) — 0.42 stopped opening it on its own", sbx.Binary)
			if err := interactive(bin(), sbx.SetupArgs()); err != nil {
				ui.Warnf("`%s setup` did not complete (%v) — run it yourself if a sandbox misbehaves", sbx.Binary, err)
			}
			return nil
		})
	}
	if d.Baseline != "" {
		add("bind the network baseline: "+d.Baseline, true, func() error {
			args := strings.Join(sbx.PolicyInitArgs(d.Baseline), " ")
			ui.Section(ui.SectionEgress)
			ui.Appf("binding the host-wide network baseline: `%s %s`", sbx.Binary, args)
			if err := interactive(bin(), sbx.PolicyInitArgs(d.Baseline)); err != nil {
				ui.Warnf("the baseline was not changed (%v) — `%s %s` by hand", err, sbx.Binary, args)
			}
			return nil
		})
	}
	if d.Git {
		add("set the sandbox git identity", true, func() error { applyGitIdentity(hostGitStage()); return nil })
	}
	if d.Prompt {
		add("install the prompt kit", true, func() error { applyPromptKit(!d.NoPrompt && interactiveTerminal()); return nil })
	}
	if d.Gh {
		add("connect GitHub", true, func() error {
			applyGitHub(ghStage{
				auth:      hostGhAuth(),
				host:      c.host,
				run:       runArgv,
				secretSet: func(name, value string) error { return secretSetAt(bin(), name, value) },
				tty:       !d.NoPrompt && interactiveTerminal(),
				access:    func() { applyGitHubAccess(bin(), !d.NoPrompt && interactiveTerminal()) },
			})
			return nil
		})
	}
	add("verify sbx (sbx diagnose)", false, func() error { return verify(bin()) })
	return out
}

// stepNames are the listed steps of plan, in order.
func stepNames(plan []applyStep) []string {
	var out []string
	for _, s := range plan {
		if s.name != "" {
			out = append(out, s.name)
		}
	}
	return out
}

// runApply runs plan in order and stops at the first step that fails. A step
// that hands the terminal away gets a start line; any other gets a live line.
func runApply(plan []applyStep) error {
	steps := ui.NewSteps(len(stepNames(plan)))
	for _, s := range plan {
		var err error
		switch {
		case s.name == "":
			err = s.run()
		case s.hands:
			err = steps.Hand(s.name, s.run)
		default:
			err = steps.Do(s.name, s.run)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// fetchPackage downloads the distro package.
func fetchPackage(p sbx.Packaged, download string) error {
	ui.Appf("downloading %s", p.Asset)
	sum, err := fetch(p.URL, filepath.Join(download, p.Asset))
	if err != nil {
		return err
	}
	ui.Notef("sha256 %s (no published digest covers this asset)", sum)
	return nil
}

// runPackage hands the downloaded package to the distro's package manager.
func runPackage(p sbx.Packaged, download string) error {
	c := exec.Command(p.Argv[0], p.Argv[1:]...)
	c.Dir = download
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(p.Argv, " "), err)
	}
	return nil
}

// fetchRelease downloads the release asset and holds it to its published digest.
func fetchRelease(plan sbx.Plan, download string) error {
	if err := prefixWritable(plan.Prefix); err != nil {
		return err
	}
	ui.Appf("downloading %s", plan.Asset)
	sum, err := fetch(plan.URL, filepath.Join(download, plan.Asset))
	if err != nil {
		return err
	}
	if plan.Provenance == nil {
		ui.Notef("sha256 %s (no published digest covers this asset)", sum)
		return nil
	}
	want, err := publishedDigest(*plan.Provenance)
	if err != nil {
		return err
	}
	if sum != want {
		return fmt.Errorf("%s does not match the digest published for %s: got %s, want %s",
			plan.Asset, plan.Provenance.Subject, sum, want)
	}
	ui.Okf("sha256 %s — matches the release's provenance", sum)
	return nil
}

// placeRelease runs the release's own install steps and checks sbx landed.
func placeRelease(plan sbx.Plan, download string) error {
	for _, s := range plan.Steps {
		ui.Appf("%s", s.What)
		c := exec.Command(s.Argv[0], s.Argv[1:]...)
		c.Dir = s.Dir
		if c.Dir == "" {
			c.Dir = download
		}
		c.Env = append(os.Environ(), s.Env...)
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s (%s): %w", s.What, strings.Join(s.Argv, " "), err)
		}
	}
	if plan.Bin != "" {
		if _, err := os.Stat(plan.Bin); err != nil {
			return fmt.Errorf("the installer reported success but %s is not there: %w", plan.Bin, err)
		}
		ui.Okf("installed %s", plan.Bin)
	}
	return nil
}
