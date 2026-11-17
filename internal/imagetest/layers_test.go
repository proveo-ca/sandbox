//go:build image

// SPEC: _spec/_devops/sandbox-template-rebase.puml (FLAT BASE)
package imagetest_test

import (
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/imagetest"
	"github.com/proveo-ca/proveo/internal/ui"
)

const sbxLayerCap = 40

func TestEveryLocalImageStaysUnderTheSbxLayerCap(t *testing.T) {
	if err := imagetest.DaemonReady(); err != nil {
		t.Skip(err)
	}
	list := imagetest.Docker(time.Minute, nil, "images", "--filter", "reference=proveo/*:local", "--format", "{{.Repository}}:{{.Tag}}")
	if !list.OK() {
		t.Fatalf("docker images: %v\n%s", list.Err, list.Out)
	}
	refs := strings.Fields(list.Out)
	if len(refs) == 0 {
		t.Skip("no proveo/*:local image built on this host")
	}
	for _, ref := range refs {
		r := imagetest.Docker(time.Minute, nil, "image", "inspect", ref, "--format", "{{len .RootFS.Layers}}")
		n, err := strconv.Atoi(strings.TrimSpace(r.Out))
		if !r.OK() || err != nil {
			t.Errorf("%s: layer count unreadable: %v %s", ref, r.Err, r.Out)
			continue
		}
		if n > sbxLayerCap {
			t.Errorf("%s has %d layers; sbx 0.49.2 hangs booting 45 or more, and proveo keeps every image at %d or fewer",
				ref, n, sbxLayerCap)
		}
	}
}

type imageConfig struct {
	User       string
	WorkingDir string
	Entrypoint []string
	Cmd        []string
	Env        []string
	Volumes    map[string]struct{}
	Labels     map[string]string
}

func inspectConfig(t *testing.T, ref string) imageConfig {
	t.Helper()
	r := imagetest.Docker(time.Minute, nil, "image", "inspect", ref, "--format", "{{json .Config}}")
	if !r.OK() {
		t.Fatalf("inspect %s: %v\n%s", ref, r.Err, r.Out)
	}
	var c imageConfig
	if err := json.Unmarshal([]byte(r.Out), &c); err != nil {
		t.Fatalf("inspect %s: %v", ref, err)
	}
	for k := range c.Labels {
		if strings.HasPrefix(k, "org.opencontainers.") {
			delete(c.Labels, k)
		}
	}
	if len(c.Labels) == 0 {
		c.Labels = nil
	}
	slices.Sort(c.Env)
	return c
}

func TestAFlatStageKeepsTheConfigOfTheLayersItReplaces(t *testing.T) {
	if err := imagetest.DaemonReady(); err != nil {
		t.Skip(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ spec, dockerfile, layered, flat string }{
		{"base", "defs/base/Dockerfile", "base-full", ""},
		{"hermes", "defs/hermes/Dockerfile", "hermes-upstream", "hermes-flat"},
	} {
		t.Run(c.dockerfile, func(t *testing.T) {
			df := filepath.Join(root, c.dockerfile)
			if _, err := os.Stat(df); err != nil {
				t.Skip("no proveo checkout above this package")
			}
			b := imagebuild.New(root)
			b.Out, b.UI = io.Discard, ui.New(io.Discard)
			pins, err := b.PinArgs(c.spec)
			if err != nil {
				t.Fatalf("resolve %s pins the way `mise run build %s` does: %v", c.spec, c.spec, err)
			}
			build := func(target string) string {
				ref := "proveo-test/flat-" + filepath.Base(filepath.Dir(df)) + "-" + cmp.Or(target, "final") + ":local"
				args := append([]string{"build", "-q", "-f", df, "-t", ref}, pins...)
				if target != "" {
					args = append(args, "--target", target)
				}
				b := imagetest.Docker(30*time.Minute, nil, append(args, root)...)
				if !b.OK() {
					t.Fatalf("build %s --target %q: %v\n%s", c.dockerfile, target, b.Err, b.Out)
				}
				t.Cleanup(func() { imagetest.Docker(time.Minute, nil, "rmi", ref) })
				return ref
			}
			got, want := inspectConfig(t, build(c.flat)), inspectConfig(t, build(c.layered))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: the flat stage drifted from %s — re-declare its config\n got: %+v\nwant: %+v",
					c.dockerfile, c.layered, got, want)
			}
		})
	}
}
