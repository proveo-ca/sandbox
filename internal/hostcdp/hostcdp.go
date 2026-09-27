// SPEC: _spec/internal/sbx/host-browser-cdp.puml
package hostcdp

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	Addon       = "host chrome (CDP)"
	EnvPort     = "PROVEO_HOST_CDP_PORT"
	DefaultPort = 9222
)

var (
	hostURL   = func(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }
	readyWait = 20 * time.Second
	configDir = os.UserConfigDir
	lookPath  = exec.LookPath
	statFile  = os.Stat
)

// Port is the host CDP port: PROVEO_HOST_CDP_PORT, else 9222.
func Port(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv(EnvPort))
	if v == "" {
		return DefaultPort, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s=%q is not a TCP port", EnvPort, v)
	}
	return n, nil
}

// PolicyHost is the sbx allow rule for the host port.
func PolicyHost(port int) string { return fmt.Sprintf("localhost:%d", port) }

// ProfileDir is the dedicated Chrome profile for target, outside the mounted proveo home.
func ProfileDir(target string) (string, error) {
	base, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "proveo", "host-chrome", target), nil
}

// Ready reports whether a browser answers CDP discovery on the host port.
func Ready(port int) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(hostURL(port) + "/json/version")
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ChromeBinary finds a Chromium-family browser on the host.
func ChromeBinary() string {
	if runtime.GOOS == "darwin" {
		for _, app := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		} {
			if fi, err := statFile(app); err == nil && !fi.IsDir() {
				return app
			}
		}
		return ""
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "microsoft-edge"} {
		if p, err := lookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// LaunchArgs opens a CDP-enabled window on the dedicated profile.
func LaunchArgs(profile string, port int) []string {
	return []string{
		"--user-data-dir=" + profile,
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--no-first-run",
		"--no-default-browser-check",
	}
}

// Ensure reuses a CDP browser already on port, or launches one on target's dedicated profile.
func Ensure(target string, port int, report func(string, ...any)) error {
	if Ready(port) {
		if report != nil {
			report("host chrome: reusing the browser already serving CDP on 127.0.0.1:%d", port)
		}
		return nil
	}
	bin := ChromeBinary()
	if bin == "" {
		return fmt.Errorf("no Chrome, Chromium, Brave or Edge found on this host")
	}
	profile, err := ProfileDir(target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(bin, LaunchArgs(profile, port)...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", filepath.Base(bin), err)
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		if Ready(port) {
			if report != nil {
				report("host chrome: opened %s on profile %s (CDP 127.0.0.1:%d)", filepath.Base(bin), profile, port)
			}
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s did not answer CDP on 127.0.0.1:%d within %s — another Chrome on profile %s may hold it without the debugging port",
		filepath.Base(bin), port, readyWait, profile)
}
