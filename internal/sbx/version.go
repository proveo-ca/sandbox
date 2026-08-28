package sbx

import (
	"fmt"
	"strconv"
	"strings"
)

func Available() (bool, string) {
	if _, err := lookPath(Binary); err != nil {
		return false, fmt.Sprintf("%s CLI not found on PATH", Binary)
	}
	switch {
	case goos == "darwin" && goarch == "arm64":
	case goos == "linux":
		if _, err := stat(kvmDevice); err != nil {
			return false, fmt.Sprintf("linux requires KVM (%s unavailable)", kvmDevice)
		}
	default:
		return false, fmt.Sprintf("unsupported platform %s/%s (want darwin/arm64 or linux+KVM)", goos, goarch)
	}
	got, err := Version()
	if err != nil {
		return false, fmt.Sprintf("%s version unreadable: %v", Binary, err)
	}
	if Older(got, MinVersion) {
		return false, fmt.Sprintf("%s %s is older than the %s this build targets", Binary, got, MinVersion)
	}
	return true, ""
}

// verLine matches the CLI's own report: "sbx version: v0.39.0 <sha>".
func Version() (string, error) {
	out, err := sh.Version()
	if err != nil {
		return "", err
	}
	m := verLine.FindStringSubmatch(string(out))
	if m == nil {
		return "", fmt.Errorf("no version in %q", strings.TrimSpace(string(out)))
	}
	return m[1] + "." + m[2] + "." + m[3], nil
}

// Older reports whether got precedes want. An unparseable side is treated as
// NOT older, so a version scheme this build has never seen is assumed newer
// rather than blocking a host the operator just upgraded.
func Older(got, want string) bool {
	g, gok := parseVer(got)
	w, wok := parseVer(want)
	if !gok || !wok {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return g[i] < w[i]
		}
	}
	return false
}

func parseVer(s string) ([3]int, bool) {
	m := verLine.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InstallCmd is the shell command that brings sbx to MinVersion or newer:
// installing it when absent, upgrading it when present but too old. Empty on a
// platform with no supported install route.
func InstallCmd(installed bool) string {
	switch goos {
	case "darwin":
		if goarch != "arm64" {
			return ""
		}
		if installed {
			return "brew update && brew upgrade docker/tap/sbx"
		}
		return "brew trust docker/tap && brew install docker/tap/sbx"
	case "windows":
		if installed {
			return "winget upgrade -h Docker.sbx"
		}
		return "winget install -h Docker.sbx"
	case "linux":
		if installed {
			return "sudo apt update && sudo apt install --only-upgrade docker-sbx"
		}
		return "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx"
	default:
		return ""
	}
}

// Installed reports whether the CLI is on PATH at all, which is what decides
// between an install and an upgrade.
func Installed() bool {
	_, err := lookPath(Binary)
	return err == nil
}

// InstallHint is the platform's install line for the sbx CLI.
func InstallHint() string {
	switch goos {
	case "darwin":
		if goarch != "arm64" {
			return ""
		}
		return "brew trust docker/tap && brew install docker/tap/sbx && sbx login"
	case "windows":
		return "winget install -h Docker.sbx && sbx login"
	case "linux":
		return "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx && sbx login"
	default:
		return ""
	}
}

// TemplateLoadArgs builds the argv that reads a `docker save` TAR into the
// sandbox runtime's own image store. The file is appended by the caller.
