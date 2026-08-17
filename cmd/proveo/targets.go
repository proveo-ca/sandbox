// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/internal/maintain/image-build-deploy.puml
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// targetsCmd prints the maintainer build/deploy/test registry as TSV
// (name<TAB>image<TAB>defDir), one target per line. It is the single source of
// truth the maintainer mise tasks read (lib/registry.sh) instead of re-parsing
// the harness manifests in Bash. Maintainer tooling only — needs an on-disk
// defs/ tree (a checkout), not the embedded manifests.
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

// maintainerDefsDir resolves the on-disk defs/ root: PROVEO_DEFS_DIR, else the
// enclosing repo's defs/. Errors when neither exists (targets needs the source
// tree to locate each def's build.sh / test.sh).
func maintainerDefsDir() (string, error) {
	if d := os.Getenv("PROVEO_DEFS_DIR"); d != "" {
		return d, nil
	}
	root := orWD("")
	if ws := workspace.Resolve(root); ws.IsRepo {
		root = ws.Root
	}
	d := filepath.Join(root, "defs")
	if fi, err := os.Stat(d); err == nil && fi.IsDir() {
		return d, nil
	}
	return "", fmt.Errorf("targets: no defs/ tree found (run inside the repo or set PROVEO_DEFS_DIR)")
}
