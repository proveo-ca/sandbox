// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
package sbx

import (
	"strings"
)

// KitCommand is one setup step. `startup` takes a LIST command (install takes a
// string) — the two spellings differ and the loader is strict about it.
type KitCommand struct {
	Command     []string `yaml:"command"`
	User        string   `yaml:"user,omitempty"`
	Background  bool     `yaml:"background,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

// The seed phases proveo-seed and proveo_seed accept.
// SPEC: _spec/internal/sbx/startup-dispatch.puml
const (
	SeedPhaseAll       = "all"
	SeedPhaseRelease   = "release"
	SeedPhaseProvision = "provision"
	SeedPhaseNone      = "none"
)

// SeedBinary is the path every sbx image must COPY the seed to. SeedCommand
// names it; a non-zero exit from that binary must not reach sbx's dispatcher.
const SeedBinary = "/usr/local/bin/proveo-seed"

// seedNeverAbort is the -c script SeedCommand runs.
const seedNeverAbort = `set +e
if [ "$2" = provision ] && [ -e "${PROVEO_SEED_DONE_MARKER:-/dev/shm/proveo-seed-done}" ]; then
  exit 0
fi
/usr/local/bin/proveo-seed "$@"
rc=$?
if [ "$rc" -ne 0 ]; then
  printf 'proveo-seed: exit %s — continuing; a seed miss must not tear the sandbox down\n' "$rc" >&2
fi
exit 0`

// SeedCommand runs the seed up to the agent's release, in the foreground.
func SeedCommand(target string) KitCommand {
	return seedPhaseCommand(target, SeedPhaseRelease, "proveo: compose subagents, settings and workspace trust")
}

// SeedFullCommand runs the whole seed in the foreground.
func SeedFullCommand(target string) KitCommand {
	return seedPhaseCommand(target, SeedPhaseAll, "proveo: compose subagents, settings and workspace trust, then provision")
}

// SeedProvisionCommand runs the seed's installs beside the agent.
func SeedProvisionCommand(target string) KitCommand {
	c := seedPhaseCommand(target, SeedPhaseProvision, "proveo: toolchains, dependencies and language servers, beside the agent")
	c.Background = true
	return c
}

func seedPhaseCommand(target, phase, description string) KitCommand {
	return KitCommand{
		Command:     []string{"/bin/sh", "-c", seedNeverAbort, "proveo-seed", target, phase},
		User:        "1000",
		Description: description,
	}
}

// WorktreeDirCommand creates, as root, the directory a worktree clone adds its
// linked worktree at: sbx makes mount-point parents root-owned, so uid 1000
// cannot create it. It never fails the startup.
// SPEC: _spec/internal/sbx/clone-workspace.puml
func WorktreeDirCommand(dir string) KitCommand {
	return KitCommand{
		Command:     []string{"/bin/sh", "-c", `install -d -o 1000 -g 1000 "$1" || true`, "proveo-worktree-dir", dir},
		User:        "root",
		Description: "proveo: the linked worktree's directory, owned by the agent",
	}
}

// SeedEntrypointCommand runs the def's entrypoint up to, not including, the
// agent launch: PROVEO_SEED_ONLY makes proveo_exec_agent return instead of
// exec, so a mixin kit gets the def's own wiring while sbx launches the agent.
// PROVEO_SEED_PHASE=none skips the shared seed the other startup steps run.
// An image with neither entrypoint exits 0.
func SeedEntrypointCommand(target string) KitCommand {
	return KitCommand{
		Command: []string{"/usr/bin/env", "PROVEO_SEED_ONLY=1", "PROVEO_SEED_PHASE=" + SeedPhaseNone,
			"/bin/sh", "-c", strings.Join(defEntrypoint(target), "; "), "proveo-" + target},
		User:        "1000",
		Description: "proveo: the def's own wiring, launch skipped",
	}
}

// CommandNamesSeed reports whether a Kit startup argv invokes the seed, whether
// as the binary itself or inside the host-side never-abort wrapper.
func CommandNamesSeed(cmd []string) bool {
	for _, a := range cmd {
		if strings.Contains(a, "proveo-seed") {
			return true
		}
	}
	return false
}
