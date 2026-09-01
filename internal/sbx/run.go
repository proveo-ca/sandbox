package sbx

import (
	"strconv"
	"strings"
)

func Exists(name string) bool {
	if name == "" {
		return false
	}
	out, err := sh.SandboxList()
	if err != nil {
		// Unreadable listing: claim it exists, so a retry that might double-run an
		// agent is never taken on a guess.
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range strings.Fields(line) {
			if f == name {
				return true
			}
		}
	}
	return false
}

// sandboxList reads the sandbox listing. Overridable in tests.
func Running(name string) bool {
	if name == "" {
		return false
	}
	out, err := sh.SandboxList()
	if err != nil {
		// Unreadable listing: claim NOT running. Callers use this to decide whether
		// to trust what a copy-out produced, and the optimistic guess is the one
		// that lets a restart's leftovers pass as the run's own evidence.
		return false
	}
	return statusOf(string(out), name) == "running"
}

// statusOf reads the STATUS column of `sbx ls` for one sandbox. The header row
// cannot collide: its first field is the literal "SANDBOX".
func statusOf(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == name {
			return strings.ToLower(f[2])
		}
	}
	return ""
}

// secretList reads the credential store listing. Overridable in tests.
func StoredSecretNames() []string {
	out, err := sh.SecretList()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// SCOPE TYPE NAME SECRET — the header's NAME column is the literal "NAME".
		if len(f) < 3 || f[2] == "NAME" {
			continue
		}
		names = append(names, f[2])
	}
	return names
}

// imageEntrypoint reads the image's own ENTRYPOINT. Overridable in tests.
func ImageEntrypoint(image string) []string { return sh.ImageEntrypoint(image) }

// Mount is a workspace bind into the sandbox VM.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// BuiltinAgents are the agent names sbx defines itself. A kind: sandbox Kit
// DECLARES an agent, and sbx refuses to let a declaration shadow one of these —
// `agent "cursor" is already registered (built-in agents cannot be overridden by
// a kit)`. Recorded here for the test that proves proveo never lands on one;
// nothing reads it at run time, because AgentName dodges the whole set.
func AgentName(target string) string { return "proveo-" + target }

// RunConfig describes one agent run on the sbx backend.
type RunConfig struct {
	Name string // sandbox/session name; empty lets sbx assign one
	// Agent is sbx's own agent name (claude · cursor · shell · …) and it is
	// MANDATORY: the first positional is parsed as an agent, so leaving it empty
	// makes sbx read the first workspace path as an agent name and refuse the run
	// with "is not a sandbox or known agent".
	Agent  string
	KitDir string // directory holding the rendered Kit spec.yaml
	Image  string // template image, passed as -t
	Memory string // -m limit; empty leaves sbx's host-derived default in place
	CPUs   int    // --cpus; zero leaves sbx's default (every host CPU) in place
	// Clone runs the agent on a private in-container CLONE of the host repo
	// (--clone) rather than on the mounted tree itself. Creation-time only: sbx
	// ignores it when re-attaching, which is why it belongs in the run config
	// rather than being toggled later.
	Clone   bool
	Mounts  []Mount  // workspace binds, passed POSITIONALLY
	Env     []string // non-secret KEY=VALUE (or bare NAME) passthrough
	Command []string // trailing agent command (after "--")
}

// RunArgs builds the sbx invocation for cfg:
//
//	sbx run [flags] AGENT [PATH[:ro]...] [-- AGENT_ARGS...]
//
// The shape is not docker's, and the differences are the ones that made a
// docker-shaped argv fail one flag at a time:
//
//	workspaces  POSITIONAL paths, not -v. `--volume` exists only with --cloud, and
//	            a local sandbox mounts each path at its own HOST path over
//	            virtiofs — there is no container-side target to name.
//	image       -t/--template. The first positional is the AGENT, so passing an
//	            image reference there is read as an unknown agent name.
//	workdir     no such flag. The cwd is the first workspace; PROVEO_WORKDIR in the
//	            environment is how a harness is told where it landed.
//
// Flags are emitted before the positionals: the usage line puts them there, and
// an interspersed parse is not something to rely on from a pre-GA CLI.
// CreateArgs builds the non-attaching form: `sbx create` makes the sandbox and
// returns, where `sbx run` stays attached to the agent for the session's life.
// See _spec/minimum_requirements.puml.
func CreateArgs(cfg RunConfig) []string {
	cfg.Command = nil
	return append([]string{"create"}, RunArgs(cfg)[1:]...)
}

