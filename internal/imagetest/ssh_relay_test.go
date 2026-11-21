//go:build image

// SPEC: _spec/cmd/proveo/github-access.puml
package imagetest_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/imagetest"
	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestEveryHarnessImageShipsTheSSHAgentRelay(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := manifest.LoadFS(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	images, err := manifest.Targets(ms)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) == 0 {
		t.Skip("no proveo checkout above this package")
	}
	targets := make([]string, 0, len(images))
	for target := range images {
		targets = append(targets, target)
	}
	slices.Sort(targets)
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			s := imagetest.New(t, imagetest.Resolve(target, images[target]))
			s.Success("socat, which sbx's ssh-agent@1 relay runs, is on PATH", s.Image, "command -v socat")
		})
	}
}
