package main

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func stubAuth(env map[string]string, ghPresent bool, tok string, tokErr error, login func() error) (*ghAuth, *int) {
	logins := 0
	g := ghAuth{
		getenv: func(k string) string { return env[k] },
		lookPath: func(string) (string, error) {
			if ghPresent {
				return "/usr/bin/gh", nil
			}
			return "", exec.ErrNotFound
		},
		token: func() (string, error) { return tok, tokErr },
		login: func() error {
			logins++
			if login == nil {
				return nil
			}
			return login()
		},
	}
	return &g, &logins
}

func TestGitHubTokenPrefersExplicitEnv(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		g, logins := stubAuth(map[string]string{key: "from-env"}, true, "from-gh", nil, nil)
		got := g.resolve(true, strings.NewReader(""), &bytes.Buffer{})
		if got != "from-env" {
			t.Errorf("%s set: token = %q, want the env value to win over gh", key, got)
		}
		if *logins != 0 {
			t.Errorf("%s set: must never prompt for a login", key)
		}
	}
}

func TestGitHubTokenComesFromHostGhSession(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, true, "keyring-token", nil, nil)
	out := &bytes.Buffer{}
	if got := g.resolve(true, strings.NewReader(""), out); got != "keyring-token" {
		t.Errorf("token = %q, want the host gh session's token", got)
	}
	if *logins != 0 {
		t.Error("a working gh session must not trigger a login prompt")
	}
	if out.Len() != 0 {
		t.Errorf("nothing should be asked when gh is already authenticated, got %q", out)
	}
}

func TestGitHubTokenNeverPromptsWhenNonInteractive(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, true, "", errors.New("not logged in"), nil)
	out := &bytes.Buffer{}
	if got := g.resolve(false, strings.NewReader("y\n"), out); got != "" {
		t.Errorf("token = %q, want empty", got)
	}
	if *logins != 0 {
		t.Error("a headless run must never shell out to an interactive login")
	}
	if out.Len() != 0 {
		t.Errorf("a headless run must not write a prompt, got %q", out)
	}
}

func TestGitHubTokenPromptsAndReReadsAfterLogin(t *testing.T) {
	t.Parallel()
	calls := 0
	g := ghAuth{
		getenv:   func(string) string { return "" },
		lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
		token: func() (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("not logged in")
			}
			return "fresh-token", nil
		},
		login: func() error { return nil },
	}
	out := &bytes.Buffer{}
	got := g.resolve(true, strings.NewReader("y\n"), out)
	if got != "fresh-token" {
		t.Errorf("token = %q, want the token stored by the login", got)
	}
	if !strings.Contains(out.String(), "gh auth login") {
		t.Errorf("the prompt must name the command being offered, got %q", out)
	}
}

func TestGitHubTokenDecliningIsNotFatal(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, true, "", errors.New("not logged in"), nil)
	if got := g.resolve(true, strings.NewReader("n\n"), &bytes.Buffer{}); got != "" {
		t.Errorf("token = %q, want empty after declining", got)
	}
	if *logins != 0 {
		t.Error("declining must not run the login")
	}
}

func TestGitHubTokenFailedLoginIsNotFatal(t *testing.T) {
	t.Parallel()
	g, _ := stubAuth(nil, true, "", errors.New("no session"), func() error {
		return errors.New("user aborted")
	})
	if got := g.resolve(true, strings.NewReader("y\n"), &bytes.Buffer{}); got != "" {
		t.Errorf("token = %q, want empty when the login fails", got)
	}
}

func TestGitHubTokenLockedKeychainDoesNotOfferLogin(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, true, "", errCredentialStoreTimeout, nil)
	out := &bytes.Buffer{}
	if got := g.resolve(true, strings.NewReader("y\n"), out); got != "" {
		t.Errorf("token = %q, want empty when the credential store times out", got)
	}
	if *logins != 0 {
		t.Error("a timed-out credential store must not trigger `gh auth login`")
	}
	if out.Len() != 0 {
		t.Errorf("no prompt should be written for a timeout, got %q", out)
	}
}

func TestGitHubTokenSkippedWithoutGh(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, false, "", errors.New("nope"), nil)
	if got := g.resolve(true, strings.NewReader("y\n"), &bytes.Buffer{}); got != "" {
		t.Errorf("token = %q, want empty when gh is not installed", got)
	}
	if *logins != 0 {
		t.Error("cannot offer a login when gh is absent")
	}
}

func TestGitHubTokenIsForwardedByBareNameOnly(t *testing.T) {
	g := ghAuth{
		getenv: func(k string) string {
			if k == "GITHUB_TOKEN" {
				return "s3cret"
			}
			return "" // PROVEO_MOUNT_GH_CONFIG unset, HOME below
		},
		lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
		token:    func() (string, error) { return "", nil },
		login:    func() error { return nil },
	}
	dir := t.TempDir()
	base := g.getenv
	g.getenv = func(k string) string {
		if k == "GH_CONFIG_DIR" {
			return dir
		}
		return base(k)
	}
	got := resolveGitHubTokenEnv(g, false, strings.NewReader(""), &bytes.Buffer{})
	if got != ghTokenEnvVar {
		t.Fatalf("env entry = %q, want the bare %q", got, ghTokenEnvVar)
	}
	if strings.Contains(got, "=") || strings.Contains(got, "s3cret") {
		t.Errorf("env entry %q leaks the secret onto the docker argv", got)
	}
}

func TestGitHubTokenHonorsTheGhConfigOptOut(t *testing.T) {
	g := ghAuth{
		getenv: func(k string) string {
			switch k {
			case "PROVEO_MOUNT_GH_CONFIG":
				return "0"
			case "GITHUB_TOKEN":
				return "s3cret"
			}
			return ""
		},
		lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
		token:    func() (string, error) { return "tok", nil },
		login:    func() error { return nil },
	}
	if got := resolveGitHubTokenEnv(g, false, strings.NewReader(""), &bytes.Buffer{}); got != "" {
		t.Errorf("env entry = %q, want none when PROVEO_MOUNT_GH_CONFIG=0", got)
	}
}
