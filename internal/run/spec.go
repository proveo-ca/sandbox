// Package run holds the resolved contract of a single `proveo run` and the
// stages that build it. See _spec/internal/run/run-spec.puml.
package run

import (
	"io/fs"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/credentials"
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
//
// It was flat at first, and 47 fields wide, which made every stage's surface the
// whole run. The groups below were already comment headings; making them TYPES
// costs one selector at each use and buys a stage signature that says what it
// touches. What stays at the top level is what genuinely crosses every stage:
// the run's identity, the manifest, and the posture it renders.
type Spec struct {
	Sid   string // proveo-<unix>-<pid>; names the sandbox, the egress dir and the run log
	EgDir string // per-run state: inject/, review/
	UID   string // host uid, so container-written files land owned by the operator
	GID   string
	Log   *runlog.Log

	Man          manifest.Manifest
	SquidConfig  fs.FS  // the root package's embedded squid config, passed down rather than imported up
	ModelBridges fs.FS  // and its model bridge tables — same reason: internal/ never imports the root
	Start        string // the resolved input dir, before scoping
	InvocationWD string // where the operator actually stood; the env-file search starts here

	Posture posture.Posture

	Workspace WorkspaceSpec
	Creds     CredentialSpec
	Choices   ChoiceSpec
	Backend   BackendSpec
	Model     ModelSpec
	Docker    DockerSpec
}

// WorkspaceSpec is WHERE the run happens: the dirs, the repo, and the mount plan
// they imply.
type WorkspaceSpec struct {
	Scope         workspace.Scope
	RepoRoot      string
	SubScope      string // the picked project, relative to RepoRoot
	WS            workspace.MountSpec
	Mounts        []runner.Mount
	Workdir       string
	Links         []workspace.Link
	WorktreeLinks string // container-correct pointer dir, or "" to fall back to GIT_DIR
}

// CredentialSpec is WHAT the agent may authenticate with, and what carries it.
type CredentialSpec struct {
	HostEnvFile    string
	Lookup         func(string) string // env-then-file; the ONLY credential read in a run
	Detected       []string
	Brokered       []string
	BrokerFile     string
	BrokerKeyNames []string
	Env            []string
	// Child holds the VALUES behind every bare `-e NAME` in Env, for the launch
	// exec alone. Never printed, never in argv: --print renders Env, and Child is
	// the half that only the child process ever sees.
	Child              credentials.ChildEnv
	FileLogin          bool
	LoginNeedsRefresh  bool
	StoreHeld          []string // names sbx's store holds; proveo sees that they exist, not what they hold
	LoggedIn           bool
	AuthMissingAtStart []manifest.EnvVar
	// Keychain is what the HOST secret store holds — metadata only, and it feeds
	// no decision. See _spec/internal/secretref/secret-references.puml.
	Keychain credentials.KeychainLogin

	HomePlan proveohome.Plan
}

// ChoiceSpec is what the operator was asked, and whether they could be asked.
type ChoiceSpec struct {
	SettingsRoot string
	Settings     *agentsettings.Store
	Promptable   bool // a TTY, wizard on, not a dry run: the cache may seed a prompt
	EvidenceSet  bool // the env file pinned evidence, so the cache must not override it
}

// BackendSpec is which backend won, and the add-ons that decision enables.
type BackendSpec struct {
	Sbx bool
	// Clone is the EFFECTIVE workspace mode: true when the agent edits a private
	// in-VM clone, false when it edits the mounted checkout. CloneOff says why the
	// clone default did not apply ("" when it did, or when nothing asked for it).
	Clone        bool
	CloneOff     string
	BrowserImage string
}

// ModelSpec is the local-model sidecar, when --local-model asks for one.
type ModelSpec struct {
	ModelsDir  string
	HostOllama bool
	OllamaGPU  bool
}

// DockerSpec is what only the docker+egress path needs.
type DockerSpec struct {
	Host         runner.HostInfo
	Browser      bool
	PidsLimit    int
	ReviewSocket string
}
