// SPEC: _spec/internal/ui/step-lines.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// stepKind is how a step shows while it runs.
type stepKind int

const (
	stepLive   stepKind = iota // a live line; plain output keeps its line
	stepHand                   // its line stays, then another program or a prompt has the terminal
	stepQuiet                  // a live line alone: its caller prints what it found
	stepSilent                 // nothing: its own heading and block follow
)

// applyStep is one thing setup's Enter runs. A step with no name runs unlisted.
type applyStep struct {
	name  string // what the setup page lists and the live frame names
	line  string // what stays on screen: a live step's plain line, a hand step's start line
	label string // the setup page's row it answers, as the result names it
	kind  stepKind
	run   func() error
}

// applyCtx is what one apply acts on.
type applyCtx struct {
	plan     sbx.Plan // the release plan: what to download and how to place it
	applied  sbx.Plan // the plan setup acts on once sbx is in place
	download string
	host     sbx.Host
	rep      *initReport // where the steps' facts and items gather; nil when only the names are read
}

// applyPlan lists what setup runs for d, in the order it runs it. The setup page
// reads the names; the apply runs the steps.
func applyPlan(d initDecisions, c applyCtx) []applyStep {
	var out, sbxSteps []applyStep
	bin := func() string { return resolveBin(c.applied) }
	var sum string
	var verified bool
	switch d.Install {
	case installPackaged:
		if p := c.plan.Packaged; p != nil {
			sbxSteps = append(sbxSteps,
				applyStep{name: "download the distro package", line: "download the distro package", label: rowInstall,
					run: func() (err error) { sum, err = fetch(p.URL, filepath.Join(c.download, p.Asset)); return err }},
				applyStep{name: "install the distro package", line: "distro package — installing sbx " + sbx.Release, label: rowInstall, kind: stepHand,
					run: func() error { return runPackage(*p, c.download, sum, c.rep) }})
		} else {
			sbxSteps = append(sbxSteps, applyStep{label: rowInstall,
				run: func() error { return fmt.Errorf("this release ships no distro package for %s", describeHost(c.host)) }})
		}
	case installTarball:
		sbxSteps = append(sbxSteps,
			applyStep{name: "download sbx " + sbx.Release, line: "download sbx " + sbx.Release, label: rowInstall,
				run: func() (err error) { sum, verified, err = fetchRelease(c.plan, c.download); return err }},
			applyStep{name: "install sbx " + installPlace(c.plan, false), line: "sbx " + sbx.Release + " — installing " + installPlace(c.plan, true),
				label: rowInstall, kind: stepHand, run: func() error { return placeRelease(c.plan, c.download, sum, verified, c.rep) }})
	}
	if dir := filepath.Dir(c.applied.Bin); c.applied.Bin != "" && !onPath(dir) && (d.Install == installTarball || sbxPathDir(c.applied) != "") &&
		(dir != d.pathDone || d.AddPath && !rcAdds(dir)) {
		s := applyStep{label: rowPath, run: func() error {
			if dir := sbxPathDir(c.applied); dir != "" {
				c.rep.sbxItem(ensureSbxOnPath(dir, d.AddPath))
			}
			return nil
		}}
		if d.AddPath {
			s.name, s.line = "add "+dir+" to PATH", "add "+ui.Tilde(dir)+" to PATH"
		}
		sbxSteps = append(sbxSteps, s)
	}
	if d.Login {
		sbxSteps = append(sbxSteps, applyStep{name: actSignIn, line: "sbx login — signing in to Docker", label: rowSignIn, kind: stepHand,
			run: func() error { return handOff(command(bin(), sbx.LoginArgs()...)) }})
	}
	if d.Wizard {
		sbxSteps = append(sbxSteps, applyStep{name: actWizard, line: "sbx setup — running the first-run wizard", label: rowWizard, kind: stepHand,
			run: func() error {
				err := handOff(command(bin(), sbx.SetupArgs()...))
				if interrupted(err) {
					return err
				}
				if err != nil {
					c.rep.sbxItem(sandbox.Item{Warn: true, Text: "sbx setup — did not complete",
						Detail: append(errDetail(err), "run `"+sbx.Binary+" setup` yourself if a sandbox misbehaves")})
				}
				return nil
			}})
	}
	if d.Baseline != "" {
		args := sbx.PolicyInitArgs(d.Baseline)
		sbxSteps = append(sbxSteps, applyStep{name: "bind the network baseline: " + d.Baseline,
			line: "sbx policy init — binding the " + d.Baseline + " network baseline", label: rowBaseline, kind: stepHand,
			run: func() error {
				err := handOff(command(bin(), args...))
				if interrupted(err) {
					return err
				}
				if err != nil {
					c.rep.sbxItem(sandbox.Item{Warn: true, Text: "network baseline — not changed",
						Detail: append(errDetail(err), "`"+sbx.Binary+" "+strings.Join(args, " ")+"` by hand")})
				}
				return nil
			}})
	}
	if c.rep != nil && (len(sbxSteps) > 0 || !c.rep.sbxSaid) {
		out = append(out, applyStep{run: func() error { c.rep.openSbx(); return nil }})
		out = append(out, sbxSteps...)
		out = append(out, applyStep{run: func() error { c.rep.printSbx(); return nil }})
	} else {
		out = append(out, sbxSteps...)
	}
	if d.Prompt {
		out = append(out, applyStep{name: actPrompt, label: rowPrompt, kind: stepSilent,
			run: func() error { applyPromptKit(!d.NoPrompt && interactiveTerminal(), c.rep); return nil }})
	}
	if c.rep != nil && (d.Git || d.Gh) {
		out = append(out, applyStep{run: func() error { c.rep.openGit(); return nil }})
	}
	if d.Git {
		out = append(out, applyStep{name: actGit, label: rowGit, kind: stepSilent, run: func() error {
			applyGitIdentity(hostGitStage(!d.NoPrompt && interactiveTerminal(), c.rep.gitBlock(), c.rep.noTerminal()))
			return nil
		}})
	}
	if d.Gh {
		out = append(out, applyStep{name: actGitHub, label: rowGh, kind: stepSilent, run: func() error {
			tty := !d.NoPrompt && interactiveTerminal()
			return applyGitHub(ghStage{
				auth:       hostGhAuth(),
				host:       c.host,
				run:        runArgv,
				secretSet:  func(name, value string) error { return secretSetAt(bin(), name, value) },
				tty:        tty,
				out:        c.rep.gitBlock(),
				noTerminal: c.rep.noTerminal(),
				access: func() error {
					return applyGitHubAccess(bin(), tty, c.rep.gitBlock(), c.rep.noTerminal())
				},
			})
		}})
	}
	if c.rep != nil && (d.Git || d.Gh) {
		out = append(out, applyStep{run: func() error { c.rep.printGit(); return nil }})
	}
	out = append(out, applyStep{name: "verify sbx (sbx diagnose)", label: labelVerify, kind: stepQuiet,
		run: func() error { return verify(bin(), c.rep) }})
	return out
}

