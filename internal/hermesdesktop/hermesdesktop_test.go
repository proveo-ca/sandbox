// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package hermesdesktop

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestThePlanPinsTheCommitOnEveryHost(t *testing.T) {
	t.Parallel()
	for _, h := range []sbx.Host{
		{OS: "darwin", Arch: "arm64"},
		{OS: "linux", Arch: "arm64", Distro: "ubuntu"},
		{OS: "linux", Arch: "amd64", Distro: "debian"},
		{OS: "linux", Arch: "amd64", Distro: "fedora"},
		{OS: "linux", Arch: "amd64", Distro: "pop", Like: "ubuntu debian"},
	} {
		p, err := PlanFor(h, "/home/op", "/dl")
		if err != nil {
			t.Fatalf("%+v: %v", h, err)
		}
		if i := slices.Index(p.InstallArgv, "--commit"); i < 0 || p.InstallArgv[i+1] != Commit {
			t.Errorf("%+v: install argv %v does not pin %s", h, p.InstallArgv, Commit)
		}
		if !slices.Contains(p.InstallArgv, "--include-desktop") {
			t.Errorf("%+v: install argv %v builds no desktop app", h, p.InstallArgv)
		}
		if !strings.Contains(InstallScriptURL(), "/"+Commit+"/") {
			t.Errorf("install.sh URL %s is not at the pinned commit", InstallScriptURL())
		}
	}
}

func TestLinuxPrerequisitesFollowTheMeasuredPackageSets(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		h    sbx.Host
		pm   string
		want []string
	}{
		{sbx.Host{OS: "linux", Arch: "arm64", Distro: "ubuntu"}, "apt-get", []string{"libatomic1", "build-essential"}},
		{sbx.Host{OS: "linux", Arch: "amd64", Distro: "debian"}, "apt-get", []string{"libatomic1", "build-essential"}},
		{sbx.Host{OS: "linux", Arch: "amd64", Distro: "fedora"}, "dnf", []string{"libatomic", "make", "gcc-c++"}},
	} {
		p, _ := PlanFor(c.h, "/home/op", "/dl")
		if len(p.PrereqArgv) < 2 || p.PrereqArgv[1] != c.pm {
			t.Errorf("%s: prerequisites run %v, want %s", c.h.Distro, p.PrereqArgv, c.pm)
		}
		for _, w := range c.want {
			if !slices.Contains(p.Prereqs, w) {
				t.Errorf("%s: prerequisites %v lack %s — the build failed without it", c.h.Distro, p.Prereqs, w)
			}
		}
	}
	if p, _ := PlanFor(sbx.Host{OS: "darwin", Arch: "arm64"}, "/Users/op", "/dl"); p.PrereqArgv != nil {
		t.Errorf("macOS runs %v; install.sh obtains git itself", p.PrereqArgv)
	}
}

func TestUnsupportedHostsAreRefusedByName(t *testing.T) {
	t.Parallel()
	for _, h := range []sbx.Host{
		{OS: "darwin", Arch: "amd64"},
		{OS: "linux", Arch: "amd64", Distro: "arch"},
		{OS: "windows", Arch: "amd64"},
	} {
		if _, err := PlanFor(h, "/home/op", "/dl"); err == nil {
			t.Errorf("%+v: accepted", h)
		}
	}
	if _, err := PlanFor(sbx.Host{OS: "darwin", Arch: "arm64"}, "", "/dl"); err == nil {
		t.Error("an empty home was accepted")
	}
}

func TestInstallRunsNothingWhenTheScriptDoesNotMatchThePin(t *testing.T) {
	t.Parallel()
	p, _ := PlanFor(sbx.Host{OS: "linux", Arch: "amd64", Distro: "ubuntu"}, t.TempDir(), t.TempDir())
	var ran [][]string
	run := func(argv []string) error { ran = append(ran, argv); return nil }
	err := Install(p, func(string) ([]byte, error) { return []byte("#!/bin/sh\necho tampered\n"), nil }, run)
	if err == nil || !strings.Contains(err.Error(), "sha256") || len(ran) != 0 {
		t.Fatalf("a tampered install.sh: err=%v ran=%v; nothing may run", err, ran)
	}
	if _, statErr := os.Stat(p.Script); statErr == nil {
		t.Error("a tampered install.sh was written to disk")
	}
	if err := Install(p, func(string) ([]byte, error) { return nil, errors.New("offline") }, run); err == nil || len(ran) != 0 {
		t.Fatalf("a failed fetch: err=%v ran=%v", err, ran)
	}
}

func TestInstalledNeedsABuiltAppDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if Installed(home) {
		t.Fatal("an empty home reads as installed")
	}
	if err := os.MkdirAll(filepath.Join(ReleaseDir(home), "linux-arm64-unpacked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !Installed(home) {
		t.Error("a built linux-arm64-unpacked app reads as missing")
	}
}
