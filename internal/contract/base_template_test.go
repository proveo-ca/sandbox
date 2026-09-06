package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The harness base must EXTEND a Docker sandbox template. Descending from a
// plain distro image is what left `docker: sbx` promised by four manifests and
// kept by none: sbx starts what the IMAGE ships, and the image shipped nothing.
// SPEC: _spec/_devops/sandbox-template-rebase.puml
func TestBaseExtendsASandboxTemplate(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))

	arg := regexp.MustCompile(`(?m)^ARG SANDBOX_TEMPLATE=(\S+)`).FindStringSubmatch(src)
	if arg == nil {
		t.Fatal("defs/base names no SANDBOX_TEMPLATE — the base image is the one place " +
			"the supported lineage is declared")
	}
	ref := arg[1]
	if !strings.HasPrefix(ref, "docker/sandbox-templates:") {
		t.Fatalf("base is %q: custom templates must extend a built-in agent environment, "+
			"not a bare distro image", ref)
	}
	if !regexp.MustCompile(`^ARG SANDBOX_TEMPLATE=\S+$`).MatchString(arg[0]) {
		t.Fatalf("malformed template pin: %q", arg[0])
	}
	if !strings.Contains(src, "FROM ${SANDBOX_TEMPLATE}") {
		t.Error("SANDBOX_TEMPLATE is declared but the runtime stage does not build FROM it")
	}
}

// A -docker variant is the only one carrying dockerd. A plain tag gives a
// client with no daemon behind it, which answers `docker` and then fails to
// connect — the same half-provisioning as shipping no docker at all.
func TestBaseTemplateCarriesTheEngine(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))
	ref := regexp.MustCompile(`(?m)^ARG SANDBOX_TEMPLATE=(\S+)`).FindStringSubmatch(src)[1]
	tag := ref[strings.LastIndex(ref, ":")+1:]
	if !strings.Contains(tag, "-docker") {
		t.Fatalf("template tag %q is not a -docker variant, so the image has no Engine — "+
			"`docker: sbx` in the manifests would again promise a daemon nothing supplies", tag)
	}
	// A floating tag lets a republished template silently change the libc, the
	// toolchains and the identity of every harness.
	if !regexp.MustCompile(`-\d+\.\d+\.\d+$`).MatchString(tag) {
		t.Errorf("template tag %q is not version-pinned", tag)
	}
}

// proveo-harden strips setuid bits. On this base that would disarm sudo, which
// is the agent user's only route to root — and the daemon needs root.
func TestHardenPassExemptsSudo(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/proveo-harden"))
	if !strings.Contains(src, "sudo.ws") {
		t.Fatal("proveo-harden does not name sudo.ws, so a blanket chmod u-s disarms " +
			"/usr/bin/sudo and the sandbox agent loses its only route to root")
	}
	if !regexp.MustCompile(`\*/sudo(\.ws)?\)[^\n]*continue`).MatchString(src) &&
		!strings.Contains(src, "*/sudo|*/sudo.ws") {
		t.Error("sudo is mentioned but not actually skipped by the stripping loop")
	}
	// The exemption must stay narrow: a harden pass that skips everything is not
	// a harden pass.
	for _, must := range []string{"-perm -4000", "-perm -2000", "chmod u-s,g-s"} {
		if !strings.Contains(src, must) {
			t.Errorf("proveo-harden no longer strips setuid/setgid (%q missing)", must)
		}
	}
}

// The base asserts its own inheritance at BUILD time. Without this a template
// that drops the Engine, or a harden pass that disarms sudo, is discovered in a
// running sandbox instead of in CI.
func TestBaseAssertsWhatItInherits(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))
	for _, probe := range []string{"command -v docker", "command -v dockerd", "test -u /usr/bin/sudo.ws"} {
		if !strings.Contains(src, probe) {
			t.Errorf("base does not verify %q at build time", probe)
		}
	}
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
