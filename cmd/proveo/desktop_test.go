// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestAnInstalledDesktopIsLaunchedWithoutAsking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_AUTO_PROVISION", "off")
	if err := os.MkdirAll(filepath.Join(hermesdesktop.ReleaseDir(home), "mac-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch, installed, err := ensureDesktop()
	if err != nil || installed || len(launch) == 0 || !strings.HasPrefix(launch[0], home) {
		t.Fatalf("ensureDesktop = %v, %v, %v; want the installed app's launch, installed before this run", launch, installed, err)
	}
}

func TestADeclinedInstallInstallsNothingAndLogsThePlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_AUTO_PROVISION", "off")
	var screen, log strings.Builder
	prev := ui.Default
	ui.Default = ui.New(&screen)
	restore := ui.TeeTo(&log)
	t.Cleanup(func() { restore(); ui.Default = prev })
	_, _, err := ensureDesktop()
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("ensureDesktop with installs declined = %v, want a refusal naming the missing app", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".hermes")); statErr == nil {
		t.Error("a declined install wrote ~/.hermes")
	}
	if strings.Contains(screen.String(), "The plan") {
		t.Errorf("the install plan reached the screen:\n%s", screen.String())
	}
	if !strings.Contains(log.String(), "Hermes Desktop is not installed. The plan") {
		t.Errorf("the run log lacks the install plan:\n%s", log.String())
	}
}
