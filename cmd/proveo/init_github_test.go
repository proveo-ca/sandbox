package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestGhInstallArgv(t *testing.T) {
	t.Parallel()
	has := func(names ...string) func(string) (string, error) {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		return func(name string) (string, error) {
			if set[name] {
				return "/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		}
	}
	for _, tc := range []struct {
		name string
		host sbx.Host
		bins []string
		want []string
		why  string
	}{
		{name: "darwin brew", host: sbx.Host{OS: "darwin"}, bins: []string{"brew"}, want: []string{"brew", "install", "gh"}, why: "Homebrew"},
		{name: "darwin no brew", host: sbx.Host{OS: "darwin"}, why: ghInstallDocs},
		{name: "windows winget", host: sbx.Host{OS: "windows"}, bins: []string{"winget"},
			want: []string{"winget", "install", "--id", "GitHub.cli", "-e", "--accept-package-agreements", "--accept-source-agreements"}, why: "WinGet"},
		{name: "ubuntu apt", host: sbx.Host{OS: "linux", Distro: "ubuntu", Like: "debian"}, bins: []string{"apt-get"},
			want: []string{"sudo", "apt-get", "install", "-y", "gh"}, why: "apt"},
		{name: "rocky dnf", host: sbx.Host{OS: "linux", Distro: "rocky", Like: "rhel"}, bins: []string{"dnf"},
			want: []string{"sudo", "dnf", "install", "-y", "gh"}, why: "dnf"},
		{name: "linux no pkg", host: sbx.Host{OS: "linux", Distro: "void"}, why: ghInstallDocs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv, why := ghInstallArgv(tc.host, has(tc.bins...))
			if diff := cmp.Diff(tc.want, argv); diff != "" {
				t.Errorf("argv mismatch (-want +got):\n%s", diff)
			}
			if tc.why != "" && !strings.Contains(why, tc.why) {
				t.Errorf("why = %q, want it to name %q", why, tc.why)
			}
		})
	}
}

func TestInitGitHubStoresTheBuiltinService(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(map[string]string{"GH_TOKEN": "tok"}, true, "from-gh", nil, nil)
	var name, value string
	applyGitHub(ghStage{
		auth:   *g,
		setenv: func(string, string) error { return nil },
		secretSet: func(n, v string) error {
			name, value = n, v
			return nil
		},
	})
	if *logins != 0 {
		t.Error("an env token must not trigger a login")
	}
	if name != githubService {
		t.Errorf("stored as %q, want builtin %q so the proxy injects it", name, githubService)
	}
	if !sbx.IsBuiltinService(name) {
		t.Errorf("%q is not an sbx builtin service", name)
	}
	if value != "tok" {
		t.Errorf("value = %q, want the env token", value)
	}
}

func TestInitGitHubLogsInWhenGhIsPresentButUnauthed(t *testing.T) {
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
	var stored string
	applyGitHub(ghStage{
		auth:   g,
		tty:    true,
		setenv: func(string, string) error { return nil },
		secretSet: func(name, value string) error {
			stored = value
			return nil
		},
	})
	if stored != "fresh-token" {
		t.Errorf("stored %q, want the token login just wrote", stored)
	}
}

func TestInitGitHubDoesNotLoginOnALockedKeychain(t *testing.T) {
	t.Parallel()
	g, logins := stubAuth(nil, true, "", errCredentialStoreTimeout, nil)
	applyGitHub(ghStage{auth: *g, tty: true, setenv: func(string, string) error { return nil }, secretSet: func(string, string) error {
		t.Error("must not store a token that was never read")
		return nil
	}})
	if *logins != 0 {
		t.Error("a timed-out credential store must not trigger `gh auth login`")
	}
}

func TestInitGitHubInstallsThenLogsIn(t *testing.T) {
	t.Parallel()
	installed := false
	var ran []string
	logins := 0
	g := ghAuth{
		getenv: func(string) string { return "" },
		lookPath: func(name string) (string, error) {
			if name == "gh" && installed {
				return "/opt/homebrew/bin/gh", nil
			}
			if name == "brew" {
				return "/opt/homebrew/bin/brew", nil
			}
			return "", exec.ErrNotFound
		},
		token: func() (string, error) {
			if !installed {
				return "", exec.ErrNotFound
			}
			if logins == 0 {
				return "", errors.New("not logged in")
			}
			return "brewed-token", nil
		},
		login: func() error { logins++; return nil },
	}
	var stored string
	applyGitHub(ghStage{
		auth: g,
		host: sbx.Host{OS: "darwin", Arch: "arm64"},
		tty:  true,
		run: func(argv []string) error {
			ran = argv
			installed = true
			return nil
		},
		secretSet: func(_, value string) error { stored = value; return nil },
		setenv:    func(string, string) error { return nil },
	})
	if diff := cmp.Diff([]string{"brew", "install", "gh"}, ran); diff != "" {
		t.Errorf("install argv mismatch (-want +got):\n%s", diff)
	}
	if logins != 1 {
		t.Errorf("logins = %d, want one after the install", logins)
	}
	if stored != "brewed-token" {
		t.Errorf("stored %q, want the token after brew + login", stored)
	}
}

func TestInitGitHubHeadlessDoesNotInstallOrLogin(t *testing.T) {
	t.Parallel()
	g := ghAuth{
		getenv: func(string) string { return "" },
		lookPath: func(name string) (string, error) {
			if name == "brew" {
				return "/opt/homebrew/bin/brew", nil
			}
			return "", exec.ErrNotFound
		},
		token: func() (string, error) { return "", exec.ErrNotFound },
		login: func() error {
			t.Error("must not log in without a terminal")
			return nil
		},
	}
	applyGitHub(ghStage{
		auth: g,
		host: sbx.Host{OS: "darwin"},
		tty:  false,
		run: func([]string) error {
			t.Error("must not install without a terminal")
			return nil
		},
		secretSet: func(string, string) error {
			t.Error("must not store without a token")
			return nil
		},
		setenv: func(string, string) error { return nil },
	})
}

func TestInitGitHubFailedLoginIsNotFatal(t *testing.T) {
	t.Parallel()
	g, _ := stubAuth(nil, true, "", errors.New("no session"), func() error {
		return errors.New("user aborted")
	})
	applyGitHub(ghStage{auth: *g, tty: true, setenv: func(string, string) error { return nil }}) // must not panic
}
