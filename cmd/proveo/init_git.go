// SPEC: _spec/_paradigms/git-identity.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/ui"
)

// gitStage is `proveo init`'s git-identity step. Seed still bridges the
// resolved pair into GIT_CONFIG_KEY_n; init is the one place that can ASK.
type gitStage struct {
	resolve    func() gitidentity.Identity
	lookPath   func(string) (string, error)
	setGlobal  func(name, email string) error
	typed      func() (name, email string) // page 2's user.name and user.email fields; nil asked nothing
	tty        bool
	out        *sandbox.Block // the git block; nil prints each row at once
	noTerminal bool           // no terminal stood in for the operator, so a missing identity keeps its how-to
}

// noIdentityHowTo is the one how-to a missing identity keeps when no terminal could ask for it.
const noIdentityHowTo = "`git config --global user.name` and `user.email`, or `proveo init` from a terminal"

// applyGitIdentity says in the git block what identity git holds, writing the one page 2's fields
// answered when it holds none; it returns the card row's outcome.
func applyGitIdentity(s gitStage) (choiceui.Mark, string) {
	if _, err := s.lookPath("git"); err != nil {
		s.out.Item(sandbox.Item{Warn: true, Text: "git — not on PATH"})
		return choiceui.MarkNeeds, "git is not on PATH"
	}
	id := s.resolve()
	if id.Complete() {
		s.out.Fact(ui.Fact{Role: ui.RoleHost, Key: "identity", Value: id.Name + " <" + id.Email + ">"})
		return choiceui.MarkDone, id.Name + " <" + id.Email + ">"
	}
	if !s.tty {
		missing := sandbox.Item{Warn: true, Text: "git identity — not set"}
		if s.noTerminal {
			missing.Detail = []string{noIdentityHowTo}
		}
		s.out.Item(missing)
		return choiceui.MarkNeeds, "not set"
	}
	name, email := id.Name, id.Email
	if s.typed != nil {
		name, email = s.typed()
	}
	if name == "" || email == "" {
		s.out.Item(sandbox.Item{Warn: true, Text: "git identity — not set"})
		return choiceui.MarkNeeds, "not set · both fields needed"
	}
	if s.setGlobal == nil {
		return choiceui.MarkNeeds, "not written"
	}
	if err := s.setGlobal(name, email); err != nil {
		s.out.Item(sandbox.Item{Warn: true, Text: "git identity — not written", Detail: errLines(err)})
		return choiceui.MarkNeeds, "not written"
	}
	s.out.Fact(ui.Fact{Role: ui.RoleHost, Key: "identity", Value: name + " <" + email + ">", Detail: []string{"written to git's global config"}})
	return choiceui.MarkDone, name + " <" + email + "> · git's global config"
}

// setGitConfigGlobal writes the identity to git's global config, with git's output kept.
func setGitConfigGlobal(name, email string) error {
	for _, kv := range [][2]string{{"user.name", name}, {"user.email", email}} {
		if kv[1] == "" {
			continue
		}
		if err := runCaptured(exec.Command("git", "config", "--global", kv[0], kv[1])); err != nil {
			return fmt.Errorf("git config --global %s: %w", kv[0], err)
		}
	}
	return nil
}

// hostGitStage is the git step against this host; tty is whether init may ask, which --yes takes
// away, and id is what page 2's fields answered.
func hostGitStage(tty bool, out *sandbox.Block, noTerminal bool, id gitAnswer) gitStage {
	return gitStage{
		resolve:    func() gitidentity.Identity { return gitidentity.Resolve(os.Getenv, nil) },
		lookPath:   exec.LookPath,
		setGlobal:  setGitConfigGlobal,
		typed:      func() (string, string) { return id.name, id.email },
		tty:        tty,
		out:        out,
		noTerminal: noTerminal,
	}
}
