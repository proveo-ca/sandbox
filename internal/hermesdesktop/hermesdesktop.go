// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (INSTALL)
package hermesdesktop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// The Hermes release proveo installs the desktop app from: its commit is the integrity anchor.
const (
	Tag                 = "v0.22.5"
	Commit              = "bf0203a794a07aac3ff764972ab9087baa4e4f4e"
	InstallScriptSHA256 = "99cb3691d55019c6973a7acb775a915648666cc24e483ca827a1cdbb3531a674"
)

// InstallScriptURL is upstream's install.sh at the pinned commit.
func InstallScriptURL() string {
	return "https://raw.githubusercontent.com/NousResearch/hermes-agent/" + Commit + "/scripts/install.sh"
}

// Plan is how one host gets the pinned Hermes Desktop.
type Plan struct {
	Prereqs     []string // distro packages the desktop build needs
	PrereqArgv  []string // the package-manager install; nil when the host needs none
	Script      string   // where the verified install.sh is written
	InstallArgv []string
	LaunchArgv  []string // opens the built app
	ReleaseDir  string   // the built app lands here
}

// Package sets per package manager, as measured in the plan's LINUX PROBES.
var (
	aptPrereqs = []string{"curl", "git", "ca-certificates", "xz-utils", "libatomic1", "build-essential", "python3"}
	dnfPrereqs = []string{"curl", "git", "ca-certificates", "xz", "libatomic", "make", "gcc-c++", "python3"}
)

// PlanFor resolves the pinned install for host h, user home and download directory.
func PlanFor(h sbx.Host, home, download string) (Plan, error) {
	if strings.TrimSpace(home) == "" {
		return Plan{}, fmt.Errorf("hermes desktop: no home directory to install into")
	}
	p := Plan{
		Script:     filepath.Join(download, "hermes-install-"+Commit[:12]+".sh"),
		LaunchArgv: []string{filepath.Join(home, ".local", "bin", "hermes"), "desktop", "--skip-build"},
		ReleaseDir: ReleaseDir(home),
	}
	p.InstallArgv = []string{"bash", p.Script, "--commit", Commit, "--include-desktop",
		"--non-interactive", "--skip-browser", "--skip-computer-use"}
	switch h.OS {
	case "darwin":
		if h.Arch != "arm64" {
			return Plan{}, fmt.Errorf("hermes desktop: no build for darwin/%s — it needs Apple silicon", h.Arch)
		}
	case "linux":
		switch family(h) {
		case "apt":
			p.Prereqs = aptPrereqs
			p.PrereqArgv = append([]string{"sudo", "apt-get", "install", "-y"}, aptPrereqs...)
		case "dnf":
			p.Prereqs = dnfPrereqs
			p.PrereqArgv = append([]string{"sudo", "dnf", "install", "-y"}, dnfPrereqs...)
		default:
			return Plan{}, fmt.Errorf("hermes desktop: %s is not a supported Linux (want Ubuntu, Debian or Fedora)", cmpOr(h.Distro, "this distribution"))
		}
	default:
		return Plan{}, fmt.Errorf("hermes desktop: unsupported platform %s/%s (want macOS on Apple silicon, or Ubuntu, Debian or Fedora)", h.OS, h.Arch)
	}
	return p, nil
}

func family(h sbx.Host) string {
	ids := append([]string{h.Distro}, strings.Fields(h.Like)...)
	switch {
	case slices.ContainsFunc(ids, func(id string) bool { return id == "ubuntu" || id == "debian" }):
		return "apt"
	case slices.ContainsFunc(ids, func(id string) bool { return id == "fedora" }):
		return "dnf"
	}
	return ""
}

func cmpOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// ReleaseDir is where install.sh's desktop build leaves the app.
func ReleaseDir(home string) string {
	return filepath.Join(home, ".hermes", "hermes-agent", "apps", "desktop", "release")
}

// Installed reports whether a built desktop app is present under home.
func Installed(home string) bool {
	entries, err := os.ReadDir(ReleaseDir(home))
	if err != nil {
		return false
	}
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.IsDir() })
}

// VerifyScript checks install.sh bytes against the pinned digest.
func VerifyScript(b []byte) error {
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != InstallScriptSHA256 {
		return fmt.Errorf("hermes desktop: install.sh sha256 %s, want %s (%s at %s)", got, InstallScriptSHA256, Tag, Commit[:12])
	}
	return nil
}

// Install fetches and verifies install.sh, installs the prerequisites, then runs it.
func Install(p Plan, fetch func(url string) ([]byte, error), run func(argv []string) error) error {
	b, err := fetch(InstallScriptURL())
	if err != nil {
		return fmt.Errorf("hermes desktop: fetch install.sh: %w", err)
	}
	if err := VerifyScript(b); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.Script), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p.Script, b, 0o600); err != nil {
		return err
	}
	if len(p.PrereqArgv) > 0 {
		if err := run(p.PrereqArgv); err != nil {
			return fmt.Errorf("hermes desktop: prerequisites (%s): %w", strings.Join(p.Prereqs, " "), err)
		}
	}
	if err := run(p.InstallArgv); err != nil {
		return fmt.Errorf("hermes desktop: install.sh: %w", err)
	}
	return nil
}
