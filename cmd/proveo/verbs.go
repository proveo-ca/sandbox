// SPEC: _spec/internal/results/result-store.puml
package main

import (
	"errors"
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
		Long: `With no argument, list the sandboxes runs kept, this repository's first, each
with why it was kept and how long ago. ` + "`proveo rm <sandbox>`" + ` carries its work home
(the clone's commits under refs/proveo/, the output dir, an OpenCode engine's
history), then removes the sandbox and clears its entry.

A running sandbox needs --stop. Work that cannot come home keeps the sandbox;
run ` + "`proveo rm <sandbox>`" + ` again once the cause is gone.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := results.Dir(run.StateDir())
			if len(args) == 0 {
				sandbox.ReconcileResults(dir, here())
				listKept(dir)
				return nil
			}
			err := sandbox.RemoveKept(args[0], dir, ui.Hostf, stop)
			sandbox.ReconcileResults(dir, here())
			return err
		},
	}
	cmd.Flags().BoolVar(&stop, "stop", false, "stop a running sandbox first (`sbx stop`), then carry its work home and remove it")
	return cmd
}

// listKept prints the sandboxes earlier runs left, this repository's first: each "<name> — <why>",
// how long ago dim under it.
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
	if len(mine)+len(others) == 0 {
		ui.Storef("no sandbox kept")
		return
	}
	for _, e := range append(mine, others...) {
		ui.Storef("%s — %s", e.Key, keptWhy(e))
		when := ago(time.Since(e.At))
		if e.Repo != "" && e.Repo != repo {
			when += " · in " + ui.Tilde(e.Repo)
		}
		ui.Detailf("%s", when)
	}
}

// ago is how long ago a thing happened, in the schedule's wait form: "19h 7m ago".
func ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	return ui.Span(d) + " ago"
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
		Long: `Run it inside the repository a run carried its work into. With no argument,
list each ref under refs/proveo/ that holds commits no local branch holds;
` + "`git log --oneline main..<ref>`" + ` reviews one.

` + "`proveo adopt <ref|session|branch>`" + ` creates the branch and nothing else: HEAD,
the index and your files stay as they are. ` + "`git switch <branch>`" + ` when you want it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := here()
			if repo == "" {
				return reported(sandbox.Item{Fail: true, Text: "adopt — not inside a git repository"},
					errors.New("proveo adopt works inside the repository a run carried its work into"))
			}
			dir := results.Dir(run.StateDir())
			refs, err := adopt.Carried(repo)
			if err != nil {
				return reported(adoptRefusal(err, as), err)
			}
			if len(args) == 0 {
				listWaiting(adopt.Waiting(refs))
				sandbox.ReconcileResults(dir, repo)
				return nil
			}
			ref, err := adopt.Pick(refs, args[0])
			if err != nil {
				return reported(adoptRefusal(err, as), err)
			}
			name, err := adopt.Branch(repo, ref, as)
			if err != nil {
				return reported(adoptRefusal(err, as), err)
			}
			sandbox.ReconcileResults(dir, repo)
			ui.Okf("branch %s — created at %s", name, ref.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "name the new branch (default: the carried ref's own branch name)")
	return cmd
}

// adoptRefusal is the × item an adopt error stands for; --as is the fix only for a clash with the
// ref's own branch name.
func adoptRefusal(err error, as string) sandbox.Item {
	var (
		notListed  adopt.NotListed
		noMatch    adopt.NoMatch
		ambiguous  adopt.Ambiguous
		badName    adopt.BadName
		exists     adopt.Exists
		notCreated adopt.NotCreated
	)
	it := sandbox.Item{Fail: true}
	switch {
	case errors.As(err, &notListed):
		it.Text, it.Detail = "adopt — git could not list "+adopt.RefRoot, errLines(errors.New(notListed.Out))
	case errors.As(err, &noMatch):
		it.Text = noMatch.Arg + " — no carried ref matches"
	case errors.As(err, &ambiguous):
		it.Text, it.Detail = fmt.Sprintf("%s — fits %d carried refs", ambiguous.Arg, len(ambiguous.Refs)), ambiguous.Refs
	case errors.As(err, &badName):
		it.Text = badName.Name + " — not a valid branch name"
	case errors.As(err, &exists):
		it.Text = exists.Name + " — a branch of that name exists"
		if as == "" {
			it.Detail = []string{"`--as <name>` names the new branch"}
		}
	case errors.As(err, &notCreated):
		it.Text, it.Detail = "branch "+notCreated.Name+" — not created", errLines(errors.New(notCreated.Out))
	default:
		it.Text, it.Detail = "adopt — failed", errLines(err)
	}
	return it
}

// listWaiting prints the refs that hold work no branch has as one "to adopt" fact, each with its count
// in the words of a run's branches fact.
func listWaiting(refs []adopt.Ref) {
	if len(refs) == 0 {
		ui.Facts(ui.Fact{Role: ui.RoleStore, Key: "to adopt", Value: "none"})
		return
	}
	facts := make([]ui.Fact, len(refs))
	for i, r := range refs {
		facts[i] = ui.Fact{Role: ui.RoleStore, Value: fmt.Sprintf("%s (%s)", r.Name, newCommits(r.Lead))}
	}
	facts[0].Key = "to adopt"
	ui.Facts(facts...)
}

func newCommits(n int) string {
	if n == 1 {
		return "1 new commit"
	}
	return fmt.Sprintf("%d new commits", n)
}
