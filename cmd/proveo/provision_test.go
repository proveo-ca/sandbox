package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/engine"
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
		wantBuilt  map[string]string
		wantErr    string // substring; "" => nil error
	}{
		{
			name:    "present images are untouched",
			deps:    []imageDep{{Name: "ubuntu/squid:latest"}, {Name: "proveo/egress-proxy:latest"}},
			present: map[string]bool{"ubuntu/squid:latest": true, "proveo/egress-proxy:latest": true},
		},
		{
			name:      "missing images are pulled first — including published proveo/* ones",
			deps:      []imageDep{{Name: "ubuntu/squid:latest"}, {Name: "proveo/egress-proxy:latest", Target: "egress-proxy"}},
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
			name:       "an unpullable :latest is built as :local and handed back",
			deps:       []imageDep{{Name: "proveo/claudecode-browser:latest", Target: "claudecode-browser"}},
			pullErr:    true,
			confirm:    true,
			wantPulls:  []string{"proveo/claudecode-browser:latest"},
			wantBuilds: []string{"claudecode-browser:local"},
			wantBuilt:  map[string]string{"proveo/claudecode-browser:latest": "proveo/claudecode-browser:local"},
		},
		{
			name:       "an explicit tag is built as itself",
			deps:       []imageDep{{Name: "proveo/cursor:rc1", Target: "cursor"}},
			pullErr:    true,
			confirm:    true,
			wantPulls:  []string{"proveo/cursor:rc1"},
			wantBuilds: []string{"cursor:rc1"},
			wantBuilt:  map[string]string{"proveo/cursor:rc1": "proveo/cursor:rc1"},
		},
		{
			name:      "declined build keeps the actionable failure",
			deps:      []imageDep{{Name: "proveo/egress-proxy:latest", Target: "egress-proxy"}},
			pullErr:   true,
			confirm:   false,
			wantPulls: []string{"proveo/egress-proxy:latest"},
			wantErr:   "proveo build egress-proxy --tag local",
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
				Build: func(target, tag string) (string, error) {
					builds = append(builds, target+":"+tag)
					return "proveo/" + target + ":" + tag, nil
				},
				Confirm: func(string) bool { return tc.confirm },
				UI:      &ui.Printer{W: &strings.Builder{}, Plain: true},
			}
			built, err := pv.Ensure(tc.deps)
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
			if tc.wantBuilt == nil {
				tc.wantBuilt = map[string]string{}
			}
			if diff := cmp.Diff(tc.wantBuilt, built); diff != "" {
				t.Errorf("built mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildTargetResolution(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, defs, image, want string
		man                     manifest.Manifest
	}{
		{"sidecar image names its target", "/d", "proveo/egress-proxy:latest", "egress-proxy", manifest.Manifest{}},
		{"a browser variant is its own target", "/d", "proveo/claudecode-browser:latest", "claudecode-browser", manifest.Manifest{Name: "claudecode"}},
		{"an unknown proveo image falls back to the manifest", "/d", "proveo/cecli-node:latest", "cecli", manifest.Manifest{Name: "cecli"}},
		{"public image never builds", "/d", "ubuntu/squid:latest", "", manifest.Manifest{}},
		{"no source tree builds nothing", "", "proveo/egress-proxy:latest", "", manifest.Manifest{}},
		{"overridden non-proveo agent image builds nothing", "/d", "ghcr.io/acme/custom:1", "", manifest.Manifest{Name: "cecli"}},
	} {
		if got := buildTarget(c.defs, c.man, c.image); got != c.want {
			t.Errorf("%s: buildTarget(%q) = %q, want %q", c.name, c.image, got, c.want)
		}
	}
}

func TestEnsureDockerUsable(t *testing.T) {
	origLook, origGoos, origInfo, origEngine := preflightLookPath, preflightGOOS, preflightInfo, preflightEngine
	t.Cleanup(func() {
		preflightLookPath, preflightGOOS, preflightInfo, preflightEngine = origLook, origGoos, origInfo, origEngine
	})

	set := func(path string, goos string, infoErr error) {
		preflightLookPath = func(string) (string, error) {
			if path == "" {
				return "", os.ErrNotExist
			}
			return path, nil
		}
		preflightGOOS = goos
		preflightInfo = func() error { return infoErr }
		preflightEngine = func() engine.Info { return engine.Info{Kind: engine.Unknown} }
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

	for _, tc := range []struct {
		kind      engine.Kind
		wantName  string
		wantStart string
	}{
		{engine.OrbStack, "OrbStack", "open -a OrbStack"},
		{engine.Colima, "Colima", "colima start"},
		{engine.Podman, "Podman", "podman machine start"},
		{engine.RancherDesktop, "Rancher Desktop", "open -a 'Rancher Desktop'"},
	} {
		t.Run("unreachable daemon names "+string(tc.kind), func(t *testing.T) {
			set("/usr/local/bin/docker", "darwin", errors.New("cannot connect"))
			preflightEngine = func() engine.Info { return engine.Info{Kind: tc.kind} }
			err := ensureDockerUsable()
			if err == nil {
				t.Fatal("ensureDockerUsable() = nil, want an unreachable-daemon error")
			}
			if !strings.Contains(err.Error(), tc.wantName) || !strings.Contains(err.Error(), tc.wantStart) {
				t.Errorf("ensureDockerUsable() = %v, want it to name %q and %q", err, tc.wantName, tc.wantStart)
			}
			if tc.kind != engine.DockerDesktop && strings.Contains(err.Error(), "Docker Desktop") {
				t.Errorf("ensureDockerUsable() = %v, must not send a %s user to Docker Desktop", err, tc.wantName)
			}
		})
	}
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
