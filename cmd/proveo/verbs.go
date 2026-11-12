// SPEC: _spec/internal/results/result-store.puml
package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/adopt"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/ui"
)

// here is the repository the command runs in, or "" outside one.
func here() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return results.Canonical(strings.TrimSpace(string(out)))
}

// rmCmd removes a sandbox a run left behind, after carrying home what it still holds.
func rmCmd() *cobra.Command {
	var stop bool
	cmd := &cobra.Command{
		Use:   "rm [sandbox]",
		Short: "Carry a kept sandbox's work home, then remove it (no argument lists them)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := results.Dir(run.StateDir())
			sandbox.ReconcileResults(dir, here())
			if len(args) == 0 {
				listKept(dir)
				return nil
			}
			ui.Section(ui.SectionResults)
			if err := sandbox.RemoveKept(args[0], dir, ui.Hostf, stop); err != nil {
				return err
			}
			ui.Okf("%s is removed", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&stop, "stop", false, "stop a running sandbox first (`sbx stop`), then carry its work home and remove it")
	return cmd
}

// listKept prints the sandboxes earlier runs left, this repository's first.
func listKept(dir string) {
	repo := here()
	var mine, others []results.Entry
	for _, e := range results.List(dir) {
		switch {
		case e.Kind != results.Kept:
		case repo != "" && e.Repo == repo:
			mine = append(mine, e)
		default:
			others = append(others, e)
		}
	}
	ui.Section(ui.SectionResults)
	if len(mine)+len(others) == 0 {
		ui.Notef("no run left a sandbox behind")
		return
	}
	for _, e := range append(mine, others...) {
		ui.Storef("%s — %s, %s ago", e.Key, keptWhy(e), time.Since(e.At).Round(time.Minute))
		if e.Repo != "" && e.Repo != repo {
			ui.Notef("in %s", e.Repo)
		}
	}
	ui.Notef("`proveo rm <sandbox>` carries its work home and removes it")
}

func keptWhy(e results.Entry) string {
	if e.Why == "" {
		return "kept"
	}
	return e.Why
}

// adoptCmd turns a carried ref into a branch and touches nothing else.
func adoptCmd() *cobra.Command {
	var as string
	cmd := &cobra.Command{
		Use:   "adopt [ref|session]",
		Short: "Create a branch at work a run carried home (no argument lists it)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := here()
			if repo == "" {
				return fmt.Errorf("proveo adopt works inside the repository a run carried its work into")
			}
			dir := results.Dir(run.StateDir())
			refs, err := adopt.Carried(repo)
			if err != nil {
				return err
			}
			ui.Section(ui.SectionResults)
			if len(args) == 0 {
				listWaiting(adopt.Waiting(refs))
				sandbox.ReconcileResults(dir, repo)
				return nil
			}
			ref, err := adopt.Pick(refs, args[0])
			if err != nil {
				return err
			}
			name, err := adopt.Branch(repo, ref, as)
			if err != nil {
				return err
			}
			sandbox.ReconcileResults(dir, repo)
			ui.Okf("branch %s is at %s", name, ref.Name)
			ui.Notef("HEAD, the index and your files are untouched — `git switch %s` when you want it", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "name the new branch (default: the carried ref's own branch name)")
	return cmd
}

func listWaiting(refs []adopt.Ref) {
	if len(refs) == 0 {
		ui.Notef("no carried work waits here: every ref under %s is already on a branch", adopt.RefRoot)
		return
	}
	for _, r := range refs {
		ui.Storef("%s — %s", r.Name, commits(r.Lead))
	}
	ui.Notef("`proveo adopt <ref>` creates a branch there; `git log --oneline main..<ref>` reviews it")
}

func commits(n int) string {
	if n == 1 {
		return "1 commit no branch holds"
	}
	return fmt.Sprintf("%d commits no branch holds", n)
}
