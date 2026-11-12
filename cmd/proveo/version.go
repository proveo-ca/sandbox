// SPEC: _spec/cmd/proveo/usage.puml
package main

import "runtime/debug"

func init() {
	info, ok := debug.ReadBuildInfo()
	version = resolvedVersion(version, info, ok)
}

// resolvedVersion is the -X stamp, else dev@<12 hex of vcs.revision>[+dirty] from Go build info, else dev.
func resolvedVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "dev" || !ok || info == nil {
		return stamped
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if r := []rune(rev); len(r) > 12 {
		rev = string(r[:12])
	}
	if dirty {
		return "dev@" + rev + "+dirty"
	}
	return "dev@" + rev
}
