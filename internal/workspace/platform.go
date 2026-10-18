// SPEC: _spec/packages/lib/dependency-trees.puml
package workspace

import (
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/envflag"
)

// Platform is an OS/arch pair in docker's vocabulary (linux/arm64), without the
// variant suffix docker sometimes appends (linux/arm64/v8).
type Platform struct {
	OS, Arch string
}

func (p Platform) String() string { return p.OS + "/" + p.Arch }

func ParsePlatform(s string) Platform {
	parts := strings.Split(strings.TrimSpace(strings.ToLower(s)), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Platform{}
	}
	return Platform{OS: parts[0], Arch: normalizeArch(parts[1])}
}

func normalizeArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return a
}

func HostPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: normalizeArch(runtime.GOARCH)}
}

func ImagePlatform(getenv func(string) string) Platform {
	if v := strings.TrimSpace(getenv("DOCKER_DEFAULT_PLATFORM")); v != "" {
		if p := ParsePlatform(v); p.OS != "" {
			return p
		}
	}
	return Platform{OS: "linux", Arch: HostPlatform().Arch}
}

// SharedTreesAllowed decides whether a mounted sbx checkout may carry its
// dependency trees into a sandbox of another platform.
func SharedTreesAllowed(getenv func(string) string, host, image Platform) (bool, string) {
	raw := getenv("PROVEO_DEPS")
	mode, known := DepsMode(raw)
	switch mode {
	case "shared":
		return true, "PROVEO_DEPS=shared — the host trees go in as they are; the sandbox installs nothing over a foreign tree"
	case "reinstall":
		return true, "PROVEO_DEPS=reinstall — the sandbox may rewrite the host trees for " + image.String()
	}
	if host == image && host.OS != "" {
		return true, fmt.Sprintf("host %s matches the image — the host trees run as-is", host)
	}
	why := fmt.Sprintf("host %s ≠ image %s — an install in the sandbox rewrites the host trees for %s", host, image, image)
	if envflag.Off(raw) {
		why = fmt.Sprintf("host %s ≠ image %s — PROVEO_DEPS=off installs nothing, and the host trees do not run on %s", host, image, image)
	}
	if !known {
		why = fmt.Sprintf("PROVEO_DEPS=%s is not one of %s; %s", strings.TrimSpace(raw), strings.Join(DepsModes(), "|"), why)
	}
	return false, why
}

// DepsModes are the PROVEO_DEPS values the host and the image both accept.
func DepsModes() []string { return []string{"auto", "off", "shared", "reinstall"} }

// DepsMode is v's PROVEO_DEPS mode: auto when empty, off for any envflag off word; known is false for anything else.
func DepsMode(v string) (mode string, known bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case v == "":
		return "auto", true
	case envflag.Off(v):
		return "off", true
	case slices.Contains(DepsModes(), v):
		return v, true
	}
	return "auto", false
}
