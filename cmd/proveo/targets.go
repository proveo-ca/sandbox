// SPEC: _spec/cmd/proveo/provision-and-targets.puml,
// _spec/internal/maintain/image-build-deploy.puml
//
// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/internal/maintain/image-build-deploy.puml
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func targetsCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "targets",
		Short:  "List maintainer build/deploy targets as TSV (tooling; needs a defs/ checkout)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defsDir, err := maintainerDefsDir()
			if err != nil {
				return err
			}
			ms, err := manifest.Load(defsDir)
			if err != nil {
				return fmt.Errorf("targets: loading manifests from %s: %w", defsDir, err)
			}
			out := cmd.OutOrStdout()
			for _, t := range maintain.Registry(ms, defsDir) {
				fmt.Fprintf(out, "%s\t%s\t%s\n", t.Name, t.Image, t.DefDir)
			}
			return nil
		},
	}
}

func maintainerDefsDir() (string, error) {
	if d := os.Getenv("PROVEO_DEFS_DIR"); d != "" {
		return d, nil
	}
	root := run.OrWD("")
	if ws := workspace.Resolve(root); ws.IsRepo {
		root = ws.Root
	}
	d := filepath.Join(root, "defs")
	if fi, err := os.Stat(d); err == nil && fi.IsDir() {
		return d, nil
	}
	return "", fmt.Errorf("targets: no defs/ tree found (run inside the repo or set PROVEO_DEFS_DIR)")
}
