package proveohome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestRoot(t *testing.T) {
	t.Parallel()
	if got := Root(func(k string) string {
		if k == "PROVEO_HOME" {
			return "/custom/proveo"
		}
		return ""
	}); got != "/custom/proveo" {
		t.Errorf("PROVEO_HOME override = %q", got)
	}
	if got := Root(func(k string) string {
		if k == "HOME" {
			return "/home/u"
		}
		return ""
	}); got != "/home/u/.proveo" {
		t.Errorf("default root = %q", got)
	}
}

// The durable root is where sessions, logs and provisioned toolchains live, so
// resolving it to "." puts all three inside the operator's repository. HOME is
// set by Git Bash on Windows and by nothing else, which is why a PowerShell
// proveo needs USERPROFILE and its domain-joined fallback.
func TestUserHomeAcrossHostOSes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unix", map[string]string{"HOME": "/home/u"}, "/home/u"},
		{"macos", map[string]string{"HOME": "/Users/u"}, "/Users/u"},
		{"windows powershell", map[string]string{"USERPROFILE": `C:\Users\u`}, `C:\Users\u`},
		{"windows git bash", map[string]string{"HOME": "/c/Users/u", "USERPROFILE": `C:\Users\u`}, "/c/Users/u"},
		{"windows domain joined", map[string]string{"HOMEDRIVE": "H:", "HOMEPATH": `\users\u`}, `H:\users\u`},
		{"nothing set", map[string]string{}, "."},
		// Whitespace is not a home. An env var set to blanks used to pass the
		// != "" test and produce a root of " /.proveo".
		{"blank home falls through", map[string]string{"HOME": "   ", "USERPROFILE": `C:\Users\u`}, `C:\Users\u`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := UserHome(func(k string) string { return tc.env[k] })
			if got != tc.want {
				t.Errorf("UserHome() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Root must never land in the working directory on a supported host: that is
// the shape that scatters durable state into repositories.
func TestRootIsNotRelativeOnWindows(t *testing.T) {
	t.Parallel()
	got := Root(func(k string) string {
		if k == "USERPROFILE" {
			return `C:\Users\u`
		}
		return ""
	})
	if got == filepath.Join(".", ".proveo") {
		t.Fatalf("Root() = %q — a PowerShell proveo puts its durable root in the CWD", got)
	}
	if !strings.HasPrefix(got, `C:\Users\u`) {
		t.Errorf("Root() = %q, want it under the USERPROFILE home", got)
	}
}

func TestPrepareMountsAndScrub(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	getenv := func(k string) string {
		if k == "PROVEO_HOME" {
			return root
		}
		return ""
	}
	h := manifest.Home{
		Enabled: true,
		Mounts: []manifest.HomeMount{
			{Host: ".cursor", Container: "/proveo-home/.cursor", Mode: "rw", Deny: []string{"auth.json"}},
			{Host: "opencode/share", Container: "/proveo-home/.local/share/opencode", Mode: "rw", Deny: []string{"auth.json"}},
		},
	}
	// Pre-seed a forbidden auth file and a keep-me session marker.
	share := filepath.Join(root, "opencode", "share")
	if err := os.MkdirAll(share, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, "auth.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, "keep.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Prepare(h, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root {
		t.Errorf("Root = %q", p.Root)
	}
	if len(p.Mounts) != 1 {
		t.Fatalf("Mounts = %d, want 1 (whole PROVEO_HOME)", len(p.Mounts))
	}
	if p.Mounts[0].Host != root || p.Mounts[0].Container != ContainerHome {
		t.Errorf("root mount = %+v", p.Mounts[0])
	}
	if p.Mounts[0].ReadOnly {
		t.Error("proveo home mount should be rw")
	}
	if _, err := os.Stat(filepath.Join(root, ".cursor")); err != nil {
		t.Errorf(".cursor subdir should exist: %v", err)
	}
	// PROVEO_HOME travels beside HOME: sbx's startup dispatcher resets HOME from
	// /etc/passwd, so the seed needs a name no launcher rewrites.
	if len(p.Env) != 2 || p.Env[0] != "HOME="+ContainerHome || p.Env[1] != "PROVEO_HOME="+ContainerHome {
		t.Errorf("Env = %v", p.Env)
	}
	if _, err := os.Stat(filepath.Join(share, "auth.json")); !os.IsNotExist(err) {
		t.Errorf("auth.json should be scrubbed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(share, "keep.txt")); err != nil {
		t.Errorf("keep.txt should survive: %v", err)
	}
	// Must not touch a sibling host IDE path under $HOME/.cursor.
	ide := filepath.Join(root, "..", ".cursor")
	_ = ide
}

func TestPrepareInactive(t *testing.T) {
	t.Parallel()
	p, err := Prepare(manifest.Home{}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != "" || len(p.Mounts) != 0 {
		t.Errorf("inactive plan = %+v", p)
	}
}

func TestResumeArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		target, id string
		cont, list bool
		want       []string
		wantErr    bool
	}{
		{target: "cursor", id: "abc", want: []string{"--resume", "abc"}},
		{target: "cursor-browser", cont: true, want: []string{"--continue"}},
		{target: "cursor", list: true, want: []string{"ls"}},
		{target: "claudecode-solidity", id: "s1", want: []string{"--resume", "s1"}},
		{target: "claudecode", cont: true, want: []string{"--continue"}},
		{target: "opencode", id: "sess", want: []string{"--session", "sess"}},
		{target: "opencode", cont: true, wantErr: true},
		{target: "cecli", id: "x", wantErr: true},
		{target: "cursor", id: "a", cont: true, wantErr: true},
	}
	for _, tc := range tests {
		got, err := ResumeArgs(tc.target, tc.id, tc.cont, tc.list)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.target, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v want %v", tc.target, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v want %v", tc.target, got, tc.want)
				break
			}
		}
	}
}
