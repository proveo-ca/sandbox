// SPEC: _spec/internal/cdn/distribution-update.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"os"

	"github.com/spf13/cobra"
)

func init() {
	register(&cobra.Command{
		Use:   "dev-cli",
		Short: "Stage the CDN tree, then serve apps/cli locally with wrangler dev",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			if err := stageCDN(root, os.Getenv("PROVEO_CDN_REQUIRE_DIST") == "1"); err != nil {
				return err
			}
			return run(root, nil, "pnpm", wranglerArgs("dev")...)
		},
	})
}
