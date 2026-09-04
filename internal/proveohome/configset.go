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

// ConfigFiles encodes the manifest's home-root config files as a ";"-separated
// list of bare names. One name serves both sides: these sit at the root of the
// durable home AND at the root of the agent's home, so the relative path is the
// same in either direction — which is exactly why they needed no mount and got
// no persistence on the backend that copies instead of binding.
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

// ConfigSet encodes a manifest's durable home subtrees for the in-container
// config sync: ";"-separated "<host-rel>|<agent-rel>|<deny,csv>" entries.
//
// It is the SAME declaration the docker backend turns into bind mounts, resolved
// host-side and handed in as data rather than restated as a second list the
// shell would own. On docker those mounts are the persistence and nothing reads
// this; on sbx a bind nested under proveo home cannot be expressed at all, which
// is why every wired MCP server, LSP config, plugin record and settings merge
// died with the VM.
//
// The agent-relative path is the Container path with the proveo home prefix
// removed, because HOME is not redirected on the sandbox backend — the agent
// reads $HOME/.claude, never /proveo-home/.claude. A mount declaring a Container
// path outside proveo home has no agent-relative form and is skipped rather than
// guessed at.
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
		// A separator inside a path would silently re-split the entry on the way
		// in. Manifest-authored, so this never fires — and if a def ever tries, the
		// mount is dropped rather than corrupting every entry after it.
		if strings.ContainsAny(hostRel+agentRel, "|;") {
			continue
		}
		entries = append(entries, hostRel+"|"+agentRel+"|"+denyList(m.Deny))
	}
	return strings.Join(entries, ";")
}

// agentRel converts a container path under proveo home into a path relative to
// whatever home the agent actually has.
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

// denyList keeps only the names scrubDeny would act on, so the shell's skip set
// and the host's scrub agree on which files are credentials.
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