func RunArgs(cfg RunConfig) []string {
	args := []string{"run"}
	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}
	if cfg.KitDir != "" {
		args = append(args, "--kit", cfg.KitDir)
	}
	if cfg.Image != "" {
		args = append(args, "-t", cfg.Image)
	}
	if cfg.Memory != "" {
		args = append(args, "-m", cfg.Memory)
	}
	if cfg.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(cfg.CPUs))
	}
	if cfg.Clone {
		args = append(args, "--clone")
	}
	for _, e := range cfg.Env {
		args = append(args, "-e", e)
	}
	if cfg.Agent != "" {
		args = append(args, cfg.Agent)
	}
	for _, m := range cfg.Mounts {
		spec := m.Host
		if m.ReadOnly {
			spec += ":ro"
		}
		args = append(args, spec)
	}
	if len(cfg.Command) > 0 {
		args = append(args, "--")
		args = append(args, cfg.Command...)
	}
	return args
}

// CloneRemote is the git remote sbx adds to the host repository for a clone-mode
// sandbox: the in-VM clone, reachable for fetch while the sandbox exists.
func CloneRemote(name string) string { return "sandbox-" + name }

// CloneRefs is where a clone's branches are kept on the host after the sandbox is
// gone. NOT refs/remotes/<remote>/: removing the remote removes those, and `sbx rm`
// is exactly the moment they are needed. refs/proveo/<name>/<branch> survives it.
func CloneRefs(name string) string { return "refs/proveo/" + name }

// CloneSnapshotArgs commits whatever the agent left UNCOMMITTED in the clone, so
// the fetch that follows carries it. Only when there is something to commit — a
// clean tree gets no empty commit — and under an author that says who did it.
// `sbx rm` drops the clone with the VM, and sbx's own guidance is to fetch or
// push before removing; an agent that stopped mid-edit has nothing to push.
func CloneSnapshotArgs(name, workdir string) []string {
	return []string{"exec", "-w", workdir, name, "--", "bash", "-c",
		"git add -A && (git diff --cached --quiet || git -c user.name=proveo -c user.email=proveo@sandbox " +
			"commit -q -m 'proveo: uncommitted work at teardown (left in the clone by the agent)')"}
}

// CloneFetchArgs is the host-side git argv that lifts every branch of the clone
// into CloneRefs. --no-tags: the clone's tags are the host's own, fetched back.
func CloneFetchArgs(repoRoot, name string) []string {
	return []string{"-C", repoRoot, "fetch", "--no-tags", "--quiet", CloneRemote(name),
		"+refs/heads/*:" + CloneRefs(name) + "/*"}
}

// StateHomeVar names the host directory that resume state is copied to and from.
//
// It is deliberately NOT HOME. Redirecting HOME on this backend orphans the
// credential sbx's proxy writes into the image's home, which is what made the
// agent report "Not logged in" on ladder rung 3. This variable moves only the
// state, and only by copy.
func SaveStateArgs(name string) []string {
	return []string{"exec", name, "--", "bash", "-c",
		". /entrypoint-lib.sh && proveo_sync_state save"}
}

// RemoveArgs builds the ephemeral teardown invocation (VM + images + volumes).
//
// --force is not optional for a script: `sbx rm` asks for confirmation and would
// otherwise block on a prompt no run can answer, and it also declines to remove a
// sandbox still considered in use.
func RemoveArgs(name string) []string {
	return []string{"rm", "--force", name}
}

// NotFound reports whether output from `sbx rm` means the sandbox was never
// there. A run whose `sbx run` failed before creating anything hits this on the
// way out, and warning about a failed teardown then points the operator at the
// wrong thing entirely.
func NotFound(out string) bool {
	return strings.Contains(strings.ToLower(out), "not found")
}
