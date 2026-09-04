package sbx

import (
	"fmt"
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

// NamePrefix is what proveo calls its own sandboxes (run.go builds the sid as
// "proveo-<unix>-<pid>"). Used to tell proveo's sandboxes from an operator's own.
const NamePrefix = "proveo-"

// RunningNames lists proveo's sandboxes that sbx reports as running, and whether
// the listing could be read at all.
//
// It exists for `proveo clean --tools`. That prune's liveness gate saw only the
// docker egress sidecars, which an sbx run does not have — so on the backend
// that has no sidecars it always read "nothing is running", and the toolchain
// tree it prunes is the one a live sandbox copies itself into at teardown.
// Racing that copy is worse than either outcome alone: the store is left half
// written, which is a toolchain that satisfies `command -v` and fails to exec.
// SPEC: _spec/_plans/config-seeding-and-persistence.puml
//
// ok=false means the listing was unreadable while sbx IS installed. The caller
// must treat that as "may be live": for a destructive prune the safe direction
// is to hold back and say so, not to guess that nothing is running.
func RunningNames() (names []string, ok bool) {
	if _, err := lookPath(Binary); err != nil {
		return nil, true // sbx absent: there are no sandboxes, and that is a fact
	}
	out, err := sh.SandboxList()
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// The header row cannot collide: it is never prefixed like a proveo sid.
		if len(f) < 3 || !strings.HasPrefix(f[0], NamePrefix) {
			continue
		}
		if strings.ToLower(f[2]) == "running" {
			names = append(names, f[0])
		}
	}
	return names, true
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
	Clone  bool
	Mounts []Mount // workspace binds, passed POSITIONALLY
	// Publish is -p/--publish: sandbox ports mapped onto host loopback. Applied
	// when the sandbox is CREATED — sbx ignores it on a re-attach — which is why
	// it belongs here beside Clone rather than being added later.
	Publish []string
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
	for _, p := range cfg.Publish {
		args = append(args, "-p", p)
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
// the fetch that follows carries it. Only when the tree is dirty.
// SPEC: _spec/internal/sbx/clone-workspace.puml
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

// The browser viewport: the operator watching, or driving, the Chromium the
// agent is using — from the host, over the sandbox boundary. Two ports, because
// Chromium's DevTools endpoint refuses every non-loopback peer.
const (
	CDPRelayPort   = 9222 // the relay listens here, on every interface; this is what is published
	CDPBrowserPort = 9223 // Chromium's own DevTools port, loopback only
)

// BrowserCDPArgs is the AGENT_BROWSER_ARGS value that makes the agent's own
// Chromium expose CDP on a fixed port, preserving what the operator already set.
// Measured against agent-browser 0.36.0.
func BrowserCDPArgs(existing string) string {
	flag := fmt.Sprintf("--remote-debugging-port=%d", CDPBrowserPort)
	existing = strings.TrimSpace(existing)
	switch {
	case existing == "":
		return "--no-sandbox," + flag
	case strings.Contains(existing, "--remote-debugging-port="):
		return existing // the operator pinned their own; theirs wins
	}
	return existing + "," + flag
}

// cdpRelay is the loopback relay, as a python3 -c program: accept on every
// interface, dial Chromium on loopback, pipe both ways. python3 is in the base
// image and this is stdlib only, so it needs nothing installed and no image
// change — which is why it travels as an argv rather than as a file.
const cdpRelay = `
import socket,sys,threading
L,T=int(sys.argv[1]),int(sys.argv[2])
def pipe(a,b):
    try:
        while True:
            d=a.recv(65536)
            if not d: break
            b.sendall(d)
    except Exception: pass
    finally:
        for s in (a,b):
            try: s.close()
            except Exception: pass
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(("0.0.0.0",L)); s.listen(64)
while True:
    c,_=s.accept()
    try: u=socket.create_connection(("127.0.0.1",T))
    except Exception:
        c.close(); continue
    for a,b in ((c,u),(u,c)):
        threading.Thread(target=pipe,args=(a,b),daemon=True).start()
`

// CDPRelayArgs runs the relay inside the sandbox, in the FOREGROUND so the run
// owns its lifetime. `-w /` for the reason SaveStateArgs pins it.
func CDPRelayArgs(name string) []string {
	return []string{"exec", "-w", "/", name, "--", "python3", "-c", cdpRelay,
		strconv.Itoa(CDPRelayPort), strconv.Itoa(CDPBrowserPort)}
}

// CloneLiftNothing is the exit status CloneLiftArgs reserves for "the agent wrote
// nothing there": the directory does not exist in the clone, so there is nothing
// to unpack and nothing went wrong. Every other non-zero status is a failure.
const CloneLiftNothing = 3

// CloneLiftArgs streams a directory the agent wrote INSIDE the clone out of the
// sandbox as a tar archive on stdout, for the host to unpack under repoRoot. It
// exists because clone mode cannot mount that directory live. `-w /` for the
// same reason SaveStateArgs pins it; rel is relative to the clone root.
// SPEC: _spec/internal/sbx/clone-workspace.puml
func CloneLiftArgs(name, workdir, rel string) []string {
	return []string{"exec", "-w", "/", name, "--", "bash", "-c",
		"cd " + bashQuote(workdir) + " && { [ -d " + bashQuote(rel) + " ] || exit " +
			strconv.Itoa(CloneLiftNothing) + "; } && tar -cf - " + bashQuote(rel)}
}

// bashQuote single-quotes s for a bash -c script: host paths carry spaces.
func bashQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SaveStateArgs is the teardown copy-out: one `sbx exec` that runs the shared
// syncs inside the sandbox before `sbx rm` takes the volumes with it. `-w /` is
// load-bearing.
//
// Two syncs, one exec. Resume state and the toolchain tree are copied out the
// same way and at the same moment, and neither may skip the other: they are
// joined with `;` rather than `&&` so a failed transcript copy still lets the
// toolchains land, and the exit status is the state sync's — losing yesterday's
// transcripts is the louder failure, and it is the one the caller already
// reports on.
// SPEC: _spec/internal/sbx/state-sync.puml, _spec/_plans/config-seeding-and-persistence.puml
func SaveStateArgs(name string) []string {
	return []string{"exec", "-w", "/", name, "--", "bash", "-c",
		". /entrypoint-lib.sh && { proveo_sync_tools save || true; }; proveo_sync_state save"}
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

// authLoginArgs is the in-sandbox sign-in each sbx agent performs. sbx's own
// docs name this as what populates its oauth slot; nothing else does.
// SPEC: _spec/internal/sbx/oauth-provisioning.puml
var authLoginArgs = map[string][]string{
	"claude": {"auth", "login"},
}

// AuthLoginArgs renders the sign-in sbx captures into its own credential store,
// or nil when this agent has no such flow. An argv only: running it needs a
// terminal, so the caller owns that.
func AuthLoginArgs(agent, workspace string) []string {
	sub, ok := authLoginArgs[agent]
	if !ok || agent == "" || workspace == "" {
		return nil
	}
	args := []string{"run", agent, workspace, "--"}
	return append(args, sub...)
}
