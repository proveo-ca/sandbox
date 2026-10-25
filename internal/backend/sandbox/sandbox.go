// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/internal/sbx/clone-workspace.puml, _spec/internal/sbx/kit-domain-form.puml, _spec/internal/sbx/ide-attach.puml, _spec/packages/lib/config-seeding-and-persistence.puml, _spec/_paradigms/capability-ladder.puml, _spec/_experiments/sbx-kit-capabilities.puml, _spec/minimum_requirements.puml, _spec/internal/sbx/launch-env.puml
// Package sandbox is the sbx backend:
package sandbox

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/proveo-ca/proveo/internal/devports"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/engine"
	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/hostcdp"
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

const (
	EvidenceVar        = "PROVEO_AGENT_EVIDENCE"
	unallowlistedProbe = "proveo-egress-probe.invalid"
)

// Unavailable is the refusal for a host sbx cannot run on, with the install or upgrade hint.
func Unavailable(why string) error {
	msg := fmt.Sprintf("docker sandboxes (sbx) unavailable: %s. proveo runs agents only in sbx", why)
	if eng := engine.Detect(); eng.Kind != engine.Unknown {
		msg += fmt.Sprintf("\n  engine: %s (%s)", eng.Label(), eng.Isolation())
	}
	if cmd := sbx.InstallCmd(sbx.Installed()); cmd != "" {
		if sbx.Installed() {
			msg += fmt.Sprintf("\n  proveo targets sbx %s or newer:", sbx.MinVersion)
		} else {
			msg += "\n  sbx is standalone and does not need Docker Desktop:"
		}
		msg += "\n    " + cmd
	}
	return errors.New(msg)
}

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

func Ready(printOnly bool, confirm func(string) bool) (bool, string) {
	ready := Ensure
	if printOnly {
		ready = func(func(string) bool) (bool, string) { return sbx.Available() }
	}
	ok, why := ready(confirm)
	if ok {
		warnUnverified(sbx.Version)
	}
	return ok, why
}

func warnUnverified(version func() (string, error)) {
	v, err := version()
	if err != nil {
		return
	}
	if note := sbx.UnverifiedNote(v); note != "" {
		ui.Warnf("%s", note)
	}
}

func StateHome(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, sbx.StateHomeVar+"="); ok {
			return v
		}
	}
	return ""
}

func SbxRun(args ...string) (string, error) {
	out, err := exec.Command(sbx.Binary, args...).CombinedOutput()
	return string(out), err
}

// SPEC: _spec/internal/sbx/kit-lifecycle.puml (A HUNG TEARDOWN)
var (
	cloneProbeDeadline = 36 * time.Second
	guestExecDeadline  = 3 * time.Minute
)

// guestGrace is how long a killed command may go on holding its pipes.
const guestGrace = 2 * time.Second

// guestCommand is exec.Command killed once d passes. finish releases the timer and
// wraps context.DeadlineExceeded when the deadline ended the command.
func guestCommand(d time.Duration, name string, args ...string) (*exec.Cmd, func(error) error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = guestGrace
	return cmd, func(err error) error {
		defer cancel()
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s gave no answer within %s: %w", name, d, context.DeadlineExceeded)
		}
		return err
	}
}

// guestRun is SbxRun bounded by guestExecDeadline.
func guestRun(args ...string) (string, error) {
	cmd, finish := guestCommand(guestExecDeadline, sbx.Binary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), finish(err)
}

func SaveState(name string, env []string, exists bool, run func(...string) (string, error)) (string, error) {
	if name == "" || !exists || StateHome(env) == "" {
		return "", nil
	}
	return run(sbx.SaveStateArgs(name)...)
}

// TeardownClone carries this run's clone home: its .env edits, then its work. It
// returns an error while work remains only in the sandbox.
func TeardownClone(in Input, cfg sbx.RunConfig) error {
	if in.RepoRoot == "" {
		return nil
	}
	if in.Clone && in.CloneKnown {
		wd := agentWorkdir(in, cfg.Mounts)
		writeBackCloneEnv(in, func() ([]byte, error) {
			if wd == "" || !sbx.Exists(cfg.Name) {
				return nil, errors.New("no sandbox to read from")
			}
			cmd, finish := guestCommand(guestExecDeadline, sbx.Binary, sbx.CloneReadArgs(cfg.Name, wd, ".env")...)
			out, err := cmd.Output()
			return out, finish(err)
		})
	}
	return PreserveClone(in, cfg)
}

// PreserveClone snapshots, lifts and carries the clone's work home. It returns an
// error while work remains only in the sandbox.
func PreserveClone(in Input, cfg sbx.RunConfig) error {
	if in.RepoRoot == "" || !sbx.Exists(cfg.Name) {
		return nil
	}
	clones, err := sandboxClone(in, cfg.Name)
	if err != nil {
		return fmt.Errorf("cannot tell whether %s holds a clone, so its work may be only there: %w", cfg.Name, err)
	}
	if !clones {
		return nil
	}
	in.Clone, in.CloneKnown = true, true
	var stranded error
	wd := agentWorkdir(in, cfg.Mounts)
	if wd != "" {
		if out, err := guestRun(sbx.CloneSnapshotArgs(cfg.Name, wd)...); err != nil {
			ui.Warnf("clone: could not snapshot uncommitted work (%v): %s", err, strings.TrimSpace(out))
			stranded = fmt.Errorf("uncommitted work is still only in %s", cfg.Name)
		}
	}
	liftClonedOutput(in, cfg, liftViaSbx)
	carried, err := carryClone(in, cfg, sbx.Running(cfg.Name), fetchViaRemote, bundleViaSbx)
	if err != nil {
		return err
	}
	if !carried {
		return stranded
	}
	reportCloneRefs(in, cfg)
	liftClonedSpec(in, wd, cloneSpanViaSbx(cfg.Name, wd), func(sha string) {
		if args := sbx.CloneRebaseArgs(cfg.Name, wd, sha); args != nil {
			_, _ = guestRun(args...)
		}
	})
	return stranded
}

// writeBackCloneEnv merges the clone's .env edits into the host .env it was staged from.
// SPEC: _spec/internal/sbx/clone-workspace.puml
func writeBackCloneEnv(in Input, read func() ([]byte, error)) {
	if in.CloneEnv == "" || in.CloneEnvBase == "" || in.CloneEnvHost == "" {
		return
	}
	base, err := os.ReadFile(in.CloneEnvBase)
	if err != nil {
		ui.Warnf("clone: .env not carried home — its launch copy %s is unreadable (%v)", in.CloneEnvBase, err)
		return
	}
	edited, err := read()
	if err != nil {
		if edited, err = os.ReadFile(in.CloneEnv); err != nil {
			ui.Warnf("clone: .env not carried home — neither the clone nor %s could be read (%v)", in.CloneEnv, err)
			return
		}
	} else if err := os.WriteFile(in.CloneEnv, edited, 0o600); err != nil {
		ui.Warnf("clone: could not keep the clone's .env at %s (%v)", in.CloneEnv, err)
	}
	if bytes.Equal(base, edited) {
		return
	}
	host, err := os.ReadFile(in.CloneEnvHost)
	if err != nil {
		ui.Warnf("clone: .env not carried home — %s is unreadable (%v); the clone's copy is %s", in.CloneEnvHost, err, in.CloneEnv)
		return
	}
	m := credentials.MergeProjectEnv(host, base, edited, in.CloneEnvStrip)
	if len(m.Applied) > 0 {
		if err := replaceFile(in.CloneEnvHost, m.Body); err != nil {
			ui.Warnf("clone: .env not carried home — writing %s failed (%v); the clone's copy is %s", in.CloneEnvHost, err, in.CloneEnv)
			return
		}
		ui.Storef("clone: .env edits carried home into %s — %s", in.CloneEnvHost, strings.Join(m.Applied, ", "))
	}
	if len(m.Conflicts) > 0 {
		ui.Warnf("clone: kept the host's %s in %s — the host changed it during the run too; the clone's copy is %s",
			strings.Join(m.Conflicts, ", "), in.CloneEnvHost, in.CloneEnv)
	}
	if len(m.Refused) > 0 {
		ui.Notef("clone: ignored %s from the clone's .env — sbx's proxy holds provider keys, the host file keeps its own",
			strings.Join(m.Refused, ", "))
	}
}

// replaceFile swaps path's content atomically and keeps its mode.
func replaceFile(path string, body []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".proveo-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func cloneSpanViaSbx(name, wd string) func() (string, string, error) {
	return func() (string, string, error) {
		cmd, finish := guestCommand(guestExecDeadline, sbx.Binary, sbx.CloneSpanArgs(name, wd)...)
		out, err := cmd.Output()
		if err = finish(err); err != nil {
			return "", "", err
		}
		f := strings.Fields(string(out))
		if len(f) != 2 || !commitID(f[0]) || !commitID(f[1]) {
			return "", "", fmt.Errorf("unexpected span %q", strings.TrimSpace(string(out)))
		}
		return f[0], f[1], nil
	}
}

func commitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// liftClonedSpec restores the clone's _spec edits into the host worktree,
// skipping every path the host changed since the clone's base.
// SPEC: _spec/internal/sbx/clone-workspace.puml
func liftClonedSpec(in Input, wd string, span func() (string, string, error), rebase func(string)) {
	if wd == "" || !trackedSpec(wd) {
		return
	}
	base, head, err := span()
	if err != nil {
		ui.Warnf("clone: _spec not lifted — the clone's base is unknown (%v); its edits are under %s/", err, sbx.CloneRefs(in.Sid))
		return
	}
	if base == head || !commitID(base) || !commitID(head) {
		return
	}
	changed, err := gitPaths(wd, "diff", "--name-only", "-z", "--no-renames", "--relative", base, head, "--", "_spec")
	if err != nil {
		ui.Warnf("clone: _spec not lifted — %s..%s is not in this repository (%v); its edits are under %s/", base[:12], head[:12], err, sbx.CloneRefs(in.Sid))
		return
	}
	hostDiff, _ := gitPaths(wd, "diff", "--name-only", "-z", "--no-renames", "--relative", base, "--", "_spec")
	untracked, _ := gitPaths(wd, "ls-files", "-z", "--others", "--exclude-standard", "--", "_spec")
	dirty := map[string]bool{}
	for _, p := range append(hostDiff, untracked...) {
		dirty[p] = true
	}
	var take, kept []string
	for _, p := range changed {
		if dirty[p] {
			kept = append(kept, p)
			continue
		}
		take = append(take, p)
	}
	if len(take) > 0 {
		cmd := exec.Command("git", "--literal-pathspecs", "-C", wd, "restore", "--source="+head, "--worktree",
			"--pathspec-from-file=-", "--pathspec-file-nul")
		cmd.Stdin = strings.NewReader(strings.Join(take, "\x00"))
		if out, err := cmd.CombinedOutput(); err != nil {
			ui.Warnf("clone: _spec not lifted (%v): %s — its edits are under %s/", err, strings.TrimSpace(string(out)), sbx.CloneRefs(in.Sid))
			return
		}
		ui.Storef("clone: %d _spec file(s) lifted from the clone into %s", len(take), filepath.Join(wd, "_spec"))
	}
	if len(kept) > 0 {
		ui.Warnf("clone: kept the host's %s — the host changed them since the clone began; the clone's versions are under %s/",
			strings.Join(kept, ", "), sbx.CloneRefs(in.Sid))
	}
	rebase(head)
}

// trackedSpec reports whether dir holds a real _spec directory git tracks; a
// symlinked one is relinked live and needs no lift.
func trackedSpec(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, "_spec"))
	if err != nil || !fi.IsDir() {
		return false
	}
	paths, err := gitPaths(dir, "ls-files", "-z", "--", "_spec")
	return err == nil && len(paths) > 0
}

