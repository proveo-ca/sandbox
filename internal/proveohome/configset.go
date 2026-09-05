// SPEC: _spec/_plans/config-seeding-and-persistence.puml
//
// SPEC: _spec/_plans/config-seeding-and-persistence.puml
package proveohome

import (
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/manifest"
)

// ConfigSetVar and ConfigFilesVar are the variables proveo_sync_config reads.
const (
	ConfigSetVar   = "PROVEO_CONFIG_DIRS"
	ConfigFilesVar = "PROVEO_CONFIG_FILES"
)

func ConfigFiles(h manifest.Home) string {
	if !h.Active() {
		return ""
	}
	var names []string
	for _, f := range h.Files {
		name := strings.TrimSpace(f)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\|;`) {
			continue // Validate already refuses these; belt and braces
		}
		names = append(names, name)
	}
	return strings.Join(names, ";")
}

func ConfigSet(h manifest.Home) string {
	if !h.Active() {
		return ""
	}
	var entries []string
	for _, m := range h.Mounts {
		hostRel := strings.Trim(strings.TrimSpace(m.Host), "/")
		agentRel := agentRel(m.Container)
		if hostRel == "" || agentRel == "" {
			continue
		}
		if strings.ContainsAny(hostRel+agentRel, "|;") {
			continue
		}
		entries = append(entries, hostRel+"|"+agentRel+"|"+denyList(m.Deny))
	}
	return strings.Join(entries, ";")
}

func agentRel(container string) string {
	c := strings.TrimSpace(container)
	if c == "" {
		return ""
	}
	rel, ok := strings.CutPrefix(filepath.ToSlash(c), ContainerHome+"/")
	if !ok {
		return ""
	}
	return strings.Trim(rel, "/")
}

func denyList(deny []string) string {
	var keep []string
	for _, name := range deny {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, `/\|;,`) || name == "." || name == ".." {
			continue
		}
		keep = append(keep, name)
	}
	return strings.Join(keep, ",")
}
