package main

import (
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
		got, err := g.resolve()
		if err != nil {
			t.Errorf("%s set: resolve error %v", key, err)
		}
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
	got, err := g.resolve()
	if err != nil {
		t.Errorf("resolve error %v", err)
	}
	if got != "keyring-token" {
		t.Errorf("token = %q, want the host gh session's token", got)
	}
	if *logins != 0 {
		t.Error("a working gh session must not trigger a login prompt")
	}
}

func TestGitHubTokenNeverLogsInOnARun(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, true, "", errors.New("not logged in"), nil)
	got, _ := g.resolve()
	if got != "" {
		t.Errorf("token = %q, want empty", got)
	}
	if *logins != 0 {
		t.Error("a run must never shell out to `gh auth login` — that is proveo init's")
	}
}

func TestGitHubTokenLockedKeychainDoesNotOfferLogin(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, true, "", errCredentialStoreTimeout, nil)
	got, err := g.resolve()
	if got != "" {
		t.Errorf("token = %q, want empty when the credential store times out", got)
	}
	if !errors.Is(err, errCredentialStoreTimeout) {
		t.Errorf("err = %v, want the timeout so init does not offer login either", err)
	}
	if *logins != 0 {
		t.Error("a timed-out credential store must not trigger `gh auth login`")
	}
}

func TestGitHubTokenSkippedWithoutGh(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, false, "", errors.New("nope"), nil)
	got, err := g.resolve()
	if got != "" {
		t.Errorf("token = %q, want empty when gh is not installed", got)
	}
	if err == nil {
		t.Error("missing gh must surface as an error so init can install")
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
			return ""
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
	got := resolveGitHubTokenEnv(g)
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
	if got := resolveGitHubTokenEnv(g); got != "" {
		t.Errorf("env entry = %q, want none when PROVEO_MOUNT_GH_CONFIG=0", got)
	}
}

func TestGitHubTokenRunWarnsWithoutOpeningLogin(t *testing.T) {
	g, logins := stubAuth(nil, true, "", errors.New("not logged in"), nil)
	dir := t.TempDir()
	base := g.getenv
	g.getenv = func(k string) string {
		if k == "GH_CONFIG_DIR" {
			return dir
		}
		return base(k)
	}
	if got := resolveGitHubTokenEnv(*g); got != "" {
		t.Errorf("env entry = %q, want empty", got)
	}
	if *logins != 0 {
		t.Error("resolveGitHubTokenEnv must not log in")
	}
}
