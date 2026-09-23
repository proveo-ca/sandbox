// SPEC: _spec/internal/cdn/distribution-update.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"github.com/spf13/cobra"
)

func init() {
	register(&cobra.Command{
		Use:   "deploy-cli",
		Short: "build-cli --release (goreleaser → dist/ → CDN stage), then wrangler deploy apps/cli",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			if err := buildCLI(root, true); err != nil {
				return err
			}
			return run(root, nil, "pnpm", wranglerArgs("deploy")...)
		},
	})
}

// wranglerArgs is the pnpm argv for a wrangler verb against the apps/cli worker.
func wranglerArgs(verb string) []string {
	return []string{"exec", "wrangler", verb, "--cwd", "apps/cli"}
}
