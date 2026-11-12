// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (INSTALL)
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// ensureDesktop installs the pinned Hermes Desktop when it is missing and returns its launch argv;
// installed reports this call's install. The install plan goes to the run log.
func ensureDesktop() (launch []string, installed bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, fmt.Errorf("hermes desktop: %w", err)
	}
	if hermesdesktop.Installed(home) {
		return hermesdesktop.LaunchArgv(home), false, nil
	}
	download, err := os.MkdirTemp("", "proveo-hermes-desktop-")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = os.RemoveAll(download) }()
	plan, err := hermesdesktop.PlanFor(sbx.DetectHost(), home, download)
	if err != nil {
		return nil, false, err
	}
	ui.Logf("Hermes Desktop is not installed. The plan (Hermes %s, commit %s):", hermesdesktop.Tag, hermesdesktop.Commit[:12])
	if len(plan.PrereqArgv) > 0 {
		ui.Logf("  %s", strings.Join(plan.PrereqArgv, " "))
	}
	ui.Logf("  fetch %s (sha256 %s…)", hermesdesktop.InstallScriptURL(), hermesdesktop.InstallScriptSHA256[:16])
	ui.Logf("  %s", strings.Join(plan.InstallArgv, " "))
	ui.Logf("  it builds the app from source into %s and takes minutes", plan.ReleaseDir)
	if !provisionConfirm("Install Hermes Desktop " + hermesdesktop.Tag + " now?") {
		return nil, false, fmt.Errorf("hermes desktop is not installed — pick the tui instead, or rerun and accept the install (PROVEO_AUTO_PROVISION=1)")
	}
	if err := hermesdesktop.Install(plan, fetchHTTP, desktopInstallRunner(plan.PrereqArgv)); err != nil {
		return nil, false, err
	}
	if !hermesdesktop.Installed(home) {
		return nil, false, fmt.Errorf("hermes desktop: install.sh finished but no app is under %s", plan.ReleaseDir)
	}
	return plan.LaunchArgv, true, nil
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

// desktopInstallRunner runs the install's commands: the prerequisites under a start line that
// stays, their output on the terminal and in the run log; install.sh behind a live line, its
// output in the run log alone.
func desktopInstallRunner(prereq []string) func(argv []string) error {
	return func(argv []string) error {
		log := ui.LogWriter()
		defer func() { _ = log.Close() }()
		c := exec.Command(argv[0], argv[1:]...)
		if slices.Equal(argv, prereq) {
			ui.Asyncf("hermes desktop — installing prerequisites")
			c.Stdin = os.Stdin
			c.Stdout, c.Stderr = io.MultiWriter(os.Stderr, log), io.MultiWriter(os.Stderr, log)
			return c.Run()
		}
		ui.Logf("hermes desktop — installing: %s", strings.Join(argv, " "))
		done := ui.Running(func(ran time.Duration) string {
			return "hermes desktop — installing · " + ran.Round(time.Second).String()
		}, "hermes desktop — installing")
		defer done()
		c.Stdout, c.Stderr = log, log
		return c.Run()
	}
}
