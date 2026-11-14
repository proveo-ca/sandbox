//go:build image

// SPEC: _spec/_devops/agent-update-prompt.puml
package imagetest_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/agentupdate"
	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/imagetest"
)

type overlayFacts struct {
	ID     string `json:"Id"`
	Config struct {
		User       string
		Entrypoint []string
		Labels     map[string]string
	}
	RootFS struct{ Layers []string }
}

func overlayInspect(t *testing.T, ref string) overlayFacts {
	t.Helper()
	r := imagetest.Docker(imagetest.ProbeTimeout, nil, "image", "inspect", ref, "--format", "{{json .}}")
	var f overlayFacts
	if err := json.Unmarshal([]byte(r.Out), &f); !r.OK() || err != nil {
		t.Fatalf("inspect %s: %v\n%s", ref, err, r.Out)
	}
	return f
}

// otherRelease is a published release of pin's agent that differs from carried, where its source lists more than one.
func otherRelease(t *testing.T, b *imagebuild.Builder, pin imagebuild.Pin, carried string) string {
	t.Helper()
	if pin.Eco != "npm" {
		v, err := b.Upstream(pin.Eco, b.PinPkg(pin))
		if err != nil || v == "" {
			t.Skipf("no upstream release for %s: %v", pin.Pkg, err)
		}
		return v
	}
	body, err := b.Fetch("https://registry.npmjs.org/" + pin.Pkg)
	if err != nil {
		t.Skipf("npm registry unreachable: %v", err)
	}
	var doc struct {
		Time map[string]time.Time `json:"time"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("npm packument for %s: %v", pin.Pkg, err)
	}
	major, _, _ := strings.Cut(carried, ".")
	var versions []string
	for v := range doc.Time {
		if v != carried && v != "created" && v != "modified" && !strings.Contains(v, "-") && strings.HasPrefix(v, major+".") {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		t.Skipf("%s lists no other %s.x release than %s", pin.Pkg, major, carried)
	}
	slices.SortFunc(versions, func(x, y string) int { return doc.Time[y].Compare(doc.Time[x]) })
	return versions[0]
}

// overlaySwap installs another release of target's agent over img the way an accepted update does.
func overlaySwap(s *imagetest.Suite, target, img string) {
	s.Check("an agent update installs another release over the image in one layer", func(t *testing.T) {
		_, pin, ok := imagebuild.AgentSpec(target)
		if !ok || pin.Overlay == "" {
			t.Fatalf("%s has no overlay recipe", target)
		}
		base := overlayInspect(t, img)
		var log bytes.Buffer
		b := imagebuild.New("")
		b.Out, b.Fetch = &log, imagebuild.FetchWithin(time.Minute)
		version := otherRelease(t, b, pin, base.Config.Labels[imagebuild.AgentVersionLabel])
		ref := fmt.Sprintf("%s:overlaytest-%d", imagebuild.RefRepo(img), os.Getpid())
		t.Cleanup(func() { imagetest.Docker(time.Minute, nil, "image", "rm", ref) })
		if err := b.Overlay(target, img, base.ID, base.Config.User, version, ref); err != nil {
			t.Fatalf("overlay %s %s over %s: %v\n%s", target, version, img, err, log.String())
		}
		got := overlayInspect(t, ref)
		if v := got.Config.Labels[imagebuild.AgentVersionLabel]; v != version {
			t.Errorf("the overlay's %s is %q, want %q", imagebuild.AgentVersionLabel, v, version)
		}
		if v := got.Config.Labels[imagebuild.OverlayBaseLabel]; v != base.ID {
			t.Errorf("the overlay names the base %q, want %q", v, base.ID)
		}
		if got.Config.User != base.Config.User || !slices.Equal(got.Config.Entrypoint, base.Config.Entrypoint) {
			t.Errorf("the overlay runs as %q via %q; the base runs as %q via %q",
				got.Config.User, got.Config.Entrypoint, base.Config.User, base.Config.Entrypoint)
		}
		if n, want := len(got.RootFS.Layers), len(base.RootFS.Layers)+1; n != want {
			t.Errorf("the overlay has %d layers, want the base's %d plus one", n, want-1)
		}
	})
}

// TestImageRegistryNamesTheAgent reads a published image's config the way a run's update check does.
func TestImageRegistryNamesTheAgent(t *testing.T) {
	ref := agentupdate.PublishedRef(imagetest.Resolve("claudecode", "proveo/claudecode:latest"))
	img, err := agentupdate.New(filepath.Join(t.TempDir(), "agent-update.json")).Published(ref)
	if err != nil {
		t.Skipf("%s is not readable from its registry: %v", ref, err)
	}
	if img.Version == "" || img.Created.IsZero() {
		t.Errorf("the registry config of %s gave version %q created %v; the check could never offer a pull", ref, img.Version, img.Created)
	}
}
