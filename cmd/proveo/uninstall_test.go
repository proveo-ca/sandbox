package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/shell"
)

func TestStripInstallAndSetupPathBlocks(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".bashrc")
	bin := filepath.Join(dir, "install", "bin")
	content := strings.Join([]string{
		"export FOO=1",
		installPathMarkerStart,
		`export PATH="` + bin + `:$PATH"`,
		installPathMarkerEnd,
		shell.Marker,
		`export PATH="` + bin + `:$PATH"`,
		"export BAR=2",
		"",
	}, "\n")
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stripInstallPathBlock(rc, bin); err != nil {
		t.Fatal(err)
	}
	if err := stripSetupPathBlock(rc); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(rc)
	s := string(got)
	if strings.Contains(s, installPathMarkerStart) || strings.Contains(s, shell.Marker) || strings.Contains(s, bin) {
		t.Fatalf("markers/path not stripped:\n%s", s)
	}
	if !strings.Contains(s, "export FOO=1") || !strings.Contains(s, "export BAR=2") {
		t.Fatalf("unrelated lines lost:\n%s", s)
	}
}

func TestRemoveInstallRootSafety(t *testing.T) {
	if err := removeInstallRoot("/"); err == nil {
		t.Fatal("must refuse /")
	}
	home, _ := os.UserHomeDir()
	if err := removeInstallRoot(home); err == nil {
		t.Fatal("must refuse $HOME")
	}
	root := filepath.Join(t.TempDir(), "proveo-root")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeInstallRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root still exists: %v", err)
	}
}

func TestDoUninstallYes(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".proveo")
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_INSTALL_ROOT", root)
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "proveo"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".bashrc")
	_ = os.WriteFile(rc, []byte(installPathMarkerStart+"\nexport PATH=\""+filepath.Join(root, "bin")+":$PATH\"\n"+installPathMarkerEnd+"\n"), 0o644)
	if err := doUninstall(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("install root not removed")
	}
}
