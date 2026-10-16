// SPEC: _spec/internal/runlog/run-transcript.puml
// Package run holds the resolved contract of a single `proveo run` and the
// stages that build it.
package run

import (
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
	EgDir string // per-run state: the sbx kit, policy log and staged clone env
	Log   *runlog.Log

	Man          manifest.Manifest
	Start        string // the resolved input dir, before scoping
	InvocationWD string // where the operator actually stood; the env-file search starts here

	Posture posture.Posture

	Workspace WorkspaceSpec
	Creds     CredentialSpec
	Choices   ChoiceSpec
	Backend   BackendSpec

	// AgentLaunched separates "the agent ran and exited 0" from "no agent was
	// ever started" — `--print` and the not-ready paths both return nil.
	AgentLaunched bool
	Model         ModelSpec
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
	Promptable   bool   // a TTY, wizard on, not a dry run: the cache may seed a prompt
	EvidenceSet  bool   // the env file pinned evidence, so the cache must not override it
	PortsRoot    string // the workspace path dev ports are discovered under and remembered for
}

// BackendSpec is which backend won, and the add-ons that decision enables.
type BackendSpec struct {
	Clone        bool
	CloneOff     string
	CloneSource  workspace.WorktreeSource // set when a linked worktree clones through its main worktree
	CloneEnv     string                   // staged project .env the clone links, or ""
	CloneEnvBase string                   // the staged bytes at launch, for the teardown merge
	CloneEnvHost string                   // the host .env the staged copy came from
	CloneStrip   []string                 // brokered keys the staged copy dropped
	BrowserImage string
}

// ModelSpec is the local model, when --local-model asks for one.
type ModelSpec struct {
	HostLLM bool // served by the host's Ollama
}
