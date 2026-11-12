// SPEC: _spec/cmd/proveo/github-credentials.puml
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/credentials"
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
		login: func() error { return handOff(command("gh", "auth", "login")) },
	}
}

// lookup reads a GitHub token from env or the host gh session, and prints nothing; a gh that
// gives none in time is errCredentialStoreTimeout. It never opens `gh auth login`.
func (g ghAuth) lookup() (string, error) {
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
		return "", errCredentialStoreTimeout
	}
	return "", err
}

// resolveGitHubTokenEnv stages the host's GitHub token for the sandbox and names the variable;
// items are the credentials block's warnings when the agent's GitHub calls go anonymous.
func resolveGitHubTokenEnv(g ghAuth) (string, []sandbox.Item) {
	if _, ok := credentials.GhConfigMount(g.getenv); !ok {
		return "", nil
	}
	tok, err := g.lookup()
	switch {
	case errors.Is(err, errCredentialStoreTimeout):
		it := ghTimeoutItem(githubService)
		it.Log = []string{"on macOS a locked login keychain blocks `gh auth token`; the agent's GitHub API calls are anonymous (60 an hour)"}
		return "", []sandbox.Item{it}
	case tok == "":
		return "", []sandbox.Item{{Warn: true, Text: "github — no token",
			Detail: []string{"the agent's GitHub API calls are anonymous: 60 an hour", "`proveo init` signs in"}}}
	}
	if err := os.Setenv(ghTokenEnvVar, tok); err != nil {
		return "", []sandbox.Item{{Warn: true, Text: "github — token not staged", Detail: []string{err.Error()}}}
	}
	return ghTokenEnvVar, nil
}

// ghTimeoutItem is subject's warning for a gh that gave no token in time, with its fix dim.
func ghTimeoutItem(subject string) sandbox.Item {
	return sandbox.Item{Warn: true, Text: fmt.Sprintf("%s — gh gave no token within %s", subject, ghTokenTimeout),
		Detail: []string{"unlock the login keychain, or set " + ghTokenEnvVar}}
}
