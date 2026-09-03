// SPEC: _spec/packages/lib/dependency-trees.puml
package workspace

import (
	"fmt"
	"runtime"
	"strings"
)

// Platform is an OS/arch pair in docker's vocabulary (linux/arm64), without the
// variant suffix docker sometimes appends (linux/arm64/v8).
type Platform struct {
	OS, Arch string
}

func (p Platform) String() string { return p.OS + "/" + p.Arch }

// ParsePlatform reads "os/arch" or "os/arch/variant". Unknown shapes yield a
// zero Platform, which never matches anything — the safe direction.
func ParsePlatform(s string) Platform {
	parts := strings.Split(strings.TrimSpace(strings.ToLower(s)), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Platform{}
	}
	return Platform{OS: parts[0], Arch: normalizeArch(parts[1])}
}

// normalizeArch folds uname spellings onto docker's, the same way
// proveo_docker_host_platform in defs/lib/docker-build.sh does.
func normalizeArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return a
}

// HostPlatform is where the operator's dependency trees were built.
func HostPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: normalizeArch(runtime.GOARCH)}
}

// ImagePlatform is the platform the agent container will run as, reproducing
// docker's own default rule so --print need not ask the daemon.
func ImagePlatform(getenv func(string) string) Platform {
	if v := strings.TrimSpace(getenv("DOCKER_DEFAULT_PLATFORM")); v != "" {
		if p := ParsePlatform(v); p.OS != "" {
			return p
		}
	}
	return Platform{OS: "linux", Arch: HostPlatform().Arch}
}

// DepCopyPolicy decides whether a host dependency tree is worth copying into
// its overlay, comparing the host's platform with the image's.
// PROVEO_DEPS_COPY overrides either way; the reason is returned for reporting.
func DepCopyPolicy(getenv func(string) string, host, image Platform) (reuse bool, reason string) {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_DEPS_COPY"))) {
	case "always", "1", "on", "true", "yes":
		return true, "PROVEO_DEPS_COPY=always — copying host trees regardless of platform"
	case "never", "0", "off", "false", "no":
		return false, "PROVEO_DEPS_COPY=never — staging empty; the seed installs"
	}
	if host == image && host.OS != "" {
		return true, fmt.Sprintf("host %s matches the image — copying host trees", host)
	}
	return false, fmt.Sprintf("host %s ≠ image %s — staging empty; the seed installs for %s", host, image, image)
}
