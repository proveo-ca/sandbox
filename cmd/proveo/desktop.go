// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (INSTALL)
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// ensureDesktop installs the pinned Hermes Desktop when it is missing and returns its launch argv.
func ensureDesktop() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("hermes desktop: %w", err)
	}
	if hermesdesktop.Installed(home) {
		return hermesdesktop.LaunchArgv(home), nil
	}
	download, err := os.MkdirTemp("", "proveo-hermes-desktop-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(download) }()
	plan, err := hermesdesktop.PlanFor(sbx.DetectHost(), home, download)
	if err != nil {
		return nil, err
	}
	ui.Hostf("Hermes Desktop is not installed. The plan (Hermes %s, commit %s):", hermesdesktop.Tag, hermesdesktop.Commit[:12])
	if len(plan.PrereqArgv) > 0 {
		ui.Notef("  %s", strings.Join(plan.PrereqArgv, " "))
	}
	ui.Notef("  fetch %s (sha256 %s…)", hermesdesktop.InstallScriptURL(), hermesdesktop.InstallScriptSHA256[:16])
	ui.Notef("  %s", strings.Join(plan.InstallArgv, " "))
	ui.Notef("  it builds the app from source into %s and takes minutes", plan.ReleaseDir)
	if !provisionConfirm("Install Hermes Desktop now?") {
		return nil, fmt.Errorf("hermes desktop is not installed — pick the tui instead, or rerun and accept the install (PROVEO_AUTO_PROVISION=1)")
	}
	if err := hermesdesktop.Install(plan, fetchHTTP, runInheriting); err != nil {
		return nil, err
	}
	if !hermesdesktop.Installed(home) {
		return nil, fmt.Errorf("hermes desktop: install.sh finished but no app is under %s", plan.ReleaseDir)
	}
	return plan.LaunchArgv, nil
}

func fetchHTTP(url string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func runInheriting(argv []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
	return c.Run()
}