func gitPaths(dir string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// cloneCarry is one route home: the sbx exit code (for the empty sentinel),
// output worth quoting, and the error.
type cloneCarry func(in Input, cfg sbx.RunConfig) (int, string, error)

// carryClone reports whether branches reached home, and an error while they remain only in the sandbox.
func carryClone(in Input, cfg sbx.RunConfig, running bool, viaRemote, viaBundle cloneCarry) (bool, error) {
	if running {
		if _, out, err := viaRemote(in, cfg); err == nil {
			return true, nil
		} else {
			ui.Notef("clone: the sandbox's git remote did not answer (%v: %s) — carrying the branches out over `sbx exec` instead",
				err, strings.TrimSpace(out))
		}
	}
	code, out, err := viaBundle(in, cfg)
	switch {
	case err == nil:
		return true, nil
	case code == sbx.CloneBundleEmpty:
		ui.Notef("clone: the agent left no commit this repository does not already have")
		return false, nil
	default:
		ui.Warnf("clone: could not carry the agent's branches home (%v): %s", err, strings.TrimSpace(out))
		for _, l := range CloneRescueLines(cfg.Name, in.Sid, agentWorkdir(in, cfg.Mounts), in.RepoRoot) {
			ui.Notef("%s", l)
		}
		return false, fmt.Errorf("the agent's branches are still only in %s", cfg.Name)
	}
}

// CloneRescueLines is the by-hand recipe, using the transport that works on a
// stopped sandbox.
func CloneRescueLines(name, refsKey, workdir, repoRoot string) []string {
	if workdir == "" || repoRoot == "" {
		return nil
	}
	bundle := "/tmp/" + name + ".bundle"
	return []string{
		fmt.Sprintf("while %s exists: `sbx exec -w / %s -- git -C %s bundle create - --all > %s`",
			name, name, workdir, bundle),
		fmt.Sprintf("then: `git -C %s fetch %s '+refs/heads/*:%s/*'`", repoRoot, bundle, sbx.CloneRefs(refsKey)),
	}
}

func fetchViaRemote(in Input, cfg sbx.RunConfig) (int, string, error) {
	cmd, finish := guestCommand(guestExecDeadline, "git", sbx.CloneFetchArgs(in.RepoRoot, cfg.Name, in.Sid)...)
	out, err := cmd.CombinedOutput()
	code := exitCodeOf(err)
	return code, string(out), finish(err)
}

func bundleViaSbx(in Input, cfg sbx.RunConfig) (int, string, error) {
	wd := agentWorkdir(in, cfg.Mounts)
	if wd == "" {
		return -1, "", errors.New("no workspace mount to bundle from")
	}
	f, err := os.CreateTemp("", "proveo-clone-*.bundle")
	if err != nil {
		return -1, "", err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	src, finish := guestCommand(guestExecDeadline, sbx.Binary, sbx.CloneBundleArgs(cfg.Name, wd, hostTips(in.RepoRoot))...)
	var errb strings.Builder
	src.Stdout, src.Stderr = f, &errb
	runErr := src.Run()
	code := exitCodeOf(runErr)
	runErr = finish(runErr)
	if cerr := f.Close(); runErr == nil && cerr != nil {
		return -1, errb.String(), cerr
	}
	if runErr != nil {
		return code, errb.String(), runErr
	}
	out, err := exec.Command("git", sbx.CloneBundleFetchArgs(in.RepoRoot, path, in.Sid)...).CombinedOutput()
	if err != nil {
		return exitCodeOf(err), errb.String() + string(out), err
	}
	return 0, "", nil
}

func hostTips(repoRoot string) []string {
	out, err := exec.Command("git", sbx.CloneHostTipsArgs(repoRoot)...).Output()
	if err != nil {
		return nil
	}
	tips := strings.Fields(string(out))
	if len(tips) > sbx.CloneHostTipsCap {
		tips = tips[:sbx.CloneHostTipsCap]
	}
	return tips
}

func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// adoptLead is the carried ref to suggest adopting: the first agent branch holding commits
// HEAD lacks, else the first proveo/ snapshot ref that does, else the first ref.
func adoptLead(names []string, prefix string, ahead func(string) bool) string {
	for _, snapshot := range []bool{false, true} {
		for _, n := range names {
			if strings.HasPrefix(strings.TrimPrefix(n, prefix), "proveo/") == snapshot && ahead(n) {
				return n
			}
		}
	}
	return names[0]
}

func reportCloneRefs(in Input, cfg sbx.RunConfig) {
	refs, _ := exec.Command("git", "-C", in.RepoRoot, "for-each-ref",
		"--format=%(refname:short)", sbx.CloneRefs(in.Sid)+"/").Output()
	names := strings.Fields(string(refs))
	if len(names) == 0 {
		ui.Notef("clone: the agent left no branches to fetch")
		return
	}
	ui.Section(ui.SectionResults)
	ui.Storef("clone: the agent's work is in your repository under %s/ — %s", sbx.CloneRefs(in.Sid), strings.Join(names, " "))
	lead := adoptLead(names, strings.TrimPrefix(sbx.CloneRefs(in.Sid), "refs/")+"/", func(n string) bool {
		out, err := exec.Command("git", "-C", in.RepoRoot, "rev-list", "--count", "HEAD.."+n).Output()
		return err == nil && strings.TrimSpace(string(out)) != "0"
	})
	ui.Notef("review: `git log --oneline main..%s` · adopt: `git checkout -b <branch> %s`", lead, lead)
}

func liftClonedOutput(in Input, cfg sbx.RunConfig, lift func(args []string, into string) (int, string, error)) {
	rel, ok := nestedRel(in.RepoRoot, in.OutputDir)
	wd := agentWorkdir(in, cfg.Mounts)
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

func liftViaSbx(args []string, into string) (int, string, error) {
	src, finish := guestCommand(guestExecDeadline, sbx.Binary, args...)
	var srcErr strings.Builder
	src.Stderr = &srcErr
	stdout, err := src.StdoutPipe()
	if err != nil {
		return -1, "", finish(err)
	}
	untar := exec.Command("tar", "-xf", "-", "-C", into)
	untar.Stdin = stdout
	var untarOut strings.Builder
	untar.Stdout, untar.Stderr = &untarOut, &untarOut
	if err := src.Start(); err != nil {
		return -1, srcErr.String(), finish(err)
	}
	untarErr := untar.Run()
	srcRun := src.Wait()
	if srcRun != nil {
		return exitCodeOf(srcRun), srcErr.String() + untarOut.String(), finish(srcRun)
	}
	_ = finish(nil)
	if untarErr != nil {
		return 0, untarOut.String(), untarErr
	}
	return 0, "", nil
}

func FreeLoopbackPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

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

// PublishedPort is one dev server the run starts in the sandbox and publishes to the host's loopback.
type PublishedPort struct {
	Host, Guest int
	What        string
	Dir         string // workspace-relative directory the command runs in
	Cmd         string // shell command that starts the server on every interface
}

// PlanPorts puts each guest port on its first free devports.HostPorts entry outside reserved; a port with none is dropped.
func PlanPorts(ports []PublishedPort, free func(int) bool, reserved ...int) (kept, dropped []PublishedPort) {
	taken := map[int]bool{}
	for _, h := range reserved {
		taken[h] = true
	}
	for _, p := range ports {
		h, ok := devports.HostPortFor(p.Guest, func(h int) bool { return !taken[h] && free(h) })
		if !ok {
			dropped = append(dropped, p)
			continue
		}
		taken[h] = true
		p.Host = h
		kept = append(kept, p)
	}
	return kept, dropped
}

// LoopbackFree reports whether port can be bound on the host's loopback.
func LoopbackFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func portPublish(in Input) []string {
	out := make([]string, 0, len(in.Ports))
	for _, p := range in.Ports {
		out = append(out, fmt.Sprintf("%d:%d", p.Host, p.Guest))
	}
	return out
}

// DevLog is where a published port's server writes inside the sandbox.
func DevLog(port int) string { return fmt.Sprintf("/tmp/proveo-dev-%d.log", port) }

// ReportPorts names each published port where the operator will open it.
func ReportPorts(in Input) {
	if len(in.Ports) == 0 {
		return
	}
	ui.Section(ui.SectionInterface)
	for _, p := range in.Ports {
		moved := ""
		if first := devports.HostPorts(p.Guest); len(first) > 0 && p.Host != first[0] {
			moved = fmt.Sprintf(" — host :%d was taken", first[0])
		}
		ui.Hostf("port %d: http://127.0.0.1:%d → sandbox :%d (%s)%s", p.Guest, p.Host, p.Guest, p.What, moved)
		ui.Notef("runs `%s` in %s — log %s", p.Cmd, p.Dir, DevLog(p.Guest))
	}
}

// AndroidLog is where the seed's Android install writes inside the sandbox.
const AndroidLog = "/tmp/proveo-android-install.log"

// ReportAndroidApps names each app the run installs on the host emulator.
func ReportAndroidApps(in Input) {
	if len(in.AndroidApps) == 0 {
		return
	}
	ui.Section(ui.SectionInterface)
	for _, a := range in.AndroidApps {
		ui.Hostf("android app: %s — gradle %s, then launch, once the sandbox toolchain is ready", a.AppID, a.Task())
	}
	ui.Notef("log %s inside the sandbox", AndroidLog)
}

// AndroidInstallScript hands the chosen modules to the seed's proveo_android_install.
func AndroidInstallScript(workdir string, env []string, apps []devports.AndroidApp) string {
	var b strings.Builder
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "export %s=%q\n", k, v)
	}
	fmt.Fprintf(&b, "cd %q || exit 1\nsource /entrypoint-lib.sh\nproveo_android_install", workdir)
	for _, a := range apps {
		fmt.Fprintf(&b, " %q", a.Module+"|"+a.AppID)
	}
	b.WriteString("\n")
	return b.String()
}

// StartAndroidInstall runs the install once the sandbox is up; the seed function waits for its own toolchain.
func StartAndroidInstall(in Input, cfg sbx.RunConfig) func() {
	workdir := agentWorkdir(in, cfg.Mounts)
	if len(in.AndroidApps) == 0 || workdir == "" {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		for !sbx.Running(cfg.Name) {
			select {
			case <-stop:
				return
			case <-time.After(devRetryMin):
			}
		}
		c := exec.Command(sbx.Binary, "exec", "-w", "/", cfg.Name, "--", "bash", "-lc",
			AndroidInstallScript(workdir, in.AndroidEnv, in.AndroidApps))
		c.Stdout, c.Stderr = io.Discard, io.Discard
		if c.Start() != nil {
			return
		}
		done := make(chan struct{})
		go func() { _ = c.Wait(); close(done) }()
		select {
		case <-stop:
			_ = c.Process.Kill()
		case <-done:
		}
	}()
	return func() { close(stop) }
}

// DevServerScript starts cmd in dir unless something already listens on port; a clone's script first waits for the clone.
// SPEC: _spec/internal/devports/dev-ports.puml
func DevServerScript(workdir string, clone bool, p PublishedPort) string {
	var b strings.Builder
	fmt.Fprintf(&b, "if (exec 3<>/dev/tcp/127.0.0.1/%d) 2>/dev/null; then exit 0; fi\n", p.Guest)
	if clone {
		fmt.Fprintf(&b, "n=%d\nuntil git -C %q rev-parse -q --verify HEAD >/dev/null 2>&1 && [ -n \"$(ls -A %q 2>/dev/null)\" ]; do\n"+
			"  n=$((n-1)); [ \"$n\" -gt 0 ] || exit 1; sleep 1\ndone\n", int(devRetryMax/time.Second), workdir, workdir)
	}
	fmt.Fprintf(&b, "cd %q || exit 1\nexec %s >>%s 2>&1\n", filepath.Join(workdir, p.Dir), p.Cmd, DevLog(p.Guest))
	return b.String()
}

const (
	devRetryMin = 3 * time.Second
	devRetryMax = 30 * time.Second
)

// StartDevServers keeps each published port's server running while the sandbox runs.
func StartDevServers(in Input, cfg sbx.RunConfig) func() {
	workdir := agentWorkdir(in, cfg.Mounts)
	if len(in.Ports) == 0 || workdir == "" {
		return func() {}
	}
	stop := make(chan struct{})
	wait := func(d time.Duration) bool {
		select {
		case <-stop:
			return false
		case <-time.After(d):
			return true
		}
	}
	for _, p := range in.Ports {
		go func() {
			delay := devRetryMin
			for {
				if !sbx.Running(cfg.Name) {
					if !wait(devRetryMin) {
						return
					}
					continue
				}
				c := exec.Command(sbx.Binary, "exec", "-w", "/", cfg.Name, "--", "bash", "-lc", DevServerScript(workdir, in.Clone, p))
				c.Stdout, c.Stderr = io.Discard, io.Discard
				began := time.Now()
				if err := c.Start(); err == nil {
					done := make(chan struct{})
					go func() { _ = c.Wait(); close(done) }()
					select {
					case <-stop:
						_ = c.Process.Kill()
						return
					case <-done:
					}
				}
				if time.Since(began) > devRetryMax {
					delay = devRetryMin
				}
				if !wait(delay) {
					return
				}
				delay = min(delay*2, devRetryMax)
			}
		}()
	}
	return func() { close(stop) }
}

func cdpPublish(in Input) []string {
	if !in.Browser || in.CDPHostPort <= 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d:%d", in.CDPHostPort, sbx.CDPRelayPort)}
}

// CloneRefVar names the ref the seed checks out in a worktree's clone.
const CloneRefVar = "PROVEO_CLONE_REF"

// CloneMainVar names the clone a linked worktree is added to inside the VM.
const CloneMainVar = "PROVEO_CLONE_MAIN"

// agentWorkdir is where the agent works: the linked worktree's own path in a
// worktree clone, else the primary workspace.
func agentWorkdir(in Input, mounts []sbx.Mount) string {
	if worktreeClone(in) {
		return realPath(in.RepoRoot)
	}
	return FirstHost(mounts)
}

// realPath is a host path as sbx mounts it inside the VM: symlinks resolved.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// CloneLinksVar carries rel=target pairs, '|'-separated, the seed recreates in a clone.
const CloneLinksVar = "PROVEO_CLONE_LINKS"

// cloneLinks lists the mounted escaping symlinks a clone lacks when git ignores them.
func cloneLinks(in Input) string {
	if !in.Clone {
		return ""
	}
	var pairs []string
	for _, l := range in.Links {
		if l.Action != workspace.LinkMounted || strings.ContainsAny(l.Rel+l.Target, "|=") {
			continue
		}
		pairs = append(pairs, l.Rel+"="+l.Target)
	}
	return strings.Join(pairs, "|")
}

// CloneEnvVar names the staged .env the seed links into a clone.
const CloneEnvVar = "PROVEO_CLONE_ENV"

// CloneVar marks a clone workspace, so the seed records the clone's base.
const CloneVar = "PROVEO_CLONE_WORKSPACE"

// SharesVar carries rel=rw|ro pairs, '|'-separated, the seed asserts before it releases the agent.
// SPEC: _spec/internal/sbx/clone-workspace.puml
const SharesVar = "PROVEO_SHARES"

// shares lists the host's _spec and .env at the agent workdir, and the access the agent needs to each.
func shares(in Input, binds []sbx.Mount) string {
	wd := agentWorkdir(in, binds)
	if wd == "" {
		return ""
	}
	mode := "rw"
	if len(binds) > 0 && binds[0].ReadOnly && !in.Clone {
		mode = "ro"
	}
	var pairs []string
	if fi, err := os.Stat(filepath.Join(wd, "_spec")); err == nil && fi.IsDir() {
		pairs = append(pairs, "_spec="+mode)
	}
	switch {
	case in.Clone && in.CloneEnv != "":
		pairs = append(pairs, ".env=rw")
	case !in.Clone && workspace.ProjectEnvFile(wd, "") != "":
		pairs = append(pairs, ".env="+mode)
	}
	return strings.Join(pairs, "|")
}

func worktreeClone(in Input) bool { return in.Clone && in.CloneSource.Main != "" }

// cloneThroughMain makes the main worktree the primary workspace and drops the
// linked worktree's own bind; the worktree-nested output dir stays for SplitNested.
func cloneThroughMain(in Input, mounts []sbx.Mount) []sbx.Mount {
	out := []sbx.Mount{{Host: in.CloneSource.Main}}
	var dropped []string
	for _, m := range mounts {
		host := filepath.Clean(m.Host)
		if host == filepath.Clean(in.RepoRoot) || host == filepath.Clean(in.CloneSource.Main) ||
			m.Container == workspace.ContainerGitCommonDir {
			continue
		}
		if _, nested := nestedRel(in.CloneSource.Main, m.Host); nested {
			dropped = append(dropped, m.Host)
			continue
		}
		out = append(out, m)
	}
	warnUnmountable(in.CloneSource.Main, "main worktree", dropped)
	return out
}

// warnUnmountable warns once per top-level entry of root that holds dropped mounts.
func warnUnmountable(root, where string, hosts []string) {
	count := map[string]int{}
	first := map[string]string{}
	var tops []string
	for _, h := range hosts {
		rel, _ := nestedRel(root, h)
		top, _, _ := strings.Cut(rel, "/")
		if count[top] == 0 {
			tops = append(tops, top)
			first[top] = h
		}
		count[top]++
	}
	for _, top := range tops {
		what := first[top]
		if count[top] > 1 {
			what = fmt.Sprintf("%s (%d entries)", filepath.Join(root, top), count[top])
		}
		ui.Warnf("clone: %s is inside the %s and cannot be mounted into a clone — read it from the clone instead", what, where)
	}
}

// sandboxKey is the workspace a sandbox name is derived from.
func sandboxKey(in Input, mounts []sbx.Mount) string {
	if worktreeClone(in) {
		return in.RepoRoot
	}
	return FirstHost(WorkspaceBinds(mounts))
}

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

func KeptLines(name, runLog string) []string {
	lines := []string{fmt.Sprintf(
		"sandbox %s kept for diagnosis (the run failed) — `sbx exec -it %s bash`, then `sbx rm --force %s`",
		name, name, name)}
	if strings.TrimSpace(runLog) != "" {
		lines = append(lines,
			fmt.Sprintf("every line this run printed, posture and warnings included: %s", runLog))
	}
	return lines
}

func IDEAttachLines(in Input, cfg sbx.RunConfig, live bool) []string {
	name, workdir := strings.TrimSpace(cfg.Name), agentWorkdir(in, cfg.Mounts)
	if name == "" || workdir == "" {
		return nil
	}
	when := ""
	if live {
		when = " (live)"
	}
	lines := []string{fmt.Sprintf(
		"IDE attach%s: run `sbx %s` once, then connect to `%s` and open %s",
		when, strings.Join(sbx.SetupSSHArgs(), " "), sbx.SSHHost(name), workdir),
		fmt.Sprintf("JetBrains: File → Remote Development → SSH → host `%s`, use OpenSSH config, project %s",
			sbx.SSHHost(name), workdir),
		fmt.Sprintf("Toolbox (an IDE already installed in that environment): `%s`",
			sbx.GatewayLink(name, workdir)),
		fmt.Sprintf("VS Code: `%s`", sbx.ShellLine(sbx.VSCodeRemoteArgs(name, workdir)))}
	if in.Clone {
		lines = append(lines, fmt.Sprintf(
			"a local Open or Recent of %s edits the HOST checkout, not this clone — attach through a link above",
			workdir))
	}
	if worktreeClone(in) {
		lines = append(lines, fmt.Sprintf(
			"workdir: linked worktree of %s inside the clone; `sbx ls` lists %s as the workspace",
			in.CloneSource.Main, in.CloneSource.Main))
	}
	if live {
		lines = append(lines, "the running agent and the editor both write this tree")
	}
	if !in.Clone {
		return append(lines,
			"workspace: mounted checkout — IDE saves write the host tree directly")
	}
	refsKey := strings.TrimSpace(in.Sid)
	if refsKey == "" {
		refsKey = name
	}
	fetch := strings.Join(sbx.CloneFetchArgs(in.RepoRoot, name, refsKey), " ")
	return append(lines,
		"workspace: DISPOSABLE CLONE — commit IDE edits before removing the sandbox; they do not appear in the host checkout",
		fmt.Sprintf("carry IDE commits home before `sbx rm`: `git %s`", fetch))
}

func PrintIDEAttach(in Input, cfg sbx.RunConfig, live bool) {
	lines := IDEAttachLines(in, cfg, live)
	if len(lines) == 0 {
		return
	}
	if live {
		ui.Section(ui.SectionInterface)
	}
	for i, l := range lines {
		if i == 0 {
			ui.Hostf("%s", l)
			continue
		}
		ui.Notef("%s", l)
	}
}

func WorkspaceBinds(mounts []sbx.Mount) []sbx.Mount {
	var out []sbx.Mount
	seen := map[string]bool{}
	for _, m := range mounts {
		if strings.HasPrefix(m.Container, proveohome.ContainerHome+"/") {
			continue // nested under home; its nesting cannot be reproduced
		}
		if fi, err := os.Stat(m.Host); err != nil || !fi.IsDir() {
			continue
		}
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
	return append(out, sbx.StateHomeVar+"="+host)
}

func FirstHost(mounts []sbx.Mount) string {
	if len(mounts) == 0 {
		return ""
	}
	return mounts[0].Host
}

// Input is the resolved input to the sbx backend.
type Input struct {
	Target, Image, AuthVar string
	MCPGateway             bool     // the operator allowed sbx's MCP gateway hooks (PROVEO_SBX_MCP or the mcp row)
	Desktop                bool     // Hermes Desktop drives the sandbox over SSH instead of this terminal
	DesktopLaunch          []string // opens Hermes Desktop on the host
	Clone                  bool
	CloneKnown             bool   // Clone is the sandbox's own mode: this run created it, or teardown asked it
	RepoRoot               string // host repository root: where a clone's commits are fetched back to
	OutputDir              string
	Browser                bool
	CDPHostPort            int
	Ports                  []PublishedPort
	AndroidApps            []devports.AndroidApp
	AndroidEnv             []string // the host adb server's guest env, for the install's adb
	Extra                  []string
	Roles                  provider.Roles
	Evidence               string // was params.evidenceOrDefault()
	Forwards               bool   // was params.forwards()
	Man                    manifest.Manifest
	// ImageEntrypoint reads the image's declared ENTRYPOINT; a sandbox Kit needs
	// it verbatim. Injected so Spec stays testable without docker.
	ImageEntrypoint func(image string) []string
	// AgentEnv is what the egress plan decided for the agent, as KEY=VALUE.
	AgentEnv         []string
	Sid, RunDir      string
	Mounts           []runner.Mount
	Lookup           func(string) string
	Detected         []string
	GitEnv           []string
	HomeEnv          []string
	ScopeRel         string
	WorktreeFallback bool
	CloneSource      workspace.WorktreeSource // a linked worktree cloned through its main worktree
	CloneEnv         string                   // staged project .env, mounted read-write for the seed to link
	CloneEnvBase     string                   // the staged bytes at launch, outside the mount
	CloneEnvHost     string                   // the host .env teardown merges the clone's edits into
	ProjectEnv       string                   // the project .env whose values a new sandbox gets through --env-file
	CloneEnvStrip    []string                 // brokered keys the staged copy dropped
	Links            []workspace.Link         // escaping symlinks the plan resolved; a clone recreates the mounted ones
	WorktreeEnv      []string
	DataDir          string
	Memory           string
	CPUs             int
	HomeRoot         string
	RunLog           string
	HomeAccess       HomeAccess
}

// imageEntrypoint reads the image's declared ENTRYPOINT, or nothing when the
// image cannot be inspected.
func imageEntrypoint(in Input) []string {
	if in.ImageEntrypoint == nil || in.Image == "" {
		return nil
	}
	return in.ImageEntrypoint(in.Image)
}

// Harness is the def a target belongs to. One manifest owns several targets —
// its variant images — and they all share the def's sbx agent and launch.
func Harness(in Input) string {
	if in.Man.Name != "" {
		return in.Man.Name
	}
	return in.Target
}

func Spec(in Input) (sbx.RunConfig, Workload, [][2]string) {
	// The Kit allows only what add-ons wire up; sbx's own policy decides all other egress.
	hosts := map[string]bool{}
	addHost := func(h string) {
		for _, p := range sbx.DomainPatterns(h) {
			hosts[p] = true
		}
	}
	if sbx.HostModelRef(envValue(in.AgentEnv, "OLLAMA_API_BASE")) {
		hosts[sbx.HostOllamaPolicyHost] = true
	}
	if port, err := strconv.Atoi(envValue(in.AgentEnv, hostcdp.EnvPort)); err == nil {
		hosts[hostcdp.PolicyHost(port)] = true
	}
	if port, err := strconv.Atoi(envValue(in.AgentEnv, hostadb.EnvPort)); err == nil {
		hosts[hostadb.PolicyHost(port)] = true
		for _, h := range hostadb.InstallHosts {
			addHost(h)
		}
	}
	allow := make([]string, 0, len(hosts))
	for h := range hosts {
		allow = append(allow, h)
	}
	sort.Strings(allow)

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
		if _, kind := credentials.StoreName(name, in.Target); kind == credentials.StoreUninjectable {
			addForward(name) // nothing to attach: a signing key or a credentials file, not a header
			return
		}
		if in.Forwards {
			addForward(name) // the operator chose the complete route over the safer one
			return
		}
		if service, kind := credentials.StoreName(name, in.Target); kind == credentials.StoreAPIKey && sbx.HasOAuthSlot(service) {
			ui.Notef("sandbox secret: %s not stored — sbx's global %q entry also holds the subscription, and an API key "+
				"there outranks it in every sandbox; to bill per token, `sbx secret set %s` yourself", name, service, service)
			return
		}
		secrets = append(secrets, [2]string{name, v})
	}
	suppressedAuth := credentials.AuthSuppressor(in.Man, in.Target, in.AuthVar, in.HomeRoot, in.Lookup)
	for _, e := range in.Man.Env {
		if !e.Secret {
			continue
		}
		if suppressedAuth(e.Name) {
			continue
		}
		addSecret(e.Name)
	}
	for _, k := range provider.KeyVars() {
		if !in.Man.Capabilities.AllowsProvider(credentials.ProviderOfKeyVar(k)) {
			continue
		}
		if suppressedAuth(k) {
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
	if set := proveohome.ConfigSet(in.Man.Home); set != "" {
		env = append(env, proveohome.ConfigSetVar+"="+set)
	}
	if files := proveohome.ConfigFiles(in.Man.Home); files != "" {
		env = append(env, proveohome.ConfigFilesVar+"="+files)
	}
	env = append(env, in.Man.AgentEnvPairs(in.Lookup)...)
	env = append(env, in.GitEnv...)
	env = append(env, in.HomeEnv...)
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
	if in.Man.IsAssistant() {
		env = append(env, "PROVEO_AGENT_KIND="+manifest.KindAssistant)
	}
	if in.WorktreeFallback && !worktreeClone(in) {
		env = append(env, in.WorktreeEnv...)
	}
	if worktreeClone(in) {
		env = append(env, CloneRefVar+"="+in.CloneSource.Ref, CloneMainVar+"="+realPath(in.CloneSource.Main))
	}
	if in.Clone {
		env = append(env, CloneVar+"=1")
	}
	if in.Clone && in.CloneEnv != "" {
		env = append(env, CloneEnvVar+"="+in.CloneEnv)
	}
	if v := cloneLinks(in); v != "" {
		env = append(env, CloneLinksVar+"="+v)
	}

	var mounts, homeSourceMounts []sbx.Mount
	for _, m := range in.Mounts {
		sbxMount := sbx.Mount{Host: m.Host, Container: m.Container, ReadOnly: m.ReadOnly}
		homeSourceMounts = append(homeSourceMounts, sbxMount)
		if m.Container == proveohome.ContainerHome && filepath.Clean(m.Host) == filepath.Clean(in.HomeRoot) &&
			len(in.HomeAccess.Mounts) > 0 {
			continue
		}
		mounts = append(mounts, sbxMount)
	}
	for _, m := range in.HomeAccess.Mounts {
		mounts = append(mounts, sbx.Mount{Host: m.Host, Container: m.Container, ReadOnly: m.ReadOnly})
	}
	if dir := scopedIndexDir(in); dir != "" && len(in.HomeAccess.Mounts) > 0 {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			mounts = append(mounts, sbx.Mount{Host: dir})
		}
	}
	if in.DataDir != "" {
		mounts = append(mounts, sbx.Mount{Host: in.DataDir, Container: "/workspace/data", ReadOnly: true})
	}
	if worktreeClone(in) {
		mounts = cloneThroughMain(in, mounts)
	}
	if in.Clone && in.RepoRoot != "" {
		var nested []sbx.Mount
		var dropped []string
		mounts, nested = SplitNested(in.RepoRoot, mounts)
		for _, m := range nested {
			if _, isOutput := nestedRel(in.RepoRoot, in.OutputDir); isOutput && filepath.Clean(m.Host) == filepath.Clean(in.OutputDir) {
				ui.Storef("clone: %s is inside the repository, so it is not mounted live — sbx clones only into an "+
					"empty workspace; the agent writes it inside the clone and proveo lifts it back here at teardown", m.Host)
				continue
			}
			dropped = append(dropped, m.Host)
		}
		warnUnmountable(in.RepoRoot, "repository", dropped)
	}
	if in.Clone && in.CloneEnv != "" {
		mounts = append(mounts, sbx.Mount{Host: filepath.Dir(in.CloneEnv)})
	}

	harness := Harness(in)
	agent := sbx.AgentName(harness)
	builtin := sbx.BuiltinAgent(harness)
	up, hasUp, upErr := sbx.UpstreamKitFor(harness, sbx.UpstreamOptions{MCPGateway: in.MCPGateway})
	if upErr != nil {
		ui.Warnf("sandbox: the published %s kit could not be read (%v) — launching from the image alone", harness, upErr)
		hasUp = false
	}
	recipe := sbx.RecipeV3{From: in.Image}
	var command []string
	switch entrypoint := imageEntrypoint(in); {
	case hasUp:
		recipe.Entrypoint, recipe.Env = up.Launch, up.Env
		flags := withMobileMCP(harness, in.AgentEnv, append([]string{}, in.Extra...))
		flags = withLocalModelTools(builtin, in.AgentEnv, flags)
		command = sbx.LaunchArgs(up.Launch, flags)
	case len(entrypoint) == 0:
		ui.Warnf("sandbox: no readable ENTRYPOINT on %s — %s launches through bash", in.Image, harness)
		recipe.Entrypoint = []string{"bash"}
		command = sbx.ShellLaunch(harness, in.Extra)
	default:
		command = sbx.LaunchArgs(entrypoint, withMobileMCP(harness, in.AgentEnv, append([]string{}, in.Extra...)))
	}
	cfg := sbx.RunConfig{
		Name:    sbx.SandboxName(in.Target, sandboxKey(in, mounts)),
		KitDir:  sbx.KitDirV3(filepath.Join(in.RunDir, "sbx", "kit"), agent),
		Memory:  in.Memory,
		CPUs:    in.CPUs,
		Clone:   in.Clone,
		Publish: append(cdpPublish(in), portPublish(in)...),
		Agent:   agent,
		Mounts:  WorkspaceBinds(mounts),
		Env:     DeclineMCPGateway(in.MCPGateway, Home(launchEnv(in, cmp.Or(builtin, agent), env, mounts), homeSourceMounts)),
		Command: command,
	}
	if in.HomeAccess.FilesRoot != "" {
		cfg.Env = append(cfg.Env, proveohome.ConfigFilesRootVar+"="+in.HomeAccess.FilesRoot)
	}

	caps := []sbx.CapabilityV3{{Type: sbx.CapSbx}}
	if hasUp {
		caps = append(caps, up.Capabilities...)
	}
	creds, secrets := agentCredentials(in, secrets)
	owned := upstreamServices(caps)
	for _, c := range creds {
		if owned[c.Service] {
			continue
		}
		caps = append(caps, sbx.CapabilityV3{Type: sbx.CapCredential, Optional: true, Config: c})
		for _, inj := range c.APIKey.Inject {
			if !hosts[inj.Domain] {
				hosts[inj.Domain] = true
				allow = append(allow, inj.Domain)
			}
		}
	}
	sort.Strings(allow)
	var err error
	if caps, err = sbx.WithRuntimeAllow(caps, allow); err == nil {
		caps, err = sbx.WithLifecycle(caps, sbx.LifecycleV3{Startup: startupHooks(in, !hasUp, envNames(cfg.Env))})
	}
	if err != nil {
		ui.Warnf("sandbox: the workload's capabilities could not be merged (%v)", err)
	}
	w := Workload{
		Descriptor: sbx.WorkloadV3{
			DisplayName:  in.Target + " (proveo)",
			Description:  "proveo harness " + in.Target + ": image, launch, reachability and the seed step.",
			Version:      "1.0.0",
			Capabilities: caps,
		},
		Recipe: recipe,
		Files:  up.Files,
	}
	return cfg, w, secrets
}

// upstreamServices are the credential services a copied published kit already owns (one owner per service).
func upstreamServices(caps []sbx.CapabilityV3) map[string]bool {
	out := map[string]bool{}
	for _, c := range caps {
		if m, ok := c.Config.(map[string]any); ok && c.Type == sbx.CapCredential {
			if svc, _ := m["service"].(string); svc != "" {
				out[svc] = true
			}
		}
	}
	return out
}

// envNames are the variable names a run passes with -e, which every proveo hook declares (hooks see only what they name).
func envNames(env []string) []string {
	var out []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// startupHooks are proveo's seed steps as lifecycle@1 startup hooks: they re-run on every boot, as the v2 startup did.
func startupHooks(in Input, ownLaunch bool, env []string) []sbx.HookV3 {
	var out []sbx.HookV3
	for _, c := range startupCommands(in, ownLaunch) {
		user := c.User
		if user == "root" {
			user = "0"
		}
		out = append(out, sbx.HookV3{Command: c.Command, User: user, Background: c.Background, Env: env, Description: c.Description})
	}
	return out
}

// MCPGatewayVar is the variable sbx's built-in agent kits gate their MCP
// registration on.
const MCPGatewayVar = "MCP_GATEWAY_URL"

func DeclineMCPGateway(allowed bool, env []string) []string {
	if allowed {
		return env
	}
	for _, e := range env {
		if strings.HasPrefix(e, MCPGatewayVar+"=") {
			return env
		}
	}
	return append(env, MCPGatewayVar+"=")
}

func MCPGatewayAllowed() bool { return envflag.On(os.Getenv("PROVEO_SBX_MCP")) }

// LocalModelTools is the built-in tool set Claude Code keeps on a local model, where each tool schema costs prefill.
const LocalModelTools = "Bash,Read,Edit,Write"

// withLocalModelTools narrows sbx claude's built-in tools when the run uses a local model.
func withLocalModelTools(agent string, env, command []string) []string {
	if agent != sbx.BuiltinAgent("claudecode") || envValue(env, "PROVEO_LOCAL_MODEL") == "" {
		return command
	}
	for _, a := range command {
		if a == "--tools" {
			return command
		}
	}
	return append([]string{"--tools", LocalModelTools}, command...)
}

// withMobileMCP prepends the harness's per-launch mobile-server flags when the android add-on is on.
func withMobileMCP(harness string, env, command []string) []string {
	flags := hostadb.Launches[harness].Flags
	if len(flags) == 0 || envValue(env, hostadb.EnvPort) == "" {
		return command
	}
	for _, a := range command {
		if a == flags[0] {
			return command
		}
	}
	return append(append([]string{}, flags...), command...)
}

func Run(in Input) error {
	homeAccess := in.HomeAccess
	if homeAccess.Root == "" && in.HomeRoot != "" {
		var err error
		homeAccess, err = PrepareHomeAccess(in.HomeRoot, in.RunDir, in.Man.Home)
		if err != nil {
			return err
		}
	}
	noteSkippedLinks(homeAccess)
	in.HomeAccess = homeAccess
	keepHomeAccess := false
	defer func() {
		if !keepHomeAccess {
			homeAccess.Cleanup()
		}
	}()
	cfg, w, secrets := Spec(in)
	envValues := 0
	if path, n, err := seedProjectEnv(in.ProjectEnv, in.RunDir, in.Forwards, cfg.Env); err != nil {
		ui.Warnf("project .env not seeded into the sandbox: %v", err)
	} else {
		cfg.EnvFile, envValues = path, n
	}
	kitDir, err := sbx.WriteWorkloadV3(cfg.KitDir, w.Descriptor, w.Recipe, w.Files)
	if err != nil {
		return err
	}
	ui.Section(ui.SectionStarting)
	receipt := receiptOf(cfg, in.Image, workloadBytes(kitDir), in.Sid, sbx.LocalImageID(in.Image))
	if err := retireIfStale(in, cfg, receipt, sbx.Exists, sbx.Running, retireSandbox, ui.Notef, ui.Warnf); err != nil {
		return err
	}
	launchCfg := reuseOrCreate(cfg, sbx.Exists)
	if launchCfg.KitDir != "" {
		writeReceipt(cfg.Name, receipt)
	}
	created := launchCfg.KitDir != ""
	var child credentials.ChildEnv
	for _, e := range cfg.Env {
		if !strings.Contains(e, "=") {
			child.Add(e, in.Lookup)
		}
	}
	for _, kv := range secrets {
		ui.Section(ui.SectionSecrets)
		if hosts, service, ok := customSecretTarget(kv[0], in.Lookup); ok {
			ui.Hostf("sandbox secret: %s (custom — sbx does not know %q, so the hosts and the "+
				"variable are declared with it)", kv[0], service)
			if err := sbx.SecretSetCustom(hosts, kv[0], kv[1]); err != nil {
				return fmt.Errorf("sandbox secret %s: %w", kv[0], err)
			}
			continue
		}
		name, _ := credentials.StoreName(kv[0], in.Target)
		if name == "" {
			name = kv[0]
		}
		ui.Hostf("sandbox secret: %s stored as %s — sbx's proxy attaches it, the agent never holds it", kv[0], name)
		if err := sbx.SecretSet(name, kv[1]); err != nil {
			return fmt.Errorf("sandbox secret %s: %w", name, err)
		}
	}
	if len(secrets) > 0 {
		ui.Notef("sbx's secret store is host-wide and outlives this run — `sbx secret ls`")
	}
	if opencodeTarget(in.Target) {
		launchCfg, created, err = ensureOpenCodeEngine(in, cfg)
		if err != nil {
			return err
		}
	}
	PrintIDEAttach(in, cfg, true)
	ReportPorts(in)
	ReportAndroidApps(in)
	defer StartDevServers(in, cfg)()
	defer StartAndroidInstall(in, cfg)()
	defer StartCDPViewport(in, launchCfg)()
	in.CloneKnown = created
	if envValues > 0 {
		if created {
			ui.Storef("project .env: %d value(s) seeded into %s with --env-file", envValues, cfg.Name)
		} else {
			ui.Notef("project .env: %s keeps the values it was created with; `sbx rm --force %s` reseeds them", cfg.Name, cfg.Name)
		}
	}
	if launchCfg.KitDir != "" {
		if err := createSandbox(launchCfg); err != nil {
			return err
		}
		launchCfg = sbx.Reattach(launchCfg)
	}
	args := sbx.RunArgs(launchCfg)
	stdout, stderr, tail := agentio.Stdio(os.Stdout, os.Stderr, agentio.IsWriterTTY(os.Stdout))
	traceIn, stopTrace := agentio.Tracer(os.Getenv("PROVEO_TRACE_STDIN"))
	defer stopTrace()
	filtered := ptyproxy.Usable(os.Stdin, os.Stdout) && agentio.FilterEnabled()
	var forceRemove atomic.Bool
	runCtx, stopSignals, cancelRun, signalled := forwardStop()
	defer stopSignals()
	run := func() error {
		c := exec.CommandContext(runCtx, sbx.Binary, args...)
		c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
		c.WaitDelay = stopWait
		c.Env = child.Apply(os.Environ())
		if filtered || (traceIn != nil && ptyproxy.Usable(os.Stdin, os.Stdout)) {
			px := ptyproxy.New(os.Stdin, os.Stdout)
			px.DisableFilter = !filtered
			px.DropReports = true
			px.Tap = traceIn
			if tail != nil {
				px.OutTap = func(b []byte) { _, _ = tail.Write(b) }
			}
			if os.Getenv("PROVEO_SCHEDULE") == "" {
				px.HandleCtrlC(func(in io.Reader, _ io.Writer) ptyproxy.CtrlCAction {
					screen, err := px.OverlayScreen(in)
					if err != nil {
						return ptyproxy.CtrlCStay
					}
					if err := screen.Init(); err != nil {
						return ptyproxy.CtrlCStay
					}
					defer screen.Fini()
					switch choiceui.ConfirmSandboxClose(screen) {
					case choiceui.SandboxCloseRemove:
						return ptyproxy.CtrlCRemove
					case choiceui.SandboxClosePass:
						return ptyproxy.CtrlCPass
					default:
						return ptyproxy.CtrlCStay
					}
				}, func() {
					forceRemove.Store(true)
					cancelRun()
				})
			}
			return px.Run(c)
		}
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, stderr
		return c.Run()
	}
	if in.Desktop {
		run = func() error { return holdForDesktop(runCtx, cfg.Name, in.DesktopLaunch, SbxRun, startDetached) }
	}
	startedAt := time.Now()
	runErr := run()
	endedAt := time.Now()
	runErr, result := stopOutcome(runErr, signalled(), forceRemove.Load())
	if _, ok := result.(backend.Stopped); ok {
		ui.Hostf("%v — ending as on a normal exit", result)
	}
	defer func() {
		if clones, err := sandboxClone(in, cfg.Name); err == nil {
			in.Clone, in.CloneKnown = clones, true
		}
		reportPushedBranches(in, cfg, startedAt)
		CapturePolicyLog(in.RunDir, cfg.Name)
		CaptureMemoryEvidence(in.RunDir, cfg.Name)
		if runErr != nil && !forceRemove.Load() {
			said := false
			if lines := tail.Lines(); len(lines) > 0 {
				said = true
				ui.Section(ui.SectionResults)
				ui.Hostf("last output from the agent:")
				for _, l := range lines {
					ui.Notef("%s", l)
				}
			}
			restarted := !sbx.Running(cfg.Name)
			_ = TeardownClone(in, cfg)
			_, _ = SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun)
			if err := homeAccess.Commit(); err != nil {
				ui.Warnf("home-root config not preserved: %v", err)
			}
			if t := credentials.AgentTranscript(in.Target, in.HomeRoot, startedAt, endedAt); t != "" {
				said = true
				ui.Storef("what the agent actually said is in %s", t)
			} else if restarted && sbx.Exists(cfg.Name) {
				ui.Storef("no transcript from this run — the sandbox had already stopped, " +
					"so state was copied out after it ended and anything newer than the run is the harvest's own")
			}
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
			if sbx.Exists(cfg.Name) {
				keepHomeAccess = true
				PrintIDEAttach(in, cfg, false)
			}
			return
		}
		stranded := TeardownClone(in, cfg)
		if out, err := SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun); err != nil {
			ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(out))
		}
		if err := homeAccess.Commit(); err != nil {
			ui.Warnf("home-root config not preserved: %v", err)
		}
		switch normalEndOf(forceRemove.Load(), stranded, opencodeTarget(in.Target)) {
		case endKeepStranded:
			keepHomeAccess = true
			if opencodeTarget(in.Target) {
				ui.Warnf("%v — %s kept with its OpenCode history", stranded, cfg.Name)
			} else {
				ui.Warnf("%v — %s kept; `sbx rm --force %s` deletes it once the work is home", stranded, cfg.Name, cfg.Name)
			}
			PrintIDEAttach(in, cfg, false)
			return
		case endRetainEngine:
			keepHomeAccess = true
			ui.Storef("OpenCode: history lives in retained engine %s; `sbx rm --force %s` deletes it", cfg.Name, cfg.Name)
			return
		}
		if forceRemove.Load() {
			ui.Hostf("sbx rm --force %s — deleting the session", cfg.Name)
		}
		rmOut, rmErr := exec.Command(sbx.Binary, sbx.RemoveArgs(cfg.Name)...).CombinedOutput()
		if rmErr != nil && !sbx.NotFound(string(rmOut)) {
			keepHomeAccess = sbx.Exists(cfg.Name)
			ui.Warnf("sandbox teardown failed (%v): %s", rmErr, strings.TrimSpace(string(rmOut)))
			if keepHomeAccess {
				PrintIDEAttach(in, cfg, false)
			}
		}
	}()
	var ee *exec.ExitError
	if errors.As(result, &ee) {
		return backend.ExitError{Code: sbx.RunExitCode(ee.ExitCode(), tail.Lines())}
	}
	return result
}

