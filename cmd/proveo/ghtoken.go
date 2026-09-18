// SPEC: _spec/cmd/proveo/github-credentials.puml
package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/credentials"
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

// resolve reads a GitHub token from env or the host gh session. It never
// opens `gh auth login` — that is `proveo init`'s. A locked keychain is its
// own error so a run does not tell an already-logged-in operator to log in.
func (g ghAuth) resolve() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", ghTokenEnvVar} {
		if v := strings.TrimSpace(g.getenv(k)); v != "" {
			return v, nil
		}
	}
	if _, err := g.lookPath("gh"); err != nil {
		return "", err
	}
	tok, err := g.token()
	switch {
	case err == nil && tok != "":
		return tok, nil
	case errors.Is(err, errCredentialStoreTimeout):
		ui.Warnf("gh did not return a token within %s — on macOS a locked login keychain "+
			"blocks this. Unlock it or set %s; continuing with anonymous GitHub API limits.",
			ghTokenTimeout, ghTokenEnvVar)
		return "", errCredentialStoreTimeout
	}
	return "", err
}

func resolveGitHubTokenEnv(g ghAuth) string {
	if _, ok := credentials.GhConfigMount(g.getenv); !ok {
		return ""
	}
	tok, err := g.resolve()
	if tok == "" {
		if !errors.Is(err, errCredentialStoreTimeout) {
			ui.Warnf("no GitHub credentials found — container tooling that reads the GitHub API " +
				"will use the anonymous 60-requests/hour limit. Run `proveo init` (or `gh auth login`) to fix.")
		}
		return ""
	}
	if err := os.Setenv(ghTokenEnvVar, tok); err != nil {
		ui.Warnf("could not stage %s for the container: %v", ghTokenEnvVar, err)
		return ""
	}
	return ghTokenEnvVar
}
