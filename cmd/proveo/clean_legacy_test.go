//go:build !windows

// SPEC: _spec/internal/clean/clean-lifecycle.puml
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/clean"
)

func TestTheLegacySweepFindsEveryRetiredDockerLeftover(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  "ps -a --filter label=proveo.egress.session"*) printf 'proveo-egress-squid-x\texited\tproveo-1-2\n' ;;
  "ps -a --filter name=proveo-dind-"*) printf 'proveo-dind-opencode\texited\n' ;;
  "network ls --filter label=proveo.egress.session"*) echo proveo-net-x ;;
  "network inspect"*) printf 'proveo-1-2\t0\n' ;;
  "volume ls --filter name=proveo-squid-cache-"*) printf 'proveo-squid-cache-claudecode\nproveo-squid-cache-codex\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	legacy := gatherLegacyDocker()
	got := map[string]string{
		"egress":   joinNames(legacy.Egress),
		"dind":     joinNames(legacy.LegacyDind),
		"networks": legacy.Networks[0].Name,
		"volumes":  strings.Join(legacy.Volumes, ","),
	}
	want := map[string]string{
		"egress":   "proveo-egress-squid-x",
		"dind":     "proveo-dind-opencode",
		"networks": "proveo-net-x",
		"volumes":  "proveo-squid-cache-claudecode,proveo-squid-cache-codex",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("gatherLegacyDocker %s = %q, want %q", k, got[k], w)
		}
	}
}

func joinNames(cs []clean.Container) string {
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	return strings.Join(names, ",")
}
