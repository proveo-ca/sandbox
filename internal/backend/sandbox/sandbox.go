// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/internal/sbx/sandbox-backend.puml
//
// Package sandbox is the sbx backend: it PLANS a run (Spec) and it EXECUTES one
// (Run). That split already existed as two functions in cmd/proveo — Spec pure
// over its Input, Run the half that touches the world — and this package only
// gives it a boundary and a name. No logic moved between the two.
//
// Input carries values, never the CLI struct. It used to hold runParams whole,
// which made every function here reachable from cobra and back: the dependency
// closure was 98 declarations, essentially all of main.go. It is now the eleven
// values these functions actually read, which is the shape move 6 generalises
// into a RunSpec.
package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/engine"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// EvidenceVar and unallowlistedProbe are this backend's own: the first is the
// name it writes into the Kit environment, the second the host it asks sbx about
// to learn whether the daemon's baseline allows everything.
const (
	EvidenceVar        = "PROVEO_AGENT_EVIDENCE"
	unallowlistedProbe = "proveo-egress-probe.invalid"
)

// Enabled reports whether the sandbox backend may be selected at all.
//
// PROVEO_SBX=off pins the docker+egress path, and it exists because nothing else
// can say that non-interactively. The add-on is default-ON and only a
// remembered or prompted answer turns it off — but a headless run no longer
// reads the choice cache (that cache seeds a prompt, and there is none), so
// without this knob a host with sbx installed has no way to run on docker: not
// for a script, not for CI, and not for a test that needs to inspect the docker
// plan it is asserting against.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_SBX"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

// ReportUnavailable warns that the run fell back, naming the host engine.
func ReportUnavailable(why string) {
	ui.Section(ui.SectionExecution)
	ui.Warnf("docker sandbox unavailable (%s) — falling back to docker+egress", why)
	if eng := engine.Detect(); eng.Kind != engine.Unknown {
		ui.Notef("engine: %s (%s)", eng.Label(), eng.Isolation())
	}
	if cmd := sbx.InstallCmd(sbx.Installed()); cmd != "" {
		if sbx.Installed() {
			ui.Notef("proveo targets sbx %s or newer:", sbx.MinVersion)
		} else {
			ui.Notef("sbx is standalone and does not need Docker Desktop:")
		}
		ui.Notef("  %s", cmd)
	}
}

// Ensure brings the sbx CLI up to the version this build drives, so the
// operator is not the one tracking a pre-GA tool's releases. It returns whether
// the backend is usable and, when not, why.
//
// The install is CONFIRMED, never silent: it mutates the host outside proveo's
// own state, so it follows the same gate as a missing sidecar image
// (PROVEO_AUTO_PROVISION, else a TTY prompt, else declined). Declining is not an
// error — the run falls back to docker+egress and says so.
func Ensure(confirm func(string) bool) (bool, string) {
	ok, why := sbx.Available()
	if ok {
		return true, ""
	}
	install := sbx.InstallCmd(sbx.Installed())
	if install == "" {
		return false, why // nothing to offer on this platform
	}
	verb := "install"
	if sbx.Installed() {
		verb = "upgrade"
	}
	if !confirm(fmt.Sprintf("%s the docker sandboxes CLI (%s)?", verb, install)) {
		return false, why
	}
	ui.Section(ui.SectionStarting)
	ui.Appf("%sing sbx: %s", verb, install)
	c := exec.Command("bash", "-lc", install)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return false, fmt.Sprintf("%s failed: %v", verb, err)
	}
	return sbx.Available()
}

// Ready resolves the backend for a real run, provisioning the CLI when the
// operator allows it. A dry run only ever REPORTS: --print must not install
// anything, or `--print` stops being a way to inspect a plan safely.
func Ready(printOnly bool, confirm func(string) bool) (bool, string) {
	if printOnly {
		return sbx.Available()
	}
	return Ensure(confirm)
}

// WorkspaceBinds is every bind sbx can take as a positional workspace.
//
// sbx accepts several workspaces and mounts each at its own HOST path, so the
// workspace, the output dir, a --data-dir and proveo home all travel: their
// container path was never load-bearing on its own. Home comes with a condition —
// HOME has to be repointed at the host path, which Home does below — because
// the harness finds its config through HOME rather than through /proveo-home
// literally.
//
// What cannot travel is a bind NESTED under home: the gh config sits at
// /proveo-home/.config/gh on docker, and as its own positional workspace it would
// land at its own host path instead, nowhere the harness looks. It is dropped
// rather than mounted somewhere useless — the same conclusion
// PROVEO_MOUNT_GH_CONFIG=0 reaches deliberately.
// StateHome reports the host path the run publishes for resume state, or ""
// when this run has no proveo home to persist into.
func StateHome(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, sbx.StateHomeVar+"="); ok {
			return v
		}
	}
	return ""
}

// SbxRun executes one sbx invocation and returns its combined output. Injectable
// so the copy-out below is testable without a daemon.
func SbxRun(args ...string) (string, error) {
	out, err := exec.Command(sbx.Binary, args...).CombinedOutput()
	return string(out), err
}

// SaveState copies the sandbox-local resume state — transcripts included —
// into the mounted proveo home.
//
// Both exits need it, for different reasons. On the way out of a SUCCESSFUL run it
// has to happen before teardown, because `sbx rm` takes the volumes with it. On a
// FAILED run it is the only way the transcript reaches the host at all: the copy-out
// used to live on the success path alone, so `credentials.AgentTranscript` searched a home the
// failed session had never written to and reported "no evidence" every time.
//
// No-op rather than an error when there is nothing to do: a docker run keeps its
// home on the host and needs no copy, and a run that died before sbx created
// anything has no volumes to read.
func SaveState(name string, env []string, exists bool, run func(...string) (string, error)) (string, error) {
	if name == "" || !exists || StateHome(env) == "" {
		return "", nil
	}
	return run(sbx.SaveStateArgs(name)...)
}

