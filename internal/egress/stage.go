// SPEC: _spec/internal/egress/teardown-and-signals.puml
package egress

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// squidStaticFiles are copied verbatim from the embedded def into each session's
// squid config dir; provider-allow.conf is generated alongside them.
var squidStaticFiles = []string{
	"squid.conf",
	"firehol-blocked-nets.conf",
	"firehol-ipset.conf",
}

func StageSquidConfig(fsys fs.FS, destDir string, providers []string, customDomains string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, name := range squidStaticFiles {
		data, err := fs.ReadFile(fsys, path.Join("defs", "sidecars", "squid-proxy", name))
		if err != nil {
			return fmt.Errorf("stage squid config: read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(destDir, name), data, 0o644); err != nil {
			return fmt.Errorf("stage squid config: write %s: %w", name, err)
		}
	}
	conf, _, _ := ProviderAllowConf(providers, customDomains)
	if err := os.WriteFile(filepath.Join(destDir, "provider-allow.conf"), []byte(conf), 0o644); err != nil {
		return fmt.Errorf("stage squid config: write provider-allow.conf: %w", err)
	}
	return nil
}
