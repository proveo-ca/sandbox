// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/hermesdesktop"
)

func TestAnInstalledDesktopIsLaunchedWithoutAsking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_AUTO_PROVISION", "off")
	if err := os.MkdirAll(filepath.Join(hermesdesktop.ReleaseDir(home), "mac-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch, err := ensureDesktop()
	if err != nil || len(launch) == 0 || !strings.HasPrefix(launch[0], home) {
		t.Fatalf("ensureDesktop = %v, %v; want the installed app's launch", launch, err)
	}
}

func TestADeclinedInstallInstallsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_AUTO_PROVISION", "off")
	_, err := ensureDesktop()
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("ensureDesktop with installs declined = %v, want a refusal naming the missing app", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".hermes")); statErr == nil {
		t.Error("a declined install wrote ~/.hermes")
	}
}