// PreserveClone lifts a clone-mode sandbox's work into the host repository before
// the sandbox goes: a snapshot commit, then a fetch of every branch into
// refs/proveo/<name>/. No-op when the run was not a clone.
// SPEC: _spec/internal/sbx/clone-workspace.puml
func PreserveClone(in Input, cfg sbx.RunConfig) {
	if !in.Clone || in.RepoRoot == "" || !sbx.Exists(cfg.Name) {
		return
	}
	if wd := FirstHost(cfg.Mounts); wd != "" {
		if out, err := SbxRun(sbx.CloneSnapshotArgs(cfg.Name, wd)...); err != nil {
			ui.Warnf("clone: could not snapshot uncommitted work (%v): %s", err, strings.TrimSpace(out))
		}
	}
	// Deliverables first, commits second: the output dir is what an operator
	// looks for the moment the run ends, and it is not in any branch.
	liftClonedOutput(in, cfg, liftViaSbx)
	fetch := exec.Command("git", sbx.CloneFetchArgs(in.RepoRoot, cfg.Name)...)
	if out, err := fetch.CombinedOutput(); err != nil {
		ui.Warnf("clone: fetch from %s failed (%v): %s — if the sandbox still exists, `git fetch %s` by hand",
			sbx.CloneRemote(cfg.Name), err, strings.TrimSpace(string(out)), sbx.CloneRemote(cfg.Name))
		return
	}
	refs, _ := exec.Command("git", "-C", in.RepoRoot, "for-each-ref", "--format=%(refname:short)", sbx.CloneRefs(cfg.Name)+"/").Output()
	names := strings.Fields(string(refs))
	if len(names) == 0 {
		ui.Notef("clone: the agent left no branches to fetch")
		return
	}
	ui.Section(ui.SectionResults)
	ui.Storef("clone: the agent's work is in your repository under %s/ — %s", sbx.CloneRefs(cfg.Name), strings.Join(names, " "))
	ui.Notef("review: `git log --oneline main..%s` · adopt: `git checkout -b <branch> %s`", names[0], names[0])
}

// liftClonedOutput copies the output dir out of the clone into the host output
// dir, as a tar stream unpacked under the host repo root. Failure warns and
// names the sandbox.
func liftClonedOutput(in Input, cfg sbx.RunConfig, lift func(args []string, into string) (int, string, error)) {
	rel, ok := nestedRel(in.RepoRoot, in.OutputDir)
	wd := FirstHost(cfg.Mounts)
	if !ok || wd == "" {
		return
	}
	code, out, err := lift(sbx.CloneLiftArgs(cfg.Name, wd, rel), in.RepoRoot)
	switch {
	case err == nil:
		ui.Storef("clone: deliverables lifted from the clone's %s/ into %s", rel, in.OutputDir)
	case code == sbx.CloneLiftNothing:
		ui.Notef("clone: the agent wrote nothing under %s/", rel)
	default:
		ui.Warnf("clone: could not lift %s/ out of the sandbox (%v): %s — while %s exists: `sbx exec -w / %s -- tar -C %s -cf - %s | tar -xf - -C %s`",
			rel, err, strings.TrimSpace(out), cfg.Name, cfg.Name, wd, rel, in.RepoRoot)
	}
}

// liftViaSbx runs the lift argv and unpacks its stdout under into. It returns the
// sandbox-side exit status so the caller can tell "nothing there" from a failure.
func liftViaSbx(args []string, into string) (int, string, error) {
	src := exec.Command(sbx.Binary, args...)
	var srcErr strings.Builder
	src.Stderr = &srcErr
	stdout, err := src.StdoutPipe()
	if err != nil {
		return -1, "", err
	}
	untar := exec.Command("tar", "-xf", "-", "-C", into)
	untar.Stdin = stdout
	var untarOut strings.Builder
	untar.Stdout, untar.Stderr = &untarOut, &untarOut
	if err := src.Start(); err != nil {
		return -1, srcErr.String(), err
	}
	untarErr := untar.Run()
	srcRun := src.Wait()
	if srcRun != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(srcRun, &ee) {
			code = ee.ExitCode()
		}
		return code, srcErr.String() + untarOut.String(), srcRun
	}
	if untarErr != nil {
		return 0, untarOut.String(), untarErr
	}
	return 0, "", nil
}