// stopWait bounds how long the sbx client may take to exit once proveo forwards a stop.
const stopWait = 30 * time.Second

// forwardStop cancels the run's context on SIGTERM or SIGHUP; signalled names the signal
// that cancelled it, or 0 when proveo itself did.
func forwardStop() (ctx context.Context, stop func(), cancel context.CancelFunc, signalled func() syscall.Signal) {
	ctx, cancel = context.WithCancel(context.Background())
	var got atomic.Int32
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case s := <-sigs:
			if n, ok := s.(syscall.Signal); ok {
				got.Store(int32(n))
			}
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() { signal.Stop(sigs); cancel() }, cancel, func() syscall.Signal { return syscall.Signal(got.Load()) }
}

// stopOutcome splits how a run ended into what teardown reads and what Run returns: a
// forwarded stop tears down like a normal exit and returns backend.Stopped.
func stopOutcome(runErr error, sig syscall.Signal, forced bool) (teardown, result error) {
	if sig != 0 && !forced {
		return nil, backend.Stopped{Signal: sig}
	}
	return runErr, runErr
}

// normalEnd is what a normal-exit teardown does with the sandbox once the clone was carried.
type normalEnd int

const (
	endRemove normalEnd = iota
	endKeepStranded
	endRetainEngine
)

// normalEndOf removes the sandbox when the operator chose remove, keeps it while work is
// stranded in it, retains the OpenCode engine, and otherwise removes it.
func normalEndOf(forced bool, stranded error, retainsEngine bool) normalEnd {
	switch {
	case forced:
		return endRemove
	case stranded != nil:
		return endKeepStranded
	case retainsEngine:
		return endRetainEngine
	}
	return endRemove
}

