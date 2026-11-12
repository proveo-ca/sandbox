// SPEC: _spec/cmd/proveo/github-credentials.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

const githubService = "github"

const ghInstallDocs = "https://cli.github.com/"

// ghStage is `proveo init`'s GitHub step: verify gh, install it if this host
// has a package manager proveo can drive, log in if needed, then store the
// token under sbx's builtin `github` service so the proxy can inject git HTTPS.
type ghStage struct {
	auth       ghAuth
	host       sbx.Host
	run        func(argv []string) error
	secretSet  func(name, value string) error
	setenv     func(k, v string) error
	tty        bool
	access     func() error
	out        *sandbox.Block // the git block; nil prints each row at once
	noTerminal bool
}

// applyGitHub readies the GitHub token, then sandbox Git access. A `gh auth login` that failed, and
// a Ctrl+C that ended another program it handed the terminal to, are its error; everything else it
// says in the git block and goes on.
func applyGitHub(s ghStage) error {
	if err := githubToken(s); err != nil {
		return err
	}
	if s.access != nil {
		return s.access()
	}
	return nil
}

// githubToken finds the GitHub token, through gh's install and login when this host can run
// them, and stores it as sbx's github service.
func githubToken(s ghStage) error {
	tok, err := s.auth.lookup()
	if tok == "" && !errors.Is(err, errCredentialStoreTimeout) {
		if _, lp := s.auth.lookPath("gh"); lp != nil {
			if ok, err := installGh(s); !ok {
				return err
			}
			tok, err = s.auth.lookup()
		}
		if tok == "" && !errors.Is(err, errCredentialStoreTimeout) {
			if !s.tty {
				s.out.Item(sandbox.Item{Warn: true, Text: "GitHub — not signed in"})
				return nil
			}
			ui.Asyncf("gh auth login — signing in to GitHub")
			if err := s.auth.login(); err != nil {
				return err
			}
			if tok, err = s.auth.lookup(); tok == "" && !errors.Is(err, errCredentialStoreTimeout) {
				s.out.Item(sandbox.Item{Warn: true, Text: "GitHub — no token after gh auth login"})
				return nil
			}
		}
	}
	if errors.Is(err, errCredentialStoreTimeout) {
		s.out.Item(ghTimeoutItem(githubService))
		return nil
	}
	if tok == "" {
		return nil
	}
	setenv := s.setenv
	if setenv == nil {
		setenv = os.Setenv
	}
	if err := setenv(ghTokenEnvVar, tok); err != nil {
		s.out.Item(sandbox.Item{Warn: true, Text: ghTokenEnvVar + " — not staged", Detail: errLines(err)})
	}
	if s.secretSet == nil {
		return nil
	}
	if err := s.secretSet(githubService, tok); err != nil {
		if interrupted(err) {
			return err
		}
		s.out.Item(sandbox.Item{Warn: true, Text: githubService + " — not stored in sbx",
			Detail: append(errDetail(err), "`"+sbx.Binary+" secret set "+githubService+"` stores it")})
		return nil
	}
	s.out.Fact(ui.Fact{Role: ui.RoleStore, Key: githubService, Value: "token stored in sbx"})
	return nil
}

// installGh installs the GitHub CLI where this host's package manager can; ok is false when gh is
// still missing, and err is a Ctrl+C that ended the install.
func installGh(s ghStage) (ok bool, err error) {
	argv, why := ghInstallArgv(s.host, s.auth.lookPath)
	if len(argv) == 0 {
		s.out.Item(sandbox.Item{Warn: true, Text: "gh — not on PATH", Detail: []string{why}})
		return false, nil
	}
	if !s.tty {
		s.out.Item(sandbox.Item{Warn: true, Text: "gh — not on PATH"})
		return false, nil
	}
	if s.run == nil {
		return false, nil
	}
	ui.Asyncf("%s — installing the GitHub CLI", strings.Join(argv, " "))
	if err := s.run(argv); err != nil {
		if interrupted(err) {
			return false, err
		}
		s.out.Item(sandbox.Item{Warn: true, Text: "gh — not installed", Detail: append(errDetail(err), "install it from "+ghInstallDocs)})
		return false, nil
	}
	if _, err := s.auth.lookPath("gh"); err != nil {
		s.out.Item(sandbox.Item{Warn: true, Text: "gh — not on PATH after its install", Detail: []string{"a new shell may find it; see " + ghInstallDocs}})
		return false, nil
	}
	return true, nil
}

// ghInstallArgv is the host package manager's install, when proveo can see one.
// It is never a pinned tarball: pinning gh would be a second sbx, and brew /
// winget / apt / dnf are what the operator already trusts for CLI tools.
func ghInstallArgv(h sbx.Host, lookPath func(string) (string, error)) (argv []string, why string) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	has := func(name string) bool {
		_, err := lookPath(name)
		return err == nil
	}
	switch h.OS {
	case "darwin":
		if has("brew") {
			return []string{"brew", "install", "gh"}, "Homebrew"
		}
		return nil, "install it from " + ghInstallDocs
	case "windows":
		if has("winget") {
			return []string{"winget", "install", "--id", "GitHub.cli", "-e",
				"--accept-package-agreements", "--accept-source-agreements"}, "WinGet"
		}
		return nil, "install it from " + ghInstallDocs
	case "linux":
		if debianLike(h) && has("apt-get") {
			return []string{"sudo", "apt-get", "install", "-y", "gh"}, "apt"
		}
		if rhelLike(h) && has("dnf") {
			return []string{"sudo", "dnf", "install", "-y", "gh"}, "dnf"
		}
		return nil, "install it from " + ghInstallDocs
	}
	return nil, "install it from " + ghInstallDocs
}

func debianLike(h sbx.Host) bool {
	switch h.Distro {
	case "debian", "ubuntu":
		return true
	}
	return strings.Contains(h.Like, "debian")
}

func rhelLike(h sbx.Host) bool {
	switch h.Distro {
	case "rhel", "fedora", "rocky", "centos", "almalinux":
		return true
	}
	return strings.Contains(h.Like, "rhel") || strings.Contains(h.Like, "fedora")
}

// runArgv runs argv with the terminal, as a hand-off.
func runArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("no command")
	}
	return handOff(command(argv[0], argv[1:]...))
}

// secretSetAt stores value as sbx's name secret, through bin.
func secretSetAt(bin, name, value string) error {
	c := command(bin, sbx.SecretSetArgs(name)...)
	c.Stdin = strings.NewReader(value)
	return handOff(c)
}
