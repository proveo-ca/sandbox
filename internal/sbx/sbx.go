// SPEC: _spec/internal/sbx/sandbox-backend.puml,
// _spec/_experiments/docker-sandbox.puml, _spec/internal/sbx/state-sync.puml,
// _spec/minimum_requirements.puml
//
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

var (
	lookPath  = exec.LookPath
	goos      = runtime.GOOS
	goarch    = runtime.GOARCH
	kvmDevice = "/dev/kvm"
	stat      = os.Stat
	numCPU    = runtime.NumCPU
)

const dockerInfoTimeout = 10 * time.Second

const (
	maxSandboxMemory = 32 << 30
	minSandboxMemory = 1 << 30
)

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

var verLine = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

var templateReceiptDir = func() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "proveo", "sbx-templates")
}

var BuiltinAgents = []string{
	"claude", "codex", "copilot", "cursor", "docker-agent",
	"droid", "gemini", "kiro", "opencode", "shell",
}

const StateHomeVar = "PROVEO_STATE_HOME"

const (
	BaselineAllowAll = "allow-all"
	BaselineBalanced = "balanced"
	BaselineDenyAll  = "deny-all"
)

const KitSchemaVersion = 2

const KitSchemaVersionV2 = "2"