// WarnBaseline reports sbx's network baseline and, unless it allows every host, the command that lets hosts through.
func WarnBaseline(hosts []string) {
	ui.Section(ui.SectionEgress)
	allowed, known := sbx.NetworkAllowed(unallowlistedProbe)
	switch {
	case known && allowed:
		ui.Notef("sbx's global network policy allows every host; sbx decides this run's egress")
		return
	case !known:
		ui.Notef("sbx network baseline: unreadable (`sbx policy check network %s`)", unallowlistedProbe)
	}
	if len(hosts) > 0 {
		ui.Notef("sbx's policy decides egress; if the agent cannot reach its provider, run on the host: `sbx policy allow network %s`",
			strings.Join(hosts, ","))
	}
}

// ReachHosts are the sbx patterns for the harness's declared hosts and the detected providers it may use.
func ReachHosts(man manifest.Manifest, detected []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		for _, p := range sbx.DomainPatterns(h) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	for _, h := range man.Capabilities.Hosts {
		add(h)
	}
	for _, name := range credentials.FilterProviders(detected, man.Capabilities) {
		if e, ok := provider.Lookup(name); ok {
			for _, h := range e.Hosts {
				add(h)
			}
		}
	}
	sort.Strings(out)
	return out
}

func CapturePolicyLog(runDir, name string) {
	if runDir == "" || name == "" {
		return
	}
	out, err := sbx.PolicyLog(name)
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return
	}
	dir := filepath.Join(runDir, "sbx")
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

