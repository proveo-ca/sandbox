// Package run holds the resolved contract of a single `proveo run` and the
// stages that build it.
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
// later stage reads.
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

// WorkspaceSpec is WHERE the run happens: the dirs, the repo, and the mount
// plan they imply.
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
	HostEnvFile        string
	Lookup             func(string) string // env-then-file; the ONLY credential read in a run
	Detected           []string
	Brokered           []string
	BrokerFile         string
	BrokerKeyNames     []string
	Env                []string
	Child              credentials.ChildEnv
	FileLogin          bool
	LoginNeedsRefresh  bool
	StoreHeld          []string // names sbx's store holds; proveo sees that they exist, not what they hold
	LoggedIn           bool
	AuthMissingAtStart []manifest.EnvVar
	Keychain           credentials.KeychainLogin

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
	Sbx          bool
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
