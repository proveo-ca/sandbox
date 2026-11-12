// SPEC: _spec/_devops/agent-update-prompt.puml
package imagebuild

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// AgentVersionLabel names the release of the agent an image carries.
	AgentVersionLabel = "proveo.agent.version"
	// OverlayBaseLabel names the image id an overlay was built on.
	OverlayBaseLabel = "proveo.agent.overlay.base"
	// OverlayTagPrefix opens the tag of every overlay.
	OverlayTagPrefix = "agent-"
)

// ErrNoOverlay is Overlay's error for a target whose agent cannot be installed over a built image.
var ErrNoOverlay = errors.New("no overlay recipe")

// AgentSpec is the spec that installs target's agent and its pin, following Needs to the harness.
func AgentSpec(target string) (Spec, Pin, bool) {
	for range Specs {
		s, ok := Specs[target]
		switch {
		case !ok:
			return Spec{}, Pin{}, false
		case len(s.Pins) > 0:
			return s, s.Pins[0], true
		case s.Needs == "":
			return Spec{}, Pin{}, false
		}
		target = s.Needs
	}
	return Spec{}, Pin{}, false
}

var unsafeTag = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// OverlayRef is the image an overlay of base at version is tagged as.
func OverlayRef(base, version string) string {
	return RefRepo(base) + ":" + OverlayTagPrefix + unsafeTag.ReplaceAllString(version, "_")
}

// OverlayDockerfile is the recipe that installs pin's agent over base in one layer; user is base's own USER.
func OverlayDockerfile(s Spec, pin Pin, base, baseID, user string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\nUSER root\n", base)
	for _, e := range s.EnvArgs {
		fmt.Fprintf(&b, "ARG %s\n", e.Arg)
	}
	fmt.Fprintf(&b, "ARG %s\nRUN %s\n", pin.Arg, pin.Overlay)
	if user != "" {
		fmt.Fprintf(&b, "USER %s\n", user)
	}
	fmt.Fprintf(&b, "LABEL %s=\"${%s}\" \\\n      %s=%q\n", AgentVersionLabel, pin.Arg, OverlayBaseLabel, baseID)
	return b.String()
}

// Overlay builds ref: base with target's agent at version installed over it.
func (b *Builder) Overlay(target, base, baseID, user, version, ref string) error {
	s, pin, ok := AgentSpec(target)
	if !ok || pin.Overlay == "" {
		return fmt.Errorf("%s: %w", target, ErrNoOverlay)
	}
	builder, ok := b.HostBuilder()
	if !ok {
		return errors.New("no running docker-driver buildx builder reads the host image store (set PROVEO_BUILDX_LOCAL_BUILDER)")
	}
	dir, err := os.MkdirTemp("", "proveo-agent-overlay-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	recipe := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(recipe, []byte(OverlayDockerfile(s, pin, base, baseID, user)), 0o600); err != nil {
		return err
	}
	args := []string{"buildx", "build", "--builder", builder, "--platform", HostPlatform(b.Arch), "--load", "--pull=false",
		"--build-arg", pin.Arg + "=" + version}
	for _, e := range s.EnvArgs {
		v := b.env(e.Arg)
		if v == "" {
			v = e.Default
		}
		args = append(args, "--build-arg", e.Arg+"="+v)
	}
	return b.Docker.Stream(b.Out, append(args, "-f", recipe, "-t", ref, dir)...)
}

var dottedPrefix = regexp.MustCompile(`^v?(\d+(?:\.\d+)*)`)

// Newer reports whether release a is ahead of release b in eco; two releases it cannot order count as ahead.
func Newer(eco, a, b string) bool {
	switch {
	case a == "" || a == b:
		return false
	case b == "" || eco == "github":
		return true
	}
	pa, pb := dottedPrefix.FindStringSubmatch(a), dottedPrefix.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return true
	}
	if c := compareDotted(pa[1], pb[1]); c != 0 {
		return c > 0
	}
	return true
}

func compareDotted(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := "0", "0"
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
		if len(x) != len(y) {
			return len(x) - len(y)
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}