// CaptureMemoryEvidence writes the guest's own account of its memory beside the
// policy log, and says so out loud when it names a kill.
var memoryEvidence = sbx.MemoryEvidence

func CaptureMemoryEvidence(runDir, name string) {
	restarted := !sbx.Running(name)
	if runDir == "" || name == "" {
		return
	}
	out, err := memoryEvidence(name)
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		return
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return
	}
	dir := filepath.Join(runDir, "sbx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, runlog.MemoryEvidenceFile)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return
	}
	ui.Section(ui.SectionResults)
	if sbx.OOMEvidence(out) {
		// The whole reason this function exists: an OOM is otherwise a freeze
		// with no cause, and the operator goes looking at the agent instead.
		ui.Warnf("the guest kernel reported an out-of-memory kill — a sandbox has NO SWAP, "+
			"so memory pressure kills rather than slows. Raise the ceiling with "+
			"`PROVEO_SBX_MEMORY=16g` (capped at 32g) and see %s", path)
		return
	}
	if restarted {
		// `sbx exec` starts a stopped sandbox, so this reading is a FRESH boot:
		// its dmesg is empty and its meminfo is idle. Silence here is not
		// evidence of no kill — it is evidence the kill's boot is gone.
		ui.Notef("memory logs: %s — the sandbox had already stopped, so this is a fresh boot; "+
			"a kill in the boot that died left no trace to read", path)
		return
	}
	ui.Storef("memory logs: %s", path)
}

