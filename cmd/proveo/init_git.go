// SPEC: _spec/_paradigms/git-identity.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/ui"
)

// gitStage is `proveo init`'s git-identity step. Seed still bridges the
// resolved pair into GIT_CONFIG_KEY_n; init is the one place that can ASK.
type gitStage struct {
	resolve    func() gitidentity.Identity
	lookPath   func(string) (string, error)
	setGlobal  func(name, email string) error
	prompt     func(label string) string
	tty        bool
	out        *sandbox.Block // the git block; nil prints each row at once
	noTerminal bool           // no terminal stood in for the operator, so a missing identity keeps its how-to
}

// noIdentityHowTo is the one how-to a missing identity keeps when no terminal could ask for it.
const noIdentityHowTo = "`git config --global user.name` and `user.email`, or `proveo init` from a terminal"

func applyGitIdentity(s gitStage) {
	if _, err := s.lookPath("git"); err != nil {
		s.out.Item(sandbox.Item{Warn: true, Text: "git — not on PATH"})
		return
	}
	id := s.resolve()
	if id.Complete() {
		s.out.Fact(ui.Fact{Role: ui.RoleHost, Key: "identity", Value: id.Name + " <" + id.Email + ">"})
		return
	}
	if !s.tty {
		missing := sandbox.Item{Warn: true, Text: "git identity — not set"}
		if s.noTerminal {
			missing.Detail = []string{noIdentityHowTo}
		}
		s.out.Item(missing)
		return
	}
	name, email := id.Name, id.Email
	if name == "" && s.prompt != nil {
		name = s.prompt("git user.name")
	}
	if email == "" && s.prompt != nil {
		email = s.prompt("git user.email")
	}
	if name == "" || email == "" {
		s.out.Item(sandbox.Item{Warn: true, Text: "git identity — not set"})
		return
	}
	if s.setGlobal == nil {
		return
	}
	if err := s.setGlobal(name, email); err != nil {
		s.out.Item(sandbox.Item{Warn: true, Text: "git identity — not written", Detail: errLines(err)})
		return
	}
	s.out.Fact(ui.Fact{Role: ui.RoleHost, Key: "identity", Value: name + " <" + email + ">", Detail: []string{"written to git's global config"}})
}

func setGitConfigGlobal(name, email string) error {
	if name != "" {
		c := exec.Command("git", "config", "--global", "user.name", name)
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("git config --global user.name: %w", err)
		}
	}
	if email != "" {
		c := exec.Command("git", "config", "--global", "user.email", email)
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("git config --global user.email: %w", err)
		}
	}
	return nil
}

// hostGitStage is the git step against this host; tty is whether init may ask, which --yes takes away.
func hostGitStage(tty bool, out *sandbox.Block, noTerminal bool) gitStage {
	return gitStage{
		resolve:   func() gitidentity.Identity { return gitidentity.Resolve(os.Getenv, nil) },
		lookPath:  exec.LookPath,
		setGlobal: setGitConfigGlobal,
		prompt: func(label string) string {
			return promptLine(label+":", os.Stdin, os.Stderr)
		},
		tty:        tty,
		out:        out,
		noTerminal: noTerminal,
	}
}
