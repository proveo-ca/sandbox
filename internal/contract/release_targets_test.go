// SPEC: _spec/_devops/release-gate.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProveoBuildsForEveryReleaseTarget(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Main   string   `yaml:"main"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, b := range cfg.Builds {
		if b.ID != "proveo" {
			continue
		}
		if len(b.Goos) == 0 || len(b.Goarch) == 0 {
			t.Fatalf("build %q names no goos or goarch", b.ID)
		}
		for _, goos := range b.Goos {
			for _, goarch := range b.Goarch {
				t.Run(goos+"/"+goarch, func(t *testing.T) {
					t.Parallel()
					c := exec.Command("go", "build", "-o", os.DevNull, b.Main)
					c.Dir = root
					c.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
					if out, err := c.CombinedOutput(); err != nil {
						t.Errorf("go build %s for %s/%s: %v\n%s", b.Main, goos, goarch, err, out)
					}
				})
			}
		}
		return
	}
	t.Fatal(".goreleaser.yaml holds no build with id proveo")
}