var storedSecretNames = sbx.StoredSecretNames

func agentCredentials(in Input, secrets [][2]string) ([]sbx.CredentialV3, [][2]string) {
	var creds []sbx.CredentialV3
	stored := map[string]bool{}
	for _, n := range storedSecretNames() {
		stored[n] = true
	}
	seen := map[string]bool{}
	for _, name := range provider.Detect(in.Lookup) {
		if seen[name] || !in.Man.Capabilities.AllowsProvider(name) {
			continue
		}
		r, ok := provider.ResolveWith(name, in.AuthVar, in.Lookup)
		// !ok is a signed-request provider (AWS SigV4 and friends): nothing to
		// attach as a header, so nothing to proxy-manage.
		if !ok || r.EnvVar == "" || len(r.Hosts) == 0 {
			continue
		}
		if r.Header == "" || r.Query != "" {
			continue
		}
		// Nothing stored under the service name: sbx would ask a human, and an
		// unattended run stops there.
		if !stored[name] {
			continue
		}
		format := "%s"
		if r.Bearer {
			format = "Bearer %s"
		}
		// credential@1: every inject domain must appear, in the same form, in the runtime allow list.
		inject := make([]sbx.CredInjectV3, 0, len(r.Hosts)*2)
		for _, h := range r.Hosts {
			for _, d := range sbx.DomainPatterns(h) {
				inject = append(inject, sbx.CredInjectV3{Domain: d, Header: r.Header, Format: format})
			}
		}
		creds = append(creds, sbx.CredentialV3{
			Service: name,
			Phase:   "runtime",
			APIKey:  &sbx.APIKeyV3{Name: r.EnvVar, ProxyManaged: true, Inject: inject},
		})
		seen[name] = true
	}
	sort.Slice(creds, func(i, j int) bool { return creds[i].Service < creds[j].Service })
	return creds, secrets
}

