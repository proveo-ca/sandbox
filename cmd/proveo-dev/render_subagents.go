// SPEC: _spec/defs/agent-definition-sharing.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/ui"
)

const renderScript = `set -euo pipefail; source /entrypoint-lib.sh; render_subagents "$1" "$2" 1`

func init() {
	var image string
	c := &cobra.Command{
		Use:   "render-subagents <harness> [dest-dir]",
		Short: "Preview the subagents one harness image composes, rendered inside that image",
		Long: `Runs render_subagents from the working-tree packages/lib/entrypoint-lib.sh
inside the harness image's bash, over the working-tree defs/subagents.
dest-dir defaults to a fresh temp dir. --image overrides proveo/<harness>
(resolved to its newer :local build when one exists).`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			harness := args[0]
			dest := ""
			if len(args) > 1 {
				dest = args[1]
			}
			if dest == "" {
				if dest, err = os.MkdirTemp("", "subagents-"); err != nil {
					return err
				}
			}
			if dest, err = filepath.Abs(dest); err != nil {
				return err
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			if image == "" {
				image, _ = maintain.ResolveImage("proveo/"+harness+":"+maintain.PublishTag, imageCreated)
			}
			if err := run(root, nil, "docker", renderArgs(root, image, harness, dest, os.Getuid(), os.Getgid())...); err != nil {
				return err
			}
			ui.Okf("preview: %s", dest)
			return nil
		},
	}
	c.Flags().StringVar(&image, "image", "", "image whose bash runs the renderer (default: proveo/<harness>)")
	register(c)
}

func renderArgs(root, image, harness, dest string, uid, gid int) []string {
	src := filepath.Join(root, "defs", "subagents")
	lib := filepath.Join(root, "packages", "lib", "entrypoint-lib.sh")
	return []string{
		"run", "--rm", "--network", "none",
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"--entrypoint", "bash",
		"-v", src + ":" + src + ":ro",
		"-v", lib + ":/entrypoint-lib.sh:ro",
		"-v", dest + ":" + dest,
		"-e", "PROVEO_SUBAGENTS_DIR=" + src,
		image, "-c", renderScript, "render-subagents", harness, dest,
	}
}

func imageCreated(ref string) (time.Time, bool) {
	out, err := exec.Command("docker", "image", "inspect", ref, "--format", "{{.Created}}").Output()
	if err != nil {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	return ts, err == nil
}