// installPlace says where the release lands: into its prefix, or wherever its own installer
// puts it when there is none.
func installPlace(plan sbx.Plan, screen bool) string {
	switch {
	case plan.Prefix != "" && screen:
		return "into " + ui.Tilde(plan.Prefix)
	case plan.Prefix != "":
		return "into " + plan.Prefix
	case len(plan.Steps) > 0 && len(plan.Steps[0].Argv) > 0:
		return "with " + filepath.Base(plan.Steps[0].Argv[0])
	}
	return "with its installer"
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

// runApply runs plan in order and stops at the first step that fails; ran hears the label of
// each step that did not fail. A line that stays on screen carries no place: a live step keeps
// its line in plain output, a hand step prints it before it gives the terminal away.
func runApply(plan []applyStep, ran func(label string)) error {
	steps := ui.NewSteps(len(stepNames(plan)))
	for i, s := range plan {
		var err error
		switch {
		case s.name == "":
			err = s.run()
		case s.kind == stepHand:
			err = steps.HandAs(s.name, s.line, s.run)
		case s.kind == stepQuiet:
			err = steps.Quiet(s.name, s.run)
		case s.kind == stepSilent:
			err = steps.Silent(s.name, s.run)
		default:
			err = steps.DoAs(s.name, cmp.Or(s.line, s.name), s.run)
		}
		if err != nil {
			var rest []string
			for _, r := range plan[i+1:] {
				if r.label != "" && r.label != s.label {
					rest = append(rest, r.label)
				}
			}
			return stepErr{label: s.label, rest: rest, err: err}
		}
		if ran != nil {
			ran(s.label)
		}
	}
	return nil
}

// runPackage hands the downloaded package to the distro's package manager.
func runPackage(p sbx.Packaged, download, sum string, rep *initReport) error {
	ui.Detailf("sudo may ask for your password")
	c := command(p.Argv[0], p.Argv[1:]...)
	c.Dir = download
	if err := handOff(c); err != nil {
		return err
	}
	rep.sbxItem(sandbox.Item{Ok: true, Text: "distro package — installed",
		Detail: []string{"sha256 " + sum, "no published digest covers this asset"}})
	return nil
}

// fetchRelease downloads the release asset and holds it to its published digest; verified says
// whether one covered it.
func fetchRelease(plan sbx.Plan, download string) (sum string, verified bool, err error) {
	if err := prefixWritable(plan.Prefix); err != nil {
		return "", false, err
	}
	if sum, err = fetch(plan.URL, filepath.Join(download, plan.Asset)); err != nil {
		return "", false, err
	}
	if plan.Provenance == nil {
		return sum, false, nil
	}
	want, err := publishedDigest(*plan.Provenance)
	if err != nil {
		return "", false, err
	}
	if sum != want {
		return "", false, fmt.Errorf("%s does not match the digest published for %s: got %s, want %s",
			plan.Asset, plan.Provenance.Subject, sum, want)
	}
	return sum, true, nil
}

// placeRelease runs the release's own install steps, checks sbx landed, and says so in the sbx
// block. Where the "sbx:" fact names the version and the bin, the ✓ names neither; it names the
// version a prefix held before.
func placeRelease(plan sbx.Plan, download, sum string, verified bool, rep *initReport) error {
	var replaced string
	if _, err := os.Stat(plan.Bin); plan.Bin != "" && err == nil {
		replaced, _ = sbx.VersionAt(plan.Bin)
	}
	for _, s := range plan.Steps {
		c := command(s.Argv[0], s.Argv[1:]...)
		c.Stdin = nil
		c.Dir = cmp.Or(s.Dir, download)
		c.Env = append(os.Environ(), s.Env...)
		if err := handOff(c); err != nil {
			return fmt.Errorf("%s (%s): %w", s.What, strings.Join(s.Argv, " "), err)
		}
	}
	it := sandbox.Item{Ok: true, Text: "sbx — installed"}
	if plan.Bin != "" {
		if _, err := os.Stat(plan.Bin); err != nil {
			return fmt.Errorf("the installer reported success, but %s is not there: %w", plan.Bin, err)
		}
	} else {
		it.Text = "sbx " + sbx.Release + " — installed"
		if note, _, _ := strings.Cut(plan.Note, ";"); note != "" {
			it.Detail = append(it.Detail, note)
		}
	}
	digest := "no published digest covers this asset"
	if verified {
		digest = "matches the release's provenance"
	}
	it.Detail = append(it.Detail, "sha256 "+sum, digest)
	if replaced != "" {
		it.Detail = append(it.Detail, "replaced "+replaced)
	}
	rep.sbxItem(it)
	return nil
}
