// SPEC: _spec/cmd/proveo/github-credentials.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

const githubService = "github"

const ghInstallDocs = "https://cli.github.com/"

// ghStage is `proveo init`'s GitHub step: verify gh, install it if this host
// has a package manager proveo can drive, log in if needed, then store the
// token under sbx's builtin `github` service so the proxy can inject git HTTPS.
type ghStage struct {
	auth      ghAuth
	host      sbx.Host
	run       func(argv []string) error
	secretSet func(name, value string) error
	setenv    func(k, v string) error
	tty       bool
}

func applyGitHub(s ghStage) {
	ui.Section(ui.SectionCredentials)
	tok, err := s.auth.resolve()
	if tok == "" && !errors.Is(err, errCredentialStoreTimeout) {
		if _, lp := s.auth.lookPath("gh"); lp != nil {
			if !installGh(s) {
				return
			}
			tok, err = s.auth.resolve()
		}
		if tok == "" && !errors.Is(err, errCredentialStoreTimeout) {
			if !s.tty {
				ui.Warnf("no GitHub credentials — `gh auth login` needs a terminal. "+
					"Re-run `proveo init` from a shell, or set %s", ghTokenEnvVar)
				return
			}
			ui.Hostf("signing in to GitHub — `gh auth login` owns this; proveo stores the token "+
				"as sbx's %s service and never keeps a copy", githubService)
			if err := s.auth.login(); err != nil {
				ui.Warnf("gh auth login did not complete (%v) — git push to github.com and "+
					"authenticated API limits wait on it", err)
				return
			}
			tok, _ = s.auth.resolve()
			if tok == "" {
				ui.Warnf("gh auth login finished but no token could be read back")
				return
			}
			ui.Okf("GitHub credentials stored by gh")
		}
	}
	if tok == "" {
		return
	}
	setenv := s.setenv
	if setenv == nil {
		setenv = os.Setenv
	}
	if err := setenv(ghTokenEnvVar, tok); err != nil {
		ui.Warnf("could not stage %s: %v", ghTokenEnvVar, err)
	}
	if s.secretSet == nil {
		return
	}
	if err := s.secretSet(githubService, tok); err != nil {
		ui.Warnf("could not store %s in sbx's secret store (%v) — `gh` is logged in, but "+
			"git push through the proxy needs `sbx secret set %s`", githubService, err, githubService)
		return
	}
	ui.Storef("%s stored in sbx's host-wide secret store — git HTTPS and the GitHub API go through the proxy",
		githubService)
}

func installGh(s ghStage) bool {
	argv, why := ghInstallArgv(s.host, s.auth.lookPath)
	if len(argv) == 0 {
		ui.Warnf("GitHub CLI is not on PATH — %s", why)
		return false
	}
	if !s.tty {
		ui.Warnf("GitHub CLI is not on PATH — from a terminal: %s", strings.Join(argv, " "))
		return false
	}
	ui.Appf("installing GitHub CLI via %s: %s", why, strings.Join(argv, " "))
	if s.run == nil {
		return false
	}
	if err := s.run(argv); err != nil {
		ui.Warnf("install did not complete (%v) — see %s", err, ghInstallDocs)
		return false
	}
	if _, err := s.auth.lookPath("gh"); err != nil {
		ui.Warnf("install finished but `gh` is still not on PATH — open a new shell, or see %s", ghInstallDocs)
		return false
	}
	ui.Okf("gh is on PATH")
	return true
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
		return nil, "install it from " + ghInstallDocs + " (brew is not on PATH, and proveo cannot pin a Homebrew install)"
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

func runArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("no command")
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
	return c.Run()
}

func secretSetAt(bin, name, value string) error {
	c := exec.Command(bin, sbx.SecretSetArgs(name)...)
	c.Stdin = strings.NewReader(value)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	return c.Run()
}
