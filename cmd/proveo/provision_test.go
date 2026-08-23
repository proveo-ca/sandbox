package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestProvisionerEnsure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		deps       []imageDep
		present    map[string]bool
		pullErr    bool
		confirm    bool
		wantPulls  []string
		wantBuilds []string
		wantErr    string // substring; "" => nil error
	}{
		{
			name:    "present images are untouched",
			deps:    []imageDep{{Name: "ubuntu/squid:latest"}, {Name: "proveo/egress-proxy:latest"}},
			present: map[string]bool{"ubuntu/squid:latest": true, "proveo/egress-proxy:latest": true},
		},
		{
			name:      "missing images are pulled first — including published proveo/* ones",
			deps:      []imageDep{{Name: "ubuntu/squid:latest"}, {Name: "proveo/egress-proxy:latest", BuildScript: "/src/build.sh"}},
			wantPulls: []string{"ubuntu/squid:latest", "proveo/egress-proxy:latest"},
		},
		{
			name:      "pull failure without a source tree is a hard error",
			deps:      []imageDep{{Name: "proveo/cursor:latest"}},
			pullErr:   true,
			wantPulls: []string{"proveo/cursor:latest"},
			wantErr:   "pull failed",
		},
		{
			name:       "pull failure falls back to a confirmed local build",
			deps:       []imageDep{{Name: "proveo/egress-proxy:latest", BuildScript: "/src/defs/sidecars/egress-proxy/build.sh"}},
			pullErr:    true,
			confirm:    true,
			wantPulls:  []string{"proveo/egress-proxy:latest"},
			wantBuilds: []string{"/src/defs/sidecars/egress-proxy/build.sh"},
		},
		{
			name:      "declined build keeps the actionable failure",
			deps:      []imageDep{{Name: "proveo/egress-proxy:latest", BuildScript: "/src/build.sh"}},
			pullErr:   true,
			confirm:   false,
			wantPulls: []string{"proveo/egress-proxy:latest"},
			wantErr:   "PROVEO_AUTO_PROVISION=1",
		},
		{
			name:      "duplicates are checked once",
			deps:      []imageDep{{Name: "ubuntu/squid:latest"}, {Name: "ubuntu/squid:latest"}},
			wantPulls: []string{"ubuntu/squid:latest"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var pulls, builds []string
			pv := provisioner{
				Present: func(img string) bool { return tc.present[img] },
				Pull: func(img string) error {
					pulls = append(pulls, img)
					if tc.pullErr {
						return os.ErrNotExist
					}
					return nil
				},
				Build:   func(s string) error { builds = append(builds, s); return nil },
				Confirm: func(string) bool { return tc.confirm },
				UI:      &ui.Printer{W: &strings.Builder{}, Plain: true},
			}
			err := pv.Ensure(tc.deps)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Ensure(%v) = %v, want nil", tc.deps, err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Ensure(%v) err = %v, want substring %q", tc.deps, err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantPulls, pulls); diff != "" {
				t.Errorf("pulls mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantBuilds, builds); diff != "" {
				t.Errorf("builds mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildScriptResolution(t *testing.T) {
	t.Parallel()
	defs := t.TempDir()
	mk := func(rel string) string {
		t.Helper()
		p := filepath.Join(defs, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/bash\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	proxyScript := mk("sidecars/egress-proxy/build.sh")
	cecliScript := mk("cecli/build.sh")

	t.Run("sidecar image resolves under defs/sidecars", func(t *testing.T) {
		if got := sidecarBuildScript(defs, "proveo/egress-proxy:latest"); got != proxyScript {
			t.Errorf("sidecarBuildScript = %q, want %q", got, proxyScript)
		}
	})
	t.Run("public image never resolves a build script", func(t *testing.T) {
		if got := sidecarBuildScript(defs, "ubuntu/squid:latest"); got != "" {
			t.Errorf("sidecarBuildScript(public image) = %q, want empty", got)
		}
	})
	t.Run("no source tree resolves nothing", func(t *testing.T) {
		if got := sidecarBuildScript("", "proveo/egress-proxy:latest"); got != "" {
			t.Errorf("sidecarBuildScript(no defs) = %q, want empty", got)
		}
	})
	t.Run("harness image resolves via the manifest name, not the image name", func(t *testing.T) {
		man := manifest.Manifest{Name: "cecli", Images: map[string]string{
			"cecli": "proveo/cecli:latest", "cecli-node": "proveo/cecli-node:latest",
		}}
		if got := harnessBuildScript(defs, man, "proveo/cecli-node:latest"); got != cecliScript {
			t.Errorf("harnessBuildScript = %q, want %q", got, cecliScript)
		}
	})
	t.Run("overridden non-proveo agent image resolves nothing", func(t *testing.T) {
		man := manifest.Manifest{Name: "cecli"}
		if got := harnessBuildScript(defs, man, "ghcr.io/acme/custom:1"); got != "" {
			t.Errorf("harnessBuildScript(custom image) = %q, want empty", got)
		}
	})
}

func TestEnsureDockerUsable(t *testing.T) {
	origLook, origGoos, origInfo := preflightLookPath, preflightGOOS, preflightInfo
	t.Cleanup(func() { preflightLookPath, preflightGOOS, preflightInfo = origLook, origGoos, origInfo })

	set := func(path string, goos string, infoErr error) {
		preflightLookPath = func(string) (string, error) {
			if path == "" {
				return "", os.ErrNotExist
			}
			return path, nil
		}
		preflightGOOS = goos
		preflightInfo = func() error { return infoErr }
	}

	t.Run("healthy host passes", func(t *testing.T) {
		set("/usr/bin/docker", "linux", nil)
		if err := ensureDockerUsable(); err != nil {
			t.Errorf("ensureDockerUsable() = %v, want nil", err)
		}
	})

	t.Run("missing CLI names the fix on darwin", func(t *testing.T) {
		set("", "darwin", nil)
		err := ensureDockerUsable()
		if err == nil || !strings.Contains(err.Error(), "OrbStack") {
			t.Errorf("ensureDockerUsable() = %v, want OrbStack hint", err)
		}
	})

	t.Run("missing CLI on linux stays generic", func(t *testing.T) {
		set("", "linux", nil)
		err := ensureDockerUsable()
		if err == nil || strings.Contains(err.Error(), "OrbStack") {
			t.Errorf("ensureDockerUsable() = %v, want generic PATH hint", err)
		}
	})

	t.Run("unreachable daemon points at the VM on darwin", func(t *testing.T) {
		set("/usr/local/bin/docker", "darwin", errors.New("cannot connect"))
		err := ensureDockerUsable()
		if err == nil || !strings.Contains(err.Error(), "open -a OrbStack") {
			t.Errorf("ensureDockerUsable() = %v, want start-the-VM hint", err)
		}
	})

	t.Run("unreachable daemon wraps the cause elsewhere", func(t *testing.T) {
		set("/usr/bin/docker", "linux", errors.New("cannot connect"))
		err := ensureDockerUsable()
		if err == nil || !strings.Contains(err.Error(), "docker info") {
			t.Errorf("ensureDockerUsable() = %v, want docker-info failure", err)
		}
	})
}

func TestPromptYesNo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		def   bool
		want  bool
	}{
		{"yes", "y\n", false, true},
		{"YES word", "Yes\n", false, true},
		{"no", "n\n", true, false},
		{"empty takes default yes", "\n", true, true},
		{"empty takes default no", "\n", false, false},
		{"garbage is no even with default yes", "wat\n", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out strings.Builder
			if got := promptYesNo("build?", tc.def, strings.NewReader(tc.input), &out); got != tc.want {
				t.Errorf("promptYesNo(input=%q, def=%v) = %v, want %v", tc.input, tc.def, got, tc.want)
			}
			wantSuffix := "[Y/n]"
			if !tc.def {
				wantSuffix = "[y/N]"
			}
			if !strings.Contains(out.String(), wantSuffix) {
				t.Errorf("prompt %q should show %s", out.String(), wantSuffix)
			}
		})
	}
}
