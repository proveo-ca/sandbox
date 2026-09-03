package workspace

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestImagePlatformIsWhatTheDefsBuildAsDockerWillPickIt(t *testing.T) {
	t.Parallel()
	// No override: linux, at the host's architecture — a local `proveo build`
	// loads exactly linux/<host arch> and the runner passes no --platform.
	got := ImagePlatform(env(nil))
	if got.OS != "linux" || got.Arch != HostPlatform().Arch {
		t.Errorf("ImagePlatform() = %s, want linux/%s", got, HostPlatform().Arch)
	}
	// DOCKER_DEFAULT_PLATFORM is what docker itself would honour.
	if got := ImagePlatform(env(map[string]string{"DOCKER_DEFAULT_PLATFORM": "linux/amd64"})); got != (Platform{OS: "linux", Arch: "amd64"}) {
		t.Errorf("override ignored: %s", got)
	}
	// A variant suffix names the same platform.
	if got := ParsePlatform("linux/arm64/v8"); got != (Platform{OS: "linux", Arch: "arm64"}) {
		t.Errorf("variant not folded: %s", got)
	}
	if got := ParsePlatform("linux/aarch64"); got.Arch != "arm64" {
		t.Errorf("uname spelling not folded: %s", got)
	}
	if got := ParsePlatform("garbage"); got != (Platform{}) {
		t.Errorf("unparseable platform must be zero, got %s", got)
	}
}

func TestDepCopyPolicyCopiesOnlyWhenHostAndImageAgree(t *testing.T) {
	t.Parallel()
	linuxArm := Platform{OS: "linux", Arch: "arm64"}
	for _, tc := range []struct {
		name  string
		env   map[string]string
		host  Platform
		reuse bool
		want  string // fragment of the reason
	}{
		{"macOS host never reuses", nil, Platform{OS: "darwin", Arch: "arm64"}, false, "darwin/arm64 ≠ image linux/arm64"},
		{"windows host never reuses", nil, Platform{OS: "windows", Arch: "amd64"}, false, "≠ image"},
		{"linux host of the same arch reuses", nil, linuxArm, true, "matches the image"},
		{"linux host of another arch does not", nil, Platform{OS: "linux", Arch: "amd64"}, false, "linux/amd64 ≠ image linux/arm64"},
		{"always overrides a mismatch", map[string]string{"PROVEO_DEPS_COPY": "always"}, Platform{OS: "darwin", Arch: "arm64"}, true, "PROVEO_DEPS_COPY=always"},
		{"never overrides a match", map[string]string{"PROVEO_DEPS_COPY": "never"}, linuxArm, false, "PROVEO_DEPS_COPY=never"},
		{"unknown host never reuses", nil, Platform{}, false, "≠ image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reuse, why := DepCopyPolicy(env(tc.env), tc.host, linuxArm)
			if reuse != tc.reuse {
				t.Errorf("reuse = %v, want %v (%s)", reuse, tc.reuse, why)
			}
			if !strings.Contains(why, tc.want) {
				t.Errorf("reason %q does not say %q — the operator has to see why the tree was or was not copied", why, tc.want)
			}
		})
	}
}
