// SPEC: _spec/internal/sbx/host-browser-cdp.puml
package hostcdp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func TestPortDefaultsAndValidates(t *testing.T) {
	if p, err := Port(env(nil)); err != nil || p != DefaultPort {
		t.Errorf("unset = %d, %v; want %d", p, err, DefaultPort)
	}
	if p, err := Port(env(map[string]string{EnvPort: " 9333 "})); err != nil || p != 9333 {
		t.Errorf("9333 = %d, %v", p, err)
	}
	for _, bad := range []string{"0", "70000", "chrome"} {
		if _, err := Port(env(map[string]string{EnvPort: bad})); err == nil {
			t.Errorf("%s=%q must be refused", EnvPort, bad)
		}
	}
}

func TestProfileDirStaysOutOfTheMountedProveoHome(t *testing.T) {
	orig := configDir
	t.Cleanup(func() { configDir = orig })
	base := t.TempDir()
	configDir = func() (string, error) { return base, nil }

	got, err := ProfileDir("hermes")
	if err != nil || got != filepath.Join(base, "proveo", "host-chrome", "hermes") {
		t.Errorf("ProfileDir = %q, %v", got, err)
	}
	if home, _ := os.UserHomeDir(); strings.HasPrefix(got, filepath.Join(home, ".proveo")) {
		t.Errorf("%q is under ~/.proveo, which sandboxes mount at /proveo-home — the cookies would be readable in the VM", got)
	}
}

func TestLaunchArgsUseADedicatedProfileAndPort(t *testing.T) {
	args := LaunchArgs("/p/hermes", 9333)
	for _, want := range []string{"--user-data-dir=/p/hermes", "--remote-debugging-port=9333"} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v missing %q", args, want)
		}
	}
}

func serveCDP(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"Browser":"Chrome/153"}`))
	}))
	t.Cleanup(srv.Close)
	orig := hostURL
	t.Cleanup(func() { hostURL = orig })
	hostURL = func(int) string { return srv.URL }
	return 1
}

func TestEnsureReusesARunningBrowser(t *testing.T) {
	serveCDP(t)
	origStat, origLook := statFile, lookPath
	t.Cleanup(func() { statFile, lookPath = origStat, origLook })
	statFile = func(string) (os.FileInfo, error) { t.Fatal("must not look for a browser to launch"); return nil, nil }
	lookPath = func(string) (string, error) { t.Fatal("must not look for a browser to launch"); return "", nil }

	var said string
	if err := Ensure("hermes", 9333, func(f string, a ...any) { said = f }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "reusing") {
		t.Errorf("report = %q, want the reuse line", said)
	}
}

func TestEnsureNamesTheMissingBrowser(t *testing.T) {
	orig := hostURL
	t.Cleanup(func() { hostURL = orig })
	hostURL = func(int) string { return "http://127.0.0.1:1" }
	origStat, origLook := statFile, lookPath
	t.Cleanup(func() { statFile, lookPath = origStat, origLook })
	statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	err := Ensure("hermes", 9333, nil)
	if err == nil || !strings.Contains(err.Error(), "no Chrome") {
		t.Errorf("want a named refusal, got %v", err)
	}
}

func TestReadyNeedsDiscovery(t *testing.T) {
	serveCDP(t)
	if !Ready(9333) {
		t.Error("200 on /json/version must count as ready")
	}
	hostURL = func(p int) string { return "http://127.0.0.1:" + strconv.Itoa(1) }
	if Ready(9333) {
		t.Error("nothing listening must not count as ready")
	}
}

func TestPolicyHostIsTheLocalhostForm(t *testing.T) {
	if got := PolicyHost(9222); got != "localhost:9222" {
		t.Errorf("PolicyHost = %q — the sbx proxy matches host.docker.internal:<port> as localhost:<port>", got)
	}
}