// FreeLoopbackPort reserves a host port by binding it and letting it go.
// Impure, which is why it is the caller's job and not Spec's.
func FreeLoopbackPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// StartCDPViewport publishes the agent's own Chromium to the host and holds the
// relay open for the run's life, returning the stop function. Guarded on
// Running, never merely on Exists: `sbx exec` RESTARTS a stopped sandbox.
func StartCDPViewport(in Input, cfg sbx.RunConfig) func() {
	if len(cfg.Publish) == 0 {
		return func() {}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", in.CDPHostPort)
	ui.Section(ui.SectionInterface)
	ui.Hostf("browser viewport: %s — attach Chrome DevTools or Playwright (connectOverCDP) to the agent's Chromium", url)
	ui.Notef("%s/json/list once the agent opens a page; nothing is exposed beyond this machine's loopback", url)

	stop := make(chan struct{})
	go func() {
		for {
			if !sbx.Running(cfg.Name) {
				select {
				case <-stop:
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			c := exec.Command(sbx.Binary, sbx.CDPRelayArgs(cfg.Name)...)
			// Discarded on purpose: this shares the agent's terminal, and a relay
			// that narrated itself would draw over the TUI.
			c.Stdout, c.Stderr = io.Discard, io.Discard
			if err := c.Start(); err != nil {
				select {
				case <-stop:
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			done := make(chan struct{})
			go func() { _ = c.Wait(); close(done) }()
			select {
			case <-stop:
				_ = c.Process.Kill()
				return
			case <-done:
			}
		}
	}()
	return func() { close(stop) }
}

// cdpPublish is the port mapping that carries the browser viewport, or nil when
// this run has no browser to show.
func cdpPublish(in Input) []string {
	if !in.Browser || in.CDPHostPort <= 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d:%d", in.CDPHostPort, sbx.CDPRelayPort)}
}

// SplitNested separates the mounts that sit UNDER root from the rest. The root
// itself is not nested. It is what keeps a clone-mode workspace list flat: sbx
// mounts each positional at its own host path, so a bind inside the repository is
// a bind inside the clone target, and sbx clones only into an empty one.
func SplitNested(root string, mounts []sbx.Mount) (kept, nested []sbx.Mount) {
	for _, m := range mounts {
		if _, ok := nestedRel(root, m.Host); ok {
			nested = append(nested, m)
			continue
		}
		kept = append(kept, m)
	}
	return kept, nested
}

// nestedRel reports path's location relative to root when path lies strictly
// inside root — never for root itself, never for a sibling that merely shares a
// prefix ("/repo2" is not under "/repo").
func nestedRel(root, path string) (string, bool) {
	root, path = strings.TrimSpace(root), strings.TrimSpace(path)
	if root == "" || path == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// KeptLines is what proveo says about a failed run after the evidence
// channels have had their turn: how to look inside the sandbox, how to clean it up,
// and where the run's own transcript is.
//
// The run log is named HERE and not only at startup. It holds every line the run
// printed — the resolved posture, the credential warnings — and by the time an agent
// dies those have scrolled off a terminal nobody redirected. The macOS run whose
// login file had blanked tokens said so in its twelfth line and was diagnosed from
// scrollback that no longer existed.
func KeptLines(name, runLog string) []string {
	lines := []string{fmt.Sprintf(
		"sandbox %s kept for diagnosis (the run failed) — `sbx exec %s -- sh`, then `sbx rm --force %s`",
		name, name, name)}
	if strings.TrimSpace(runLog) != "" {
		lines = append(lines,
			fmt.Sprintf("every line this run printed, posture and warnings included: %s", runLog))
	}
	return lines
}

func WorkspaceBinds(mounts []sbx.Mount) []sbx.Mount {
	var out []sbx.Mount
	seen := map[string]bool{}
	for _, m := range mounts {
		if strings.HasPrefix(m.Container, proveohome.ContainerHome+"/") {
			continue // nested under home; its nesting cannot be reproduced
		}
		// A workspace is a DIRECTORY. docker binds a single file happily — the
		// project .env arrives as one, and the credential policy masks it with a
		// /dev/null bind — but sbx refuses: "workspace path exists but is not a
		// directory". Those binds are dropped, which also means a project .env does
		// not reach an sbx sandbox; on the brokered tiers it was being masked away
		// anyway, and the credentials it would have carried are declared in the Kit.
		if fi, err := os.Stat(m.Host); err != nil || !fi.IsDir() {
			continue
		}
		// A linked worktree contributes only <repo>/.git, and sbx MIRRORS host
		// paths rather than remapping them — so the <repo> parent has to be
		// synthesized inside the VM, where the runtime creates it root-owned. It
		// then looks exactly like the operator's repo root and is not one: it is an
		// empty stub nobody can write to. Tools that resolve the main repository
		// directory (nx keeps its shared task DB there) die on a permission error
		// naming a path the operator knows is theirs, which sends the diagnosis
		// looking for a phantom root-promotion.
		//
		// Mounting the PARENT makes it a real bind, uid-mapped like every other
		// workspace and writable from the host and the agent alike. Measured on the
		// same worktree: root:root and writes DENIED before, claude:claude and
		// writes OK after, with `git rev-parse` still resolving the worktree.
		//
		// Docker keeps the narrow mount: it remaps container paths freely, so no
		// parent is ever synthesized and nothing is shadowed.
		if m.Container == workspace.ContainerGitCommonDir && filepath.Base(m.Host) == ".git" {
			m.Host = filepath.Dir(m.Host)
		}
		if seen[m.Host] {
			continue // the repo root may already be a workspace in its own right
		}
		seen[m.Host] = true
		out = append(out, m)
	}
	return out
}

// Home rewrites HOME from the container path docker used to the HOST path sbx
// mounts proveo home at, so ~/.claude and the resume state persist across runs
// instead of landing in a sandbox-local directory that dies with the VM.
func Home(env []string, mounts []sbx.Mount) []string {
	host := ""
	for _, m := range mounts {
		if m.Container == proveohome.ContainerHome {
			host = m.Host
			break
		}
	}
	if host == "" {
		return env
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "PROVEO_HOME=") {
			continue
		}
		out = append(out, e)
	}
	// NEITHER is set on this backend, and the deletion is the fix.
	//
	// Redirecting HOME to the mounted host path was written for the docker backend,
	// where proveo runs the agent as the HOST's uid and the image's passwd entry is
	// therefore wrong. sbx does not do that: it runs its own agent user, uid 1000,
	// and its built-in kit both mounts the session volumes under that user's home
	// and has its credential proxy write `.credentials.json` there. Pointing HOME
	// somewhere else did not move those — it ORPHANED them, so the agent read a
	// stale mounted credential instead of the live proxy-managed one and reported
	// "Not logged in" (e2e/ladder_test.go, rung 3).
	//
	// PROVEO_HOME existed only to survive that redirect: sbx resets HOME from
	// /etc/passwd for setup.startup, so a seed reading $HOME wrote to a different
	// place than the agent read. With no redirect there is no divergence — the seed
	// and the agent both resolve the image's home, which the Dockerfiles now pin to
	// /home/agent to match sbx's kit.
	//
	// The proveo home stays MOUNTED (it is a workspace bind), so nothing that reads
	// it by path is lost; only the env redirect goes.
	//
	// What the redirect DID buy is resume: with HOME on the mounted host dir, the
	// agent's transcripts survived the run. Without it they land in the volumes sbx
	// mounts under the image's home, and teardown removes "VM + images + volumes",
	// so `--resume` had nothing to offer on the next run. PROVEO_STATE_HOME hands
	// the seed and the teardown the host path to copy those directories to and
	// from, which restores persistence WITHOUT moving HOME away from the credential
	// the sbx proxy writes there.
	return append(out, sbx.StateHomeVar+"="+host)
}

// FirstHost is the host path of the first bind, which is where sbx puts the cwd.
func FirstHost(mounts []sbx.Mount) string {
	if len(mounts) == 0 {
		return ""
	}
	return mounts[0].Host
}

// Input is the resolved input to the sbx backend.
type Input struct {
	// The CLI struct used to cross this boundary whole, which made every sandbox
	// function reachable from cobra and back. These are the values it actually
	// read — resolved by the caller, exactly as move 6 will resolve the rest.
	Target, Image, AuthVar string
	Shell, Clone           bool
	RepoRoot               string // host repository root: where a clone's commits are fetched back to
	// OutputDir is the host output dir (the harness's deliverables). Nested under
	// RepoRoot it cannot be a live workspace in clone mode — see SplitNested — so
	// PreserveClone lifts it out of the clone at teardown instead.
	OutputDir string
	// Browser is the browser add-on: this run takes the -browser image, whose
	// Chromium the operator can watch or drive from the host over CDPHostPort.
	Browser bool
	// CDPHostPort is the host loopback port the browser viewport is published on,
	// chosen by the caller BEFORE launch so the URL can be printed up front and so
	// --print renders the argv the run executes. Zero disables the viewport.
	CDPHostPort int
	Extra       []string
	Roles       provider.Roles
	Bridges     provider.BridgeTable
	Evidence    string // was params.evidenceOrDefault()
	Forwards    bool   // was params.forwards()
	Man         manifest.Manifest
	Sid, EgDir  string
	Mounts      []runner.Mount
	Workdir     string
	Lookup      func(string) string
	Detected    []string
	GitEnv      []string
	HomeEnv     []string
	// BridgeEnv names the host Claude in Chrome relay to the agent, through the
	// Kit's env — the only route into a sandbox.
	BridgeEnv        []string
	ScopeRel         string
	WorktreeFallback bool
	WorktreeEnv      []string
	DataDir          string
	// Memory is the -m limit for the sandbox, resolved by the caller so that
	// Spec stays pure and --print renders the same argv the run executes.
	Memory string
	CPUs   int
	// HomeRoot is the proveo home, passed in for the same reason: a host login
	// living there decides which credential the run authenticates with, and
	// Spec must not reach for the real filesystem to find that out.
	HomeRoot string
	// RunLog is where every line this run printed was tee'd. Carried in so the
	// failure path can name it: by the time an agent dies the posture and the
	// credential warnings have scrolled off, and the operator has no way back to
	// them from a terminal they did not redirect.
	RunLog string
}

// Spec resolves the sbx invocation: RunConfig, Kit, and host-side secrets.
func Spec(in Input) (sbx.RunConfig, sbx.Kit, [][2]string) {
	hosts := map[string]bool{}
	for _, d := range strings.Fields(credentials.JoinDomains(os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"), in.Man.Capabilities.Hosts)) {
		hosts[d] = true
	}
	for _, h := range credentials.ReachableHosts(in.Detected) {
		hosts[h] = true
	}
	allow := make([]string, 0, len(hosts))
	for h := range hosts {
		allow = append(allow, h)
	}
	sort.Strings(allow)

	var secrets [][2]string
	addSecret := func(name string) {
		v := in.Lookup(name)
		if v == "" {
			return
		}
		for _, kv := range secrets {
			if kv[0] == name {
				return
			}
		}
		secrets = append(secrets, [2]string{name, v})
	}
	forwards := in.Forwards
	var forwarded []string
	addForward := func(name string) {
		if in.Lookup(name) == "" {
			return
		}
		for _, n := range forwarded {
			if n == name {
				return
			}
		}
		forwarded = append(forwarded, name)
	}
	suppressedAuth := credentials.AuthSuppressor(in.Man, in.Target, in.AuthVar, in.HomeRoot)
	// A suppressed credential is OMITTED, not stated as empty — the same as the
	// docker path does.
	//
	// This used to render `-e VAR=` on the theory that sbx's global secret store
	// would otherwise inject a stale value in front of the mounted login. Probed
	// against sbx v0.39.0 with both ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN
	// sitting in the global store and no -e flag: both arrive UNSET. sbx does not
	// export service secrets as environment variables — its proxy attaches them as
	// headers, and it materialises the harness credential as ~/.claude/.credentials.json
	// instead. So there was nothing in front of the login to override.
	//
	// The empty value was not merely useless, it was the failure. An agent reads a
	// SET variable as a chosen credential regardless of its value: claudecode ranks
	// ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN above the login it would find on
	// disk, so a blank one occupied the slot the login needed and asked an unattended
	// run to approve a key that could authenticate nothing.
	for _, e := range in.Man.Env {
		if !e.Secret {
			continue
		}
		if suppressedAuth(e.Name) {
			continue
		}
		if forwards {
			addForward(e.Name)
			continue
		}
		addSecret(e.Name)
	}
	// Filtered by what the harness declares it can USE, the same way detection is
	// on the docker path. Unfiltered, a claudecode sandbox asked sbx for cursor,
	// google, openai and xai credentials too — and sbx shows that list to the
	// operator for approval, so an over-declared Kit is not merely untidy: it asks
	// consent for reach the harness has no way to exercise.
	for _, k := range provider.KeyVars() {
		if !in.Man.Capabilities.AllowsProvider(credentials.ProviderOfKeyVar(k)) {
			continue
		}
		// A provider with two ways to authenticate gets only the one the operator
		// chose. Handing sbx both put an API key and a subscription token in the
		// same store, and its proxy injected the key — so a subscription run
		// silently billed per token and the auth row the operator answered was
		// decided for them somewhere they could not see.
		if suppressedAuth(k) {
			continue
		}
		if forwards {
			addForward(k)
			continue
		}
		addSecret(k)
	}

	var env []string
	env = append(env, forwarded...)
	for _, e := range in.Man.Env {
		if e.Secret {
			continue
		}
		if v := strings.TrimSpace(in.Lookup(e.Name)); v != "" {
			env = append(env, e.Name+"="+v)
		}
	}
	for _, k := range credentials.ConfigVarsFor(in.Man) {
		if v := strings.TrimSpace(in.Lookup(k)); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, EvidenceVar+"="+in.Evidence)
	// proveo's own defaults for the agent (manifest agentEnv). HERE and not in the
	// image entrypoint alone: sbx launches the agent through its own kit, so an
	// export in the entrypoint never reaches it on this backend — claudecode came
	// up in whatever renderer its saved `tui` setting named.
	env = append(env, in.Man.AgentEnvPairs(in.Lookup)...)
	env = append(env, in.GitEnv...)
	env = append(env, in.HomeEnv...)
	env = append(env, in.BridgeEnv...)
	// The browser viewport. AGENT_BROWSER_ARGS travels as run environment rather
	// than as an image default because sbx launches the agent through its own kit
	// and never runs the entrypoint, so an export there reaches the docker backend
	// alone.
	if in.Browser && in.CDPHostPort > 0 {
		existing := ""
		if in.Lookup != nil {
			existing = in.Lookup("AGENT_BROWSER_ARGS")
		}
		env = append(env, "AGENT_BROWSER_ARGS="+sbx.BrowserCDPArgs(existing))
	}
	if in.ScopeRel != "" {
		env = append(env, "PROVEO_SCOPE_REL="+in.ScopeRel)
	}
	if in.WorktreeFallback {
		env = append(env, in.WorktreeEnv...)
	}

	var mounts []sbx.Mount
	for _, m := range in.Mounts {
		mounts = append(mounts, sbx.Mount{Host: m.Host, Container: m.Container, ReadOnly: m.ReadOnly})
	}
	if in.DataDir != "" {
		mounts = append(mounts, sbx.Mount{Host: in.DataDir, Container: "/workspace/data", ReadOnly: true})
	}
	// In clone mode nothing may be mounted UNDER the repository: sbx clones only
	// into an empty workspace, and a nested mount arriving first skips the clone
	// silently. The output dir is lifted back at teardown instead.
	if in.Clone && in.RepoRoot != "" {
		var nested []sbx.Mount
		mounts, nested = SplitNested(in.RepoRoot, mounts)
		for _, m := range nested {
			if _, isOutput := nestedRel(in.RepoRoot, in.OutputDir); isOutput && filepath.Clean(m.Host) == filepath.Clean(in.OutputDir) {
				ui.Storef("clone: %s is inside the repository, so it is not mounted live — sbx clones only into an "+
					"empty workspace; the agent writes it inside the clone and proveo lifts it back here at teardown", m.Host)
				continue
			}
			ui.Warnf("clone: %s is inside the repository and cannot be mounted into a clone — read it from the clone instead", m.Host)
		}
	}

	// --shell selects sbx's OWN shell agent rather than substituting a command: the
	// built-in agent owns the launch, so a trailing "bash" reached the harness's
	// agent as an argument and the shell never opened.
	//
	// Otherwise AgentFor decides. A harness sbx has a built-in for launches under
	// it and owns its own argv, so anything the operator passed rides as the
	// trailing command; a harness sbx has no name for runs under the shell agent
	// with its own launch command, and an explicit `--` argument replaces that.
	agent, launch := sbx.AgentFor(in.Target)
	command := in.Extra
	if len(command) == 0 {
		command = launch
	}
	if in.Shell {
		command, agent = nil, sbx.ShellAgent
	}
	cfg := sbx.RunConfig{
		Name: in.Sid,
		// The Kit path is resolved HERE, not at write time, so --print renders the
		// same argv the run executes. Deriving it only inside Run left the
		// dry run silently missing --kit — the posture the Kit carries (allowlist,
		// brokered credentials) was invisible in exactly the output an operator
		// inspects to check that posture.
		KitDir: filepath.Join(in.EgDir, "sbx", "kit"),
		Image:  in.Image,
		// sbx would otherwise size the sandbox from HOST memory, which on any VM-backed
		// daemon can exceed the VM itself — see sbx.MemoryLimit.
		Memory: in.Memory,
		CPUs:   in.CPUs,
		// Clone mode is why a bind-mounted node_modules stops ping-ponging: the
		// clone carries only TRACKED files, so a host-built (macOS) tree never
		// arrives, the seed installs Linux deps into the clone, and the operator's
		// checkout is never written. Measured: origin points at
		// /run/sandbox/source and node_modules is absent from the workspace.
		Clone:   in.Clone,
		Publish: cdpPublish(in),
		// A kind: sandbox Kit DEFINES an agent, and sbx requires the positional to
		// match its name — "agent name X does not match agent kit name Y". So the
		// two are one value, and there is no separate agent to declare: an earlier
		// attempt to name one in the manifest (sbxAgent: claude) was refused for
		// exactly this reason. It is namespaced because sbx also refuses to let a
		// Kit shadow a built-in agent, and `cursor` and `opencode` are both built
		// in — see sbx.BuiltinAgent.
		Agent: agent,
		// Only the WORKSPACE binds survive onto an sbx run: it mounts each
		// positional path at its own host path, so a bind with a container-side
		// target — proveo home at /proveo-home, the gh config under it — has no way
		// to be expressed. They are dropped rather than silently mounted somewhere
		// else, and PROVEO_WORKDIR below tells the harness where it actually landed.
		Mounts: WorkspaceBinds(mounts),
		// The bridge is applied HERE, not in the container: its output goes into the
		// Kit's environment block, where it reaches the agent. Left to a setup hook
		// it would be computed in a process the agent never inherits.
		// The decline is declared twice on purpose. The Kit's environment block is
		// the posture proveo publishes; the -e flag is what sandboxd applies when
		// it CREATES the sandbox, which is the moment its own injection happens.
		// Either alone may lose that race; neither is harmful when the other wins.
		Env: DeclineMCPGateway(Home(append(append(env, ResolvedModelEnv(in)...),
			"PROVEO_WORKDIR="+FirstHost(WorkspaceBinds(mounts))), mounts)),
		Command: command,
	}
	// The Kit is a MIXIN composed onto sbx's own agent: it declares no agent, no
	// image and no credentials. The image arrives via -t, the agent is sbx's, and
	// credentials belong to the built-in agent's kit — repeating a service there is
	// rejected outright ("defined in both"), and its proxy already injects.
	kit := sbx.Kit{
		SchemaVersion: sbx.KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          in.Target + "-posture",
		DisplayName:   "proveo posture (" + in.Target + ")",
		Description:   "Reachability, host-resolved environment and the seed step for a proveo run.",
		Permissions:   sbx.KitPermissions{Network: sbx.KitNet{Allow: allow}},
		Environment:   &sbx.KitEnv{Variables: WithMCPGatewayPolicy(KitEnvVars(cfg.Env))},
		Setup:         &sbx.KitSetup{Startup: []sbx.KitCommand{sbx.SeedCommand(in.Target)}},
	}
	return cfg, kit, secrets
}

// MCPGatewayVar is the variable sbx's built-in agent kits gate their MCP
// registration on. Their step is `[ -n "$MCP_GATEWAY_URL" ] || exit 0`, so
// declaring it EMPTY is the supported way to decline — no patching, no race with
// a step that runs inside the sandbox.
const MCPGatewayVar = "MCP_GATEWAY_URL"

// WithMCPGatewayPolicy declines sbx's MCP gateway unless the operator asks for it.
//
// The registration is `claude mcp add mcp-gateway <url> --scope user`, run inside
// the sandbox by sbx's own kit. USER scope is the problem: proveo points HOME at
// the proveo home and mounts it read-write, so an entry meant to live and die with
// a disposable sandbox lands in the operator's real home and outlives every run
// that wrote it. Nothing in `proveo run --print` ever named it, which makes it a
// third source of agent capability beside the Kit and the credential store.
//
// Declining costs nothing today: `sbx mcp ls` registers no servers, so the
// gateway aggregates an empty set. PROVEO_SBX_MCP=on restores it for an operator
// who has registered servers of their own and wants the agent to reach them.
func WithMCPGatewayPolicy(vars map[string]string) map[string]string {
	if MCPGatewayAllowed() {
		return vars
	}
	if vars == nil {
		vars = map[string]string{}
	}
	// Written explicitly rather than through KitEnvVars, which drops empty values:
	// here the empty string IS the instruction.
	vars[MCPGatewayVar] = ""
	return vars
}

// DeclineMCPGateway adds the empty MCP_GATEWAY_URL to the -e set. KitEnvVars
// drops empty values, so this pair never duplicates the Kit's own declaration.
func DeclineMCPGateway(env []string) []string {
	if MCPGatewayAllowed() {
		return env
	}
	for _, e := range env {
		if strings.HasPrefix(e, MCPGatewayVar+"=") {
			return env
		}
	}
	return append(env, MCPGatewayVar+"=")
}

func MCPGatewayAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_SBX_MCP"))) {
	case "on", "1", "yes", "true", "enable", "enabled":
		return true
	}
	return false
}

// ResolvedModelEnv is the model bridge, applied host-side, as KEY=VALUE pairs.
func ResolvedModelEnv(in Input) []string {
	var out []string
	for k, v := range in.Bridges.ResolvedEnv(in.Target, in.Roles) {
		out = append(out, k+"="+v)
	}
	sort.Strings(out) // a Kit is written to disk and diffed; order must not churn
	return out
}

// KitEnvVars turns the resolved KEY=VALUE pairs into the Kit's environment block.
//
// Only pairs that already carry a value are declared: a bare NAME in cfg.Env means
// "forward whatever the host holds", which is a -e concern and cannot be written
// into a spec file. Resolving here is the point of the design — the agent receives
// ANTHROPIC_MODEL decided, rather than a table and a bridge to recompute it.
func KitEnvVars(env []string) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Run renders the Kit, injects credentials, runs the agent, tears down.
func Run(in Input) error {
	cfg, kit, secrets := Spec(in)
	// Spec already put the Kit path on cfg so --print renders the argv this
	// run will actually use; here the file behind that path gets written.
	if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
		return err
	}
	// The sandbox runtime keeps its OWN image store, so the harness image has to
	// be handed over before the Kit can name it. Local and per-user on purpose:
	// each operator has their own docker login and their own sbx config, so this
	// stays a pipe between the two stores rather than a registry round-trip.
	ui.Section(ui.SectionStarting)
	if err := sbx.EnsureTemplate(cfg.Image, func(f string, a ...any) {
		ui.Appf(f, a...)
	}); err != nil {
		return err
	}
	// The values behind every bare `-e NAME` in the Kit's env, for the `sbx` exec
	// below and nowhere else.
	var child credentials.ChildEnv
	for _, e := range cfg.Env {
		if !strings.Contains(e, "=") {
			child.Add(e, in.Lookup)
		}
	}
	for _, kv := range secrets {
		ui.Section(ui.SectionSecrets)
		ui.Hostf("sandbox secret: %s (host-side injection)", kv[0])
		if err := sbx.SecretSet(kv[0], kv[1]); err != nil {
			return fmt.Errorf("sandbox secret %s: %w", kv[0], err)
		}
	}
	// Said once, not per secret. sbx's secret store is HOST-WIDE and `secret set
	// --force` overwrites: what this run writes is visible to every sandbox on the
	// machine and outlives the run. A stale entry from an earlier run is therefore
	// able to authenticate a later one that never chose it — which reads as proveo
	// picking a credential the operator did not, somewhere they cannot see.
	if len(secrets) > 0 {
		ui.Notef("sbx's secret store is host-wide and outlives this run — `sbx secret ls`")
	}
	// Before the launch, so the URL is on screen ahead of the agent's TUI rather
	// than lost behind it.
	defer StartCDPViewport(in, cfg)()
	args := sbx.RunArgs(cfg)
	// sbx has no `logs` command and the sandbox may be gone by the time anyone
	// asks, so the only reliable copy of what the agent said is the one taken as
	// it streams past. "agent exited with code 137" is sbx's auto-stop code,
	// arriving 30s after the agent itself exited; the tail is what actually
	// explains the run — a credit-balance error, a login prompt, a stack trace.
	//
	// The tail can only be taken when stdout is ALREADY redirected. os/exec gives
	// the child the real terminal only when the field holds an *os.File; wrapping
	// it in an io.MultiWriter substitutes a pipe, so the agent sees no tty, cannot
	// read the window size, and draws its TUI one character per line. Interactive
	// runs keep the terminal and forgo the tail — the operator is watching anyway.
	stdout, stderr, tail := agentio.Stdio(os.Stdout, os.Stderr, agentio.IsWriterTTY(os.Stdout))
	// PROVEO_TRACE_STDIN answers the one question a transcript cannot: an agent
	// that exits on its own, having answered prompts the operator never typed,
	// is being driven by SOMETHING on stdin — a multiplexer replying to a
	// terminal query, a wrapper feeding a script, a stray paste. The transcript
	// records what the agent RECEIVED; only a tap records what arrived.
	traceIn, stopTrace := agentio.Tracer(os.Getenv("PROVEO_TRACE_STDIN"))
	defer stopTrace()
	// An interactive run goes through the pty proxy so the operator's terminal is
	// FILTERED before it reaches the agent. sbx drives the agent through its
	// agent-session API, where the far end reads input as a prompt stream rather
	// than as terminal input — so a report with no query to belong to (a
	// multiplexer answering Device Attributes twice, an unsolicited focus event)
	// is not ignored there, it is enqueued and answered as a user message nobody
	// typed. See ptyproxy.inputFilter.
	//
	// The proxy gives the child its own pty, so this costs the agent nothing:
	// wrapping os.Stdin instead would make os/exec substitute a pipe and take the
	// terminal away entirely.
	filtered := ptyproxy.Usable(os.Stdin, os.Stdout) && agentio.FilterEnabled()
	run := func() error {
		c := exec.Command(sbx.Binary, args...)
		c.Env = child.Apply(os.Environ())
		if filtered || (traceIn != nil && ptyproxy.Usable(os.Stdin, os.Stdout)) {
			px := ptyproxy.New(os.Stdin, os.Stdout)
			px.DisableFilter = !filtered
			// Every report, not just a surplus copy. The default rule forwards a
			// report's first copy because the child asked for it — which is true of
			// an agent on a real tty and FALSE here, where sbx's agent-session API
			// reads input as a prompt stream and never queried anything. A lone
			// unsolicited report is indistinguishable from a legitimate answer under
			// dedup alone, so it was forwarded and enqueued as a user message: one
			// "\x1b[?6c" five seconds into run proveo-1787852436-14907, then an agent
			// that answered a prompt nobody typed and exited at 10:41:04.
			px.DropReports = true
			px.Tap = traceIn
			// The child's output reaches the terminal through the pty master either
			// way, so copying it into the tail costs the agent nothing and gives an
			// interactive run the one channel it never had. Without this, the run
			// that dies at its prompt has no tail AND no transcript, and "sandbox
			// was stopped" is the entire report.
			if tail != nil {
				px.OutTap = func(b []byte) { _, _ = tail.Write(b) }
			}
			return px.Run(c)
		}
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, stderr
		return c.Run()
	}
	startedAt := time.Now()
	runErr := run()
	// Closed the moment the child returned, BEFORE anything proveo does to collect
	// evidence. Everything written after this instant belongs to the harvest, not
	// to the run — including a restarted sandbox's own fresh session files.
	endedAt := time.Now()
	if runErr != nil && !sbx.Exists(cfg.Name) {
		// The template sbx holds has gone bad. Hand the image over again and try
		// once more; if it fails the same way the second time, it is not the
		// template and the original error stands.
		ui.Section(ui.SectionStarting)
		if err := sbx.ReloadTemplate(cfg.Image, func(f string, a ...any) {
			ui.Appf(f, a...)
		}); err == nil {
			ui.Asyncf("the sandbox did not start — retrying once on a freshly loaded template")
			runErr = run()
			endedAt = time.Now()
		}
	}
	defer func() {
		CapturePolicyLog(in.EgDir, cfg.Name)
		// A sandbox that failed is the only copy of why. sbx has no `logs` command, so
		// force-removing it here destroyed the evidence for two consecutive 137s before
		// anyone could read it. Keep it, and say how to look and how to clean up.
		if runErr != nil {
			said := false
			if lines := tail.Lines(); len(lines) > 0 {
				said = true
				fmt.Fprintf(os.Stderr, "\n── last output from the agent ──\n")
				for _, l := range lines {
					fmt.Fprintf(os.Stderr, "  %s\n", l)
				}
				fmt.Fprintf(os.Stderr, "───────────────────────────────\n")
			}
			// The transcript is written into sandbox-LOCAL volumes and only the
			// SUCCESS path below copied it out, so the one channel that explains a
			// failed sbx run was structurally always empty — credentials.AgentTranscript read a
			// host home the session had never reached. Copy it out first.
			//
			// On a STOPPED sandbox this starts the VM to do it, which re-runs the
			// seed and so re-runs `proveo_sync_state restore`. The claim that used to
			// stand here — that this is safe because restore is `cp -a` with no
			// deletes — was wrong twice over, and both halves cost real data:
			//
			//   restore then ran CONCURRENTLY with this save, in the opposite
			//   direction over the same files, and `cp` truncates its destination in
			//   place. Seven of the operator's transcripts were rewritten short
			//   inside one second, at exact 256 KiB multiples, cut mid-JSON, with the
			//   short copy propagated to both sides. `proveo_sync_state` now holds a
			//   lock and renames into place, so neither direction can leave a partial
			//   file (packages/lib/entrypoint-lib.sh).
			//
			//   and "its mtime postdates startedAt" is satisfied by the restart's OWN
			//   artifacts. A zero-byte session file the restart created 17s after the
			//   agent died was reported as what the agent said. credentials.AgentTranscript is
			//   now bounded above by endedAt and rejects empty files.
			//
			// Whether the harvest had to wake a stopped sandbox is worth saying out
			// loud: it is why the home holds files newer than the run.
			restarted := !sbx.Running(cfg.Name)
			PreserveClone(in, cfg)
			_, _ = SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun)
			if t := credentials.AgentTranscript(in.Target, in.HomeRoot, startedAt, endedAt); t != "" {
				said = true
				ui.Storef("what the agent actually said is in %s", t)
			} else if restarted && sbx.Exists(cfg.Name) {
				ui.Storef("no transcript from this run — the sandbox had already stopped, " +
					"so state was copied out after it ended and anything newer than the run is the harvest's own")
			}
			// Both evidence channels came up empty, which is its own diagnosis: the
			// agent died before its first turn. Naming the credential here is what
			// stops the operator spending the next hour inside `sbx exec` looking
			// for a cause that is on the host.
			if !said {
				if hint := credentials.NoCredentialHint(in.Man, in.Target, in.HomeRoot, cfg.Env, secrets,
					sbx.StoredSecretNames(), in.Lookup); len(hint) > 0 {
					ui.Hostf("%s", hint[0])
					for _, l := range hint[1:] {
						ui.Notef("%s", l)
					}
				}
			}
			ui.Section(ui.SectionResults)
			kept := KeptLines(cfg.Name, in.RunLog)
			ui.Warnf("%s", kept[0])
			for _, l := range kept[1:] {
				ui.Notef("%s", l)
			}
			return
		}
		// A clone lives in the VM that `sbx rm` is about to destroy, so its commits
		// come out FIRST — before the resume state, which lives in per-sandbox
		// volumes the same line destroys. Best-effort by design: a sandbox that never
		// started has nothing to copy, and losing yesterday's transcripts is not a
		// reason to leave a VM running.
		PreserveClone(in, cfg)
		if out, err := SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun); err != nil {
			ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(out))
		}
		rmOut, rmErr := exec.Command(sbx.Binary, sbx.RemoveArgs(cfg.Name)...).CombinedOutput()
		// A run that never got as far as creating the sandbox has nothing to tear
		// down, and reporting that as a teardown failure sends the operator after
		// the wrong thing — the real error is the one `sbx run` already printed.
		if rmErr != nil && !sbx.NotFound(string(rmOut)) {
			ui.Warnf("sandbox teardown failed (%v): %s", rmErr, strings.TrimSpace(string(rmOut)))
		}
	}()
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return backend.ExitError{Code: ee.ExitCode()}
	}
	return runErr
}

