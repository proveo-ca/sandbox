// Package run holds the resolved contract of a single `proveo run` and the
// stages that build it. See _spec/internal/run/run-spec.puml.
package run

import (
	"io/fs"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// Spec is what the plan calls RunSpec: the values a run resolves once and every
// later stage reads. It sits ABOVE the two backend inputs rather than merging
// with them — sandbox.Input and dockeregress.Input are each built FROM a Spec at
// the seam, so neither backend can grow a field the other has to ignore.
//
// These fields were doRun's locals. That was the whole problem: a 569-line
// function's contract lived in its stack frame, so nothing could be moved
// without first proving what a given line still needed. Enumerating them here
// changes no behaviour — it only makes the contract addressable.
type Spec struct {
	// ── identity ───────────────────────────────────────────────────────────
	Sid   string // proveo-<unix>-<pid>; names the sandbox, the egress dir and the run log
	EgDir string // per-run state: inject/, review/
	UID   string // host uid, so container-written files land owned by the operator
	GID   string
	Log   *runlog.Log

	Man          manifest.Manifest
	SquidConfig  fs.FS  // the root package's embedded squid config, passed down rather than imported up
	Start        string // the resolved input dir, before scoping
	InvocationWD string // where the operator actually stood; the env-file search starts here

	// ── workspace ──────────────────────────────────────────────────────────
	Scope         workspace.Scope
	RepoRoot      string
	SubScope      string // the picked project, relative to RepoRoot
	WS            workspace.MountSpec
	Mounts        []runner.Mount
	Workdir       string
	Links         []workspace.Link
	WorktreeLinks string // container-correct pointer dir, or "" to fall back to GIT_DIR

	// ── credentials ────────────────────────────────────────────────────────
	HostEnvFile        string
	Lookup             func(string) string // env-then-file; the ONLY credential read in a run
	Detected           []string
	Brokered           []string
	BrokerFile         string
	BrokerKeyNames     []string
	Env                []string
	FileLogin          bool
	LoginNeedsRefresh  bool
	StoreHeld          []string // names sbx's store holds; proveo sees that they exist, not what they hold
	LoggedIn           bool
	AuthMissingAtStart []manifest.EnvVar

	HomePlan proveohome.Plan

	// ── choices ────────────────────────────────────────────────────────────
	SettingsRoot string
	Settings     *agentsettings.Store
	Promptable   bool // a TTY, wizard on, not a dry run: the cache may seed a prompt
	EvidenceSet  bool // the env file pinned evidence, so the cache must not override it

	Posture posture.Posture

	// ── backend ────────────────────────────────────────────────────────────
	Sbx           bool
	WantDind      bool
	DindScope     string
	DindOfferable bool
	BrowserImage  string

	// ── local model ────────────────────────────────────────────────────────
	ModelsDir  string
	HostOllama bool
	OllamaGPU  bool

	// ── docker+egress execution ────────────────────────────────────────────
	Host         runner.HostInfo
	Browser      bool
	PidsLimit    int
	ReviewSocket string
}
