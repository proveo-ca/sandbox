// SPEC: _spec/cmd/proveo/github-credentials.puml
package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/ui"
)

const ghTokenEnvVar = "GH_TOKEN"

const ghTokenTimeout = 5 * time.Second

var errCredentialStoreTimeout = errors.New("gh credential store timed out")

type ghAuth struct {
	getenv   func(string) string
	lookPath func(string) (string, error)
	token    func() (string, error)
	login    func() error
}

func hostGhAuth() ghAuth {
	return ghAuth{
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		token: func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), ghTokenTimeout)
			defer cancel()
			out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", errCredentialStoreTimeout
			}
			return strings.TrimSpace(string(out)), err
		},
		login: func() error {
			c := exec.Command("gh", "auth", "login")
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
			return c.Run()
		},
	}
}

func (g ghAuth) resolve(interactive bool, in io.Reader, out io.Writer) string {
	for _, k := range []string{"GITHUB_TOKEN", ghTokenEnvVar} {
		if v := strings.TrimSpace(g.getenv(k)); v != "" {
			return v
		}
	}
	if _, err := g.lookPath("gh"); err != nil {
		return ""
	}
	tok, err := g.token()
	switch {
	case err == nil && tok != "":
		return tok
	case errors.Is(err, errCredentialStoreTimeout):
		ui.Warnf("gh did not return a token within %s — on macOS a locked login keychain "+
			"blocks this. Unlock it or set %s; continuing with anonymous GitHub API limits.",
			ghTokenTimeout, ghTokenEnvVar)
		return ""
	}
	if !interactive {
		ui.Warnf("no GitHub credentials found — container tooling that reads the GitHub API " +
			"will use the anonymous 60-requests/hour limit. Run `gh auth login` to fix.")
		return ""
	}
	if !promptYesNo("GitHub credentials missing. Run `gh auth login` now?", true, in, out) {
		ui.Warnf("continuing without GitHub credentials (anonymous API limits apply)")
		return ""
	}
	if err := g.login(); err != nil {
		ui.Warnf("gh auth login did not complete: %v", err)
		return ""
	}
	tok, err = g.token()
	if err != nil || tok == "" {
		ui.Warnf("gh auth login finished but no token could be read back")
		return ""
	}
	ui.Okf("GitHub credentials stored by gh and forwarded to the container")
	return tok
}

func resolveGitHubTokenEnv(g ghAuth, interactive bool, in io.Reader, out io.Writer) string {
	if _, ok := ghConfigMount(g.getenv); !ok {
		return ""
	}
	tok := g.resolve(interactive, in, out)
	if tok == "" {
		return ""
	}
	if err := os.Setenv(ghTokenEnvVar, tok); err != nil {
		ui.Warnf("could not stage %s for the container: %v", ghTokenEnvVar, err)
		return ""
	}
	return ghTokenEnvVar
}
