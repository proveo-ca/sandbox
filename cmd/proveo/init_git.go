// SPEC: _spec/_paradigms/git-identity.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/ui"
)

// gitStage is `proveo init`'s git-identity step. Seed still bridges the
// resolved pair into GIT_CONFIG_KEY_n; init is the one place that can ASK.
type gitStage struct {
	resolve   func() gitidentity.Identity
	lookPath  func(string) (string, error)
	setGlobal func(name, email string) error
	prompt    func(label string) string
	tty       bool
}

func applyGitIdentity(s gitStage) {
	ui.Section(ui.SectionWorkspace)
	if _, err := s.lookPath("git"); err != nil {
		ui.Warnf("git is not on PATH — a sandbox cannot commit until this host has git")
		return
	}
	id := s.resolve()
	if id.Complete() {
		ui.Okf("git identity %s <%s>", id.Name, id.Email)
		return
	}
	if !s.tty {
		ui.Warnf("no git identity (user.name / user.email) — a commit inside a sandbox fails late. " +
			"Set them with `git config --global`, or re-run `proveo init` from a terminal")
		return
	}
	ui.Hostf("this host has no complete git identity — commits inside a sandbox fail with 'Please tell me who you are'")
	name, email := id.Name, id.Email
	if name == "" && s.prompt != nil {
		name = s.prompt("git user.name")
	}
	if email == "" && s.prompt != nil {
		email = s.prompt("git user.email")
	}
	if name == "" || email == "" {
		ui.Warnf("git identity still incomplete — set `git config --global user.name` and `user.email`")
		return
	}
	if s.setGlobal == nil {
		return
	}
	if err := s.setGlobal(name, email); err != nil {
		ui.Warnf("could not write git config (%v)", err)
		return
	}
	ui.Okf("git identity %s <%s> written to --global", name, email)
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

func hostGitStage() gitStage {
	return gitStage{
		resolve:   func() gitidentity.Identity { return gitidentity.Resolve(os.Getenv, nil) },
		lookPath:  exec.LookPath,
		setGlobal: setGitConfigGlobal,
		prompt: func(label string) string {
			return promptLine(label+":", os.Stdin, os.Stderr)
		},
		tty: interactiveTerminal(),
	}
}