func WarnBaseline() {
	ui.Section(ui.SectionEgress)
	allowed, known := sbx.NetworkAllowed(unallowlistedProbe)
	if !known {
		ui.Notef("sbx network baseline: unreadable (`sbx policy check network %s`)", unallowlistedProbe)
		return
	}
	if !allowed {
		return
	}
	ui.Warnf("sbx's global network policy allows every host, so this run's Kit allowlist adds reach rather than limiting it")
	ui.Notef("the tier below describes proveo's intent, not what sandboxd will enforce")
	ui.Notef("make it bind once, host-wide: `sbx policy init deny-all` (or `balanced`), then `sbx policy ls`")
}

func CapturePolicyLog(egDir, name string) {
	if egDir == "" || name == "" {
		return
	}
	out, err := sbx.PolicyLog(name)
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return
	}
	dir := filepath.Join(egDir, "sbx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, runlog.PolicyLogFile)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return
	}
	ui.Section(ui.SectionResults)
	ui.Storef("egress record: %s", path)
}

// Selected reports whether this run will take the sbx backend, using the same
// conditions as the branch that selects it. It is consulted before that branch so the
// transcript and the prompt describe the backend that will actually run.
func Selected(man manifest.Manifest) bool {
	if !man.IsSbx() || !Enabled() {
		return false
	}
	ok, _ := sbx.Available()
	return ok
}
