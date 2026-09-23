// SPEC: _spec/_plans/host-shell-to-go.puml
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

func init() {
	register(&cobra.Command{
		Use:   "lint",
		Short: "Hard gates: gofmt -l, go vet (default + image tag), golangci-lint run",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return lint(root)
		},
	})
}

var lintDirs = []string{"internal", "cmd", "tests"}

func lint(root string) error {
	if bad := unformatted(root); len(bad) > 0 {
		ui.Failf("gofmt needed on:")
		for _, f := range bad {
			ui.Notef("%s", f)
		}
		ui.Notef("Run: mise run fmt")
		return &exitError{code: 1}
	}
	for _, step := range [][]string{
		{"go", "vet", "./..."},
		{"go", "vet", "-tags=image", "./internal/imagetest/"},
		{"golangci-lint", "run"},
	} {
		if err := run(root, nil, step[0], step[1:]...); err != nil {
			return err
		}
	}
	ui.Okf("lint succeeded.")
	return nil
}

func gofmtTargets(root string) []string {
	var targets []string
	for _, d := range lintDirs {
		if st, err := os.Stat(filepath.Join(root, d)); err == nil && st.IsDir() {
			targets = append(targets, d)
		}
	}
	files, _ := filepath.Glob(filepath.Join(root, "*.go"))
	for _, f := range files {
		targets = append(targets, filepath.Base(f))
	}
	return targets
}

func unformatted(root string) []string {
	targets := gofmtTargets(root)
	if len(targets) == 0 {
		return nil
	}
	c := exec.Command("gofmt", append([]string{"-l"}, targets...)...)
	c.Dir = root
	out, _ := c.Output()
	return splitLines(string(out))
}

func splitLines(s string) []string {
	var lines []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
