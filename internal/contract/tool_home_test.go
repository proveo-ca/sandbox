// SPEC: _spec/_plans/config-seeding-and-persistence.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/workspace"
)

// runToolHome sources the shared lib with a stubbed `uname -m` and prints the
// value of one accessor, so the bash halves can be asserted from Go.
func runToolHome(t *testing.T, bash, machine string, env map[string]string, expr string) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "uname"), []byte(
		"#!/bin/sh\ncase \"$1\" in -m) echo "+machine+" ;; *) echo Linux ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `export PATH="` + bin + `:/usr/bin:/bin"
source "$1/packages/lib/entrypoint-lib.sh"
printf '%s' "$(` + expr + `)"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_HOME=", "PROVEO_STATE_HOME=", "_PROVEO_TOOL_HOME=")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", expr, err, out)
	}
	return string(out)
}

// The arch fold names a DIRECTORY that a docker run and an sbx run on one host
// both reach, so the two halves of proveo must agree on the spelling exactly.
// A disagreement puts amd64 binaries where an arm64 sandbox resolves them
// through `command -v` — the wrong-arch trap the LSP eligibility check exists
// to prevent, arriving from the other side.
func TestContainerPlatformFoldMatchesGo(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	for _, machine := range []string{"x86_64", "amd64", "aarch64", "arm64", "riscv64", "ppc64le"} {
		t.Run(machine, func(t *testing.T) {
			t.Parallel()
			got := runToolHome(t, bash, machine, nil, "_proveo_container_platform")
			want := "linux-" + workspace.ParsePlatform("linux/"+machine).Arch
			if got != want {
				t.Errorf("_proveo_container_platform() with uname -m = %q gave %q, want %q "+
					"(internal/workspace.normalizeArch is the other half of this fold)", machine, got, want)
			}
		})
	}
}

// Step 1: one accessor, two backends. On docker PROVEO_STATE_HOME is unset and
// the answer must stay the agent home, or the backend that works today regresses.
func TestDurableHomePrefersTheHostPath(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)

	// A real directory, because entrypoint-lib.sh rewrites an unwritable HOME to
	// /tmp at source time — the same guard that makes an arbitrary run-as uid
	// usable, and it would otherwise silently answer this test for us.
	agent, host := t.TempDir(), t.TempDir()

	docker := runToolHome(t, bash, "aarch64",
		map[string]string{"HOME": agent}, "_proveo_durable_home")
	if docker != agent {
		t.Errorf("with no PROVEO_STATE_HOME the durable home = %q, want the agent home %q", docker, agent)
	}

	sbx := runToolHome(t, bash, "aarch64",
		map[string]string{"HOME": agent, "PROVEO_STATE_HOME": host},
		"_proveo_durable_home")
	if sbx != host {
		t.Errorf("with PROVEO_STATE_HOME set the durable home = %q, want the host path %q — "+
			"the agent home lives in the VM and `sbx rm` destroys it", sbx, host)
	}
}

// Step 2: the toolchain tree hangs off the durable root, namespaced by platform.
func TestToolHomeIsDurableAndPlatformNamespaced(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state := t.TempDir()

	got := runToolHome(t, bash, "aarch64",
		map[string]string{"HOME": t.TempDir(), "PROVEO_STATE_HOME": state}, "_proveo_tool_home")
	want := filepath.Join(state, "toolchains", "linux-arm64")
	if got != want {
		t.Fatalf("_proveo_tool_home() = %q, want %q", got, want)
	}
	if fi, err := os.Stat(filepath.Join(want, ".local", "bin")); err != nil || !fi.IsDir() {
		t.Errorf("the tool home was not created: %v", err)
	}

	// Two architectures on one durable root must not land on one tree.
	amd := runToolHome(t, bash, "x86_64",
		map[string]string{"HOME": t.TempDir(), "PROVEO_STATE_HOME": state}, "_proveo_tool_home")
	if amd == got {
		t.Errorf("arm64 and amd64 both resolved to %q — the namespace is not separating them", got)
	}
}

// An absent or read-only durable root must not stop a run from provisioning: it
// costs the persistence, not the tools.
func TestToolHomeFallsBackWhenTheDurableRootIsUnwritable(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory is still writable")
	}
	ro, agent := t.TempDir(), t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	got := runToolHome(t, bash, "aarch64",
		map[string]string{"HOME": agent, "PROVEO_STATE_HOME": ro}, "_proveo_tool_home")
	if got != agent {
		t.Errorf("_proveo_tool_home() = %q, want the agent home %q — an unusable durable root "+
			"must degrade to today's location, not skip provisioning", got, agent)
	}
}

// mise's five directories move as a group. A data dir without a config dir
// leaves `mise use -g` recording a global config the next run never reads, so
// the tools are on disk and nothing knows they are.
func TestToolPathExportsTheWholeMiseGroup(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state := t.TempDir()
	tool := filepath.Join(state, "toolchains", "linux-arm64")

	for _, v := range []struct{ name, want string }{
		{"MISE_DATA_DIR", filepath.Join(tool, ".local", "share", "mise")},
		{"MISE_CONFIG_DIR", filepath.Join(tool, ".config", "mise")},
		{"MISE_STATE_DIR", filepath.Join(tool, ".local", "state", "mise")},
		{"MISE_CACHE_DIR", filepath.Join(tool, ".cache", "mise")},
	} {
		got := runToolHome(t, bash, "aarch64",
			map[string]string{"HOME": t.TempDir(), "PROVEO_STATE_HOME": state},
			"_proveo_tool_path >/dev/null; printf '%s' \"$"+v.name+"\"")
		if got != v.want {
			t.Errorf("%s = %q, want %q", v.name, got, v.want)
		}
	}

	path := runToolHome(t, bash, "aarch64",
		map[string]string{"HOME": t.TempDir(), "PROVEO_STATE_HOME": state},
		"_proveo_tool_path >/dev/null; printf '%s' \"$PATH\"")
	for _, want := range []string{
		filepath.Join(tool, ".local", "bin"),
		filepath.Join(tool, ".local", "share", "mise", "shims"),
	} {
		if !strings.Contains(path, want) {
			t.Errorf("PATH does not carry %q:\n%s", want, path)
		}
	}
}

// The relocation is only real if nothing still installs into the agent home.
// One missed site is a tool that reinstalls on every sbx open while the rest
// persist, which reads as "persistence is flaky" rather than as a missed line.
func TestNoToolPathStillTargetsTheAgentHome(t *testing.T) {
	t.Parallel()
	src := entrypointLib(t)
	for _, frag := range []string{
		"${HOME}/.local", "$HOME/.local", "${HOME}/.go", "${HOME}/go",
	} {
		if strings.Contains(src, frag) {
			t.Errorf("entrypoint-lib.sh still writes tools to %s — toolchains belong under "+
				"_proveo_tool_home so they survive an sbx teardown", frag)
		}
	}
}
