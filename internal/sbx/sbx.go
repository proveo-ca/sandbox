// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/_experiments/docker-sandbox.puml, _spec/internal/sbx/state-sync.puml, _spec/minimum_requirements.puml
package sbx

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

// Binary is the Docker Sandboxes CLI this package drives.
const Binary = "sbx"

// MinVersion is the oldest sbx whose CLI surface this package targets. proveo
// owns the version rather than leaving it to whatever the operator's package
// manager happens to hold: sbx is pre-GA and its surface moves, so a host that
// is merely "installed" is not a host that can be driven. v0.35 → v0.39 alone
// moved workspaces from `-v host:container` to positional paths, replaced the
// image positional with `--template`, made `rm` refuse to run non-interactively
// without `--force`, and rewrote the Kit schema — every one of which fails at
// run time, deep inside a sandbox the operator cannot see.
const MinVersion = "0.39.0"

// Test seams; production leaves these at their defaults.
var (
	lookPath  = exec.LookPath
	goos      = runtime.GOOS
	goarch    = runtime.GOARCH
	kvmDevice = "/dev/kvm"
	stat      = os.Stat
	numCPU    = runtime.NumCPU
)

// dockerInfoTimeout bounds the one daemon call MemoryLimit makes. Generous enough
// for a healthy daemon under load, far below the point where an operator concludes
// the tool is broken.
const dockerInfoTimeout = 10 * time.Second

// Sandbox memory bounds, in bytes. The ceiling matches sbx's own cap; the floor is
// the point below which a limit says more about a broken daemon than about policy,
// so sbx's default is left to apply instead.
const (
	maxSandboxMemory = 32 << 30
	minSandboxMemory = 1 << 30
)

// MemoryLimit returns the -m value for a sandbox run, or "" to leave sbx's default
// in place.
//
// sbx defaults a sandbox to 50% of HOST memory. Wherever Docker runs in a VM — every
// macOS and Windows host — that number can exceed the VM's entire allocation, and a
// limit larger than the machine it sits in cannot bind. The container then grows past
// what the VM has, the VM's OOM killer takes a process, and the run dies with SIGKILL
// (137) carrying no OOMKilled flag and no message: the failure is invisible in exactly
// the place an operator would look for it.
//
// Deriving the same 50% from the daemon's own MemTotal is correct on every platform,
// because on native Linux MemTotal IS host memory and the result is unchanged.
const (
	EnvMemory    = "PROVEO_SBX_MEMORY"
	EnvCPUs      = "PROVEO_SBX_CPUS"
	EnvInstances = "PROVEO_SBX_INSTANCES"
)

const (
	defaultMemoryShare  = 2
	cpuBurstDivisor     = 2
	maxSandboxInstances = 64
)

// CPULimit returns the --cpus value for a sandbox run, or 0 to leave sbx's
// default in place. See _spec/minimum_requirements.puml.
var verLine = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// HasTemplate reports whether the sandbox runtime's store holds the SAME image
// the host engine currently has under that reference.
//
// The store prints COLUMNS, not references, and qualifies the repository with a
// registry:
//
//	REPOSITORY                     TAG      IMAGE ID       FLAVOR   CREATED
//	docker.io/proveo/egress-proxy  latest   4ee370d17e72             …
//
// So a substring test for "proveo/egress-proxy:latest" never matches, and one for
// the bare repository matches any tag. Repository and tag are compared
// separately, with the registry qualifier trimmed.
//
// **Identity, not just presence.** Matching repo+tag alone treats a REBUILT
// :latest as already loaded, so the sandbox keeps running the image the store
// happened to receive first — silently, and forever. That is not hypothetical: a
// standardisation of the workspace layout rebuilt these images, and a sandbox
// started afterwards had no /app at all because the store still held the
// pre-rebuild copy.
//
// **Identity is read from a RECEIPT, not from the store's IMAGE ID column**, and
// that distinction is the whole reason this function is not two lines. `sbx
// create` re-bakes the template it was given and rewrites the tag to point at the
// baked result, so the ID under a reference stops being the ID that was loaded
// the moment a sandbox starts — measured: load puts 2ca281d785dc under
// proveo/claudecode:latest, one `sbx create` later the same row reads
// 5fcb2266417f. Comparing against that column therefore mismatches forever after
// the first run and re-loads a multi-GB tar EVERY time, which is worse than the
// staleness it was written to prevent. The receipt records what proveo handed
// over, so a rebuild still reloads and a bake does not.
//
// When the host engine cannot name the image (not built locally), identity is
// unknowable and presence is the best available answer — that path is for an
// image pulled straight into the store, which no rebuild invalidates.
var templateReceiptDir = func() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "proveo", "sbx-templates")
}

// ImageEntrypoint is what the Kit must run to reproduce a `docker run` of this
// image. It is READ FROM THE IMAGE rather than restated per def, because the two
// would drift the moment a def changed its entrypoint and nothing would notice
// until a sandbox ran the wrong command.
//
// It matters at all because sbx's own agent command would otherwise replace it —
// and proveo's entrypoint is where the seeded guide file, the model-alias
// bridging, the LSP wiring and the subagent set come from. Losing it would leave
// the image in place and the harness gone.
var BuiltinAgents = []string{
	"claude", "codex", "copilot", "cursor", "docker-agent",
	"droid", "gemini", "kiro", "opencode", "shell",
}

// AgentName is the agent a target's Kit declares, and it is namespaced rather
// than checked against BuiltinAgents: sbx adds built-ins over time, so a
// collision test against today's list would pass until the day sbx ships an
// agent named like one of ours and every run of it starts failing. The prefix
// cannot collide by construction.
//
// The failure it avoids is quiet: cursor's runs died ~12s in with the session
// gone before the shell and nothing captured, which read as a sandbox timeout
// for as long as the error stayed unread.
const StateHomeVar = "PROVEO_STATE_HOME"

// SaveStateArgs builds the invocation that lifts resume state out of a sandbox
// before it is destroyed. It runs INSIDE the sandbox, sourcing the same shell
// library the seed uses, so the list of directories worth saving is defined once
// and cannot drift between the way in and the way out.
const (
	BaselineAllowAll = "allow-all"
	BaselineBalanced = "balanced"
	BaselineDenyAll  = "deny-all"
)

// SecretSet injects one credential host-side.
const KitSchemaVersion = 2

// Kit is the posture rendered as a Kit spec.yaml (kit-spec v2).
//
// It is a MIXIN, not a sandbox. A `kind: sandbox` Kit declares an agent, and sbx's
// agent list is closed: an agent it does not already ship gets no artifact, so its
// binding gate is skipped and the interactive session is dropped seconds in. A
// mixin declares no agent, composes onto one sbx already knows, and contributes
// only what proveo actually owns — reachability, resolved env, and the seed step.
//
// SchemaVersion is a STRING because the spec says so (SPEC-v2.md). Credentials are
// deliberately absent: the built-in agent declares its own, and a mixin repeating a
// service is rejected outright ("defined in both").
const KitSchemaVersionV2 = "2"

// SeedCommand is the seed step as a Kit setup.startup entry. Both backends reach
// the same function; only the invocation differs.