// customSecretTarget answers whether a credential needs `sbx secret set-custom`
// rather than a plain service secret, and with which hosts.
//
// sbx injects a plain service secret only for the thirteen services it has
// built-in knowledge of. For any other provider it holds the value and knows
// neither the host to match nor the header to write, which is why a stored
// `opencode` secret still produced HTTP 401 while the same key forwarded fine.
// A custom secret carries the hosts and the variable with it, so the proxy can
// substitute at egress the way it does for a service it knows.
func customSecretTarget(envVar string, lookup func(string) string) (hosts []string, service string, ok bool) {
	for _, name := range provider.Names() {
		r, found := provider.ResolveWith(name, envVar, lookup)
		if !found || !strings.EqualFold(r.EnvVar, envVar) || len(r.Hosts) == 0 {
			continue
		}
		if sbx.IsBuiltinService(name) {
			return nil, name, false
		}
		for _, h := range r.Hosts {
			hosts = append(hosts, sbx.DomainPatterns(h)...)
		}
		return hosts, name, len(hosts) > 0
	}
	return nil, "", false
}

// sandboxAgentEnv keeps the pairs a sandbox can honour and drops the ones that
// name a proxy it does not have.
func sandboxAgentEnv(pairs []string) []string {
	var out []string
	for _, kv := range pairs {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || proxyOnlyVar(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func proxyOnlyVar(name string) bool {
	switch name {
	case "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy",
		"NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE",
		"SSL_CERT_FILE", "GIT_SSL_CAINFO":
		return true
	}
	return false
}

// startupCommands is the seed, plus the def's own entrypoint for a mixin kit; a
// worktree clone first gets its worktree directory. A built-in sbx agent gets
// the seed split at the release, its installs last and in the background; any
// other target keeps the whole seed in the foreground.
// SPEC: _spec/internal/sbx/startup-dispatch.puml
func startupCommands(in Input, ownAgent bool) []sbx.KitCommand {
	var cmds []sbx.KitCommand
	if worktreeClone(in) {
		cmds = append(cmds, sbx.WorktreeDirCommand(agentWorkdir(in, nil)))
	}
	split := sbx.BuiltinAgent(in.Target) != ""
	if split {
		cmds = append(cmds, sbx.SeedCommand(in.Target))
	} else {
		cmds = append(cmds, sbx.SeedFullCommand(in.Target))
	}
	if !ownAgent {
		cmds = append(cmds, sbx.SeedEntrypointCommand(in.Target))
	}
	if split {
		cmds = append(cmds, sbx.SeedProvisionCommand(in.Target))
	}
	return cmds
}

// gitSafeDirectoryEnv declares the repository root safe for git, in the env the agent inherits.
func gitSafeDirectoryEnv(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	root := repoRoot
	if r, err := filepath.EvalSymlinks(repoRoot); err == nil {
		root = r
	}
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=" + root,
	}
}

// launchConfigEnv renders OpenCode's per-launch model and MCP configuration.
func launchConfigEnv(agent string, agentEnv []string) []string {
	if agent != "opencode" {
		return nil
	}
	model, base := envValue(agentEnv, "PROVEO_LOCAL_MODEL"), envValue(agentEnv, "OLLAMA_API_BASE")
	mcp := envValue(agentEnv, hostadb.EnvPort) != ""
	if model == "" && !mcp {
		return nil
	}
	cfg := map[string]any{"$schema": "https://opencode.ai/config.json"}
	var out []string
	if model != "" {
		if base == "" {
			base = sbx.HostOllamaGuestBase
		}
		base = strings.TrimRight(base, "/")
		for strings.HasSuffix(base, "/v1") {
			base = strings.TrimRight(strings.TrimSuffix(base, "/v1"), "/")
		}
		cfg["providers"] = map[string]any{
			"ollama": map[string]any{
				"package":  "@opencode/ai/providers/openai-compatible",
				"name":     "Ollama (local)",
				"settings": map[string]any{"baseURL": base + "/v1", "apiKey": "ollama"},
				"models":   map[string]any{model: map[string]any{"name": model + " (local)"}},
			},
		}
		cfg["model"] = "ollama/" + model
		cfg["agents"] = map[string]any{"title": map[string]any{"model": "ollama/" + model}}
		out = append(out, "OPENCODE_MODEL=ollama/"+model, "OPENCODE_SMALL_MODEL=ollama/"+model)
	}
	if mcp {
		cfg["mcp"] = hostadb.Launches["opencode"].Opencode
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	return append([]string{"OPENCODE_CONFIG_CONTENT=" + string(b)}, out...)
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// scopedGitIndexEnv hides the repository paths a subproject scope does not
// mount, so `git status` in the sandbox does not report them as deleted.
// scopedGitIndexEnv names a host-built index that hides the repository paths a scope does not mount.
// scopedIndexDir is where a sub-scope's private git index lives on the host,
// under the proveo home; a narrowed home mounts it on its own.
func scopedIndexDir(in Input) string {
	if in.ScopeRel == "" || in.RepoRoot == "" || in.Sid == "" {
		return ""
	}
	home := in.HomeRoot
	if home == "" {
		home = stateHomeHost(homeBinds(in.Mounts))
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "git-index")
}

func homeBinds(mounts []runner.Mount) []sbx.Mount {
	var out []sbx.Mount
	for _, m := range mounts {
		out = append(out, sbx.Mount{Host: m.Host, Container: m.Container})
	}
	return out
}

func scopedGitIndexEnv(in Input, binds []sbx.Mount) []string {
	dir := scopedIndexDir(in)
	src := filepath.Join(in.RepoRoot, ".git", "index")
	if dir == "" || !exists(src) {
		return nil
	}
	dst := filepath.Join(dir, in.Sid)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return nil
	}
	var hidden []string
	for _, rel := range trackedFiles(in.RepoRoot) {
		if !mountedHost(filepath.Join(in.RepoRoot, rel), binds) {
			hidden = append(hidden, rel)
		}
	}
	if len(hidden) > 0 {
		cmd := exec.Command("git", append([]string{"-C", in.RepoRoot, "update-index", "--skip-worktree", "--"}, hidden...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+dst)
		if err := cmd.Run(); err != nil {
			return nil
		}
	}
	return []string{"GIT_INDEX_FILE=" + dst}
}

func stateHomeHost(binds []sbx.Mount) string {
	for _, m := range binds {
		if m.Container == proveohome.ContainerHome {
			return m.Host
		}
	}
	return ""
}

func trackedFiles(repoRoot string) []string {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

func mountedHost(path string, binds []sbx.Mount) bool {
	p := filepath.Clean(path)
	for _, m := range binds {
		h := filepath.Clean(m.Host)
		if p == h || strings.HasPrefix(p, h+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// launchEnv is everything the agent must already hold when sbx starts it: the
// plan's decision, the workdir, git's reach across the mount boundary and the
// repository's safety, the def's model wiring, and the scoped index.
// EnvHostOS tells the guest which OS the host runs, for fixes measured on one host OS only.
const EnvHostOS = "PROVEO_HOST_OS"

var hostOS = runtime.GOOS

func launchEnv(in Input, agent string, env []string, mounts []sbx.Mount) []string {
	binds := WorkspaceBinds(mounts)
	out := append([]string{}, env...)
	out = append(out, sandboxAgentEnv(in.AgentEnv)...)
	out = append(out, "PROVEO_WORKDIR="+agentWorkdir(in, binds), "GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
		EnvHostOS+"="+hostOS)
	if v := shares(in, binds); v != "" {
		out = append(out, SharesVar+"="+v)
	}
	if worktreeClone(in) {
		out = append(out, gitSafeDirectoryEnv(in.CloneSource.Main)...)
	} else {
		out = append(out, gitSafeDirectoryEnv(in.RepoRoot)...)
	}
	out = append(out, launchConfigEnv(agent, in.AgentEnv)...)
	out = append(out, scopedGitIndexEnv(in, binds)...)
	return out
}

// reuseOrCreate answers the collision proveo creates for itself: the sandbox
// name is derived from def and workspace so runs reuse one VM, and a failed run
// is KEPT for diagnosis — so the next run met a sandbox sbx will not give new
// workspaces to and stopped before the agent started.
// SPEC: _spec/internal/sbx/kit-lifecycle.puml
// createSandbox builds the workload and runs its install and startup hooks before any agent launches (P5).
func createSandbox(cfg sbx.RunConfig) error {
	c := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("sbx create %s: %w", cfg.Name, err)
	}
	return nil
}

// workloadBytes is the descriptor and recipe a receipt digests.
func workloadBytes(dir string) []byte {
	agent := filepath.Base(dir)
	desc, _ := os.ReadFile(filepath.Join(dir, agent+".yaml"))
	recipe, _ := os.ReadFile(filepath.Join(dir, agent+".dockerfile"))
	return append(desc, recipe...)
}

func reuseOrCreate(cfg sbx.RunConfig, exists func(string) bool) sbx.RunConfig {
	if !exists(cfg.Name) {
		return cfg
	}
	ui.Notef("re-attaching to %s, which an earlier run left here — its workspace is this one", cfg.Name)
	ui.Notef("for a sandbox built fresh instead: `sbx rm --force %s`", cfg.Name)
	return sbx.Reattach(cfg)
}
