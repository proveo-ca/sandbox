// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml, _spec/internal/sbx/host-readiness.puml, _spec/cmd/proveo/github-credentials.puml
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

type initOptions struct {
	printOnly  bool
	prefix     string
	skipLogin  bool
	skipSetup  bool
	skipGh     bool
	skipGit    bool
	skipPrompt bool
	force      bool
	yes        bool
}

func initCmd() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install and sign in to the sbx backend proveo runs on (alias for --init)",
		Long: "Detect this host, install the pinned Docker Sandboxes release (v" + sbx.Release +
			"), check every prerequisite a run depends on, sign in, and ready git/gh.\n\n" +
			"`proveo init` again offers what was left for later. `sbx login` runs only while `sbx diagnose` says Docker is\n" +
			"signed out; `sbx setup` opens after the result, only while sbx's store lacks an env key or SSH agent forwarding is off.\n" +
			"Then `proveo run` lists the harnesses this host can start.\n\n" +
			"proveo does not manage the host: fix what sbx diagnose reports, or reinstall sbx with --force.\n" +
			"A PATH line init adds reaches a new shell; `source <rc>` brings it into this one.\n" +
			"The prompt kit shows in a new shell; `proveo uninstall` takes it out again. It seeds starship's\n" +
			"no-nerd-font preset unless the terminal draws Nerd glyphs; `starship preset nerd-font-symbols -o <config>` switches it.",
		Args: noArgs,
		RunE: func(*cobra.Command, []string) error { return doInit(o) },
	}
	cmd.Flags().BoolVar(&o.printOnly, "print", false, "show the plan and the host's verdict without changing anything")
	cmd.Flags().StringVar(&o.prefix, "prefix", "", "where to install sbx (default ~/.docker/sbx)")
	cmd.Flags().BoolVar(&o.skipLogin, "skip-login", false, "install and check, but do not run `sbx login`, "+
		"which otherwise runs only while `sbx diagnose` says Docker is signed out")
	cmd.Flags().BoolVar(&o.skipSetup, "skip-setup", false, "do not open `sbx setup`, which otherwise opens after the result "+
		"only while sbx's store lacks an env key or SSH agent forwarding is off")
	cmd.Flags().BoolVar(&o.skipGh, "skip-gh", false, "do not verify, install, or log in via `gh`")
	cmd.Flags().BoolVar(&o.skipGit, "skip-git", false, "do not check or set git user.name / user.email")
	cmd.Flags().BoolVar(&o.skipPrompt, "skip-prompt", false, "do not install starship, a Nerd Font, or the proveo prompt segment")
	cmd.Flags().BoolVar(&o.force, "force", false, "reinstall even when this host already carries a usable sbx, or one that is broken or half-installed")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "take the defaults without prompting (implied when there is no terminal); "+
		"`sbx login`, `sbx setup`, credentials, gh's install, GitHub sign-in and access, and the operator's name wait for `proveo init` on a terminal; "+
		"a missing git identity waits for `git config --global user.name` and `user.email`")
	return cmd
}

func doInit(o initOptions) error {
	host := sbx.DetectHost()
	prefix := strings.TrimSpace(o.prefix)
	if prefix == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("no home directory to install into, and no --prefix given: %w", err)
		}
		prefix = sbx.DefaultPrefix(home)
	}

	download, err := os.MkdirTemp("", "proveo-sbx-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(download) }()

	plan, err := sbx.PlanFor(host, prefix, download)
	if err != nil {
		return err
	}

	installed, _ := installedVersion(plan)
	checks := sbx.Prereqs(host, sbx.DefaultProbe())
	rep := newInitReport(o, host, checks, plan)
	prompted := !o.yes && !o.printOnly && interactiveTerminal()
	stop := sbx.Blocking(checks, sbx.BlocksInstall)
	if len(stop) > 0 && !o.printOnly && !prompted {
		return rep.close(nil, blockedErr{stop})
	}
	if o.printOnly {
		rep.printSbx()
		printPlan(plan)
		if _, _, err := credentialStage(o, choiceui.Step{}, nil, nil, rep, cardPage{}); err != nil { // reports where each credential rests; provisions nothing
			return err
		}
		if _, err := operatorStage(o, choiceui.Step{}, rep, cardPage{}, choiceui.Row{}); err != nil {
			return err
		}
		rep.printCredentials()
		return nil
	}
	w := &initWalk{o: o, host: host, plan: plan, download: download, installed: installed, prereqs: checks, stop: stop,
		rep: rep, rec: &initRecord{}, prompted: prompted, typed: map[string]string{}}
	if !prompted {
		w.view = streamView{}
		return rep.close(w.rec, runInitSteps(false, w.pages()))
	}
	return w.onCard()
}

// appliedPlan is the plan setup acts on; a package manager owns placement, like the MSI.
func appliedPlan(plan sbx.Plan, viaPackage bool) sbx.Plan {
	if viaPackage {
		plan.Bin, plan.Prefix = "", ""
	}
	return plan
}

// stepResult is how a screen of `proveo init` was left.
type stepResult int

const (
	stepNext stepResult = iota
	stepBack
	stepNone // the screen had nothing to ask
	stepQuit // the operator left the wizard: no later screen runs
)

// errInitLeft is the error a confirmed quit ends init with.
var errInitLeft = errors.New("quit")

// initStep is one screen of `proveo init`.
type initStep func(at choiceui.Step) (stepResult, error)

// runInitSteps walks the screens in order; a step back passes over screens that ask nothing.
// Unprompted, no screen is numbered and none can step back.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml
func runInitSteps(prompted bool, steps []initStep) error {
	back := false
	for i := 0; i < len(steps); {
		at := choiceui.Step{}
		if prompted {
			at = choiceui.Step{At: i + 1, Of: len(steps)}
		}
		r, err := steps[i](at)
		if err != nil {
			return err
		}
		if r == stepQuit {
			return fmt.Errorf("%w at page %d/%d", errInitLeft, i+1, len(steps))
		}
		if (r == stepBack || (r == stepNone && back)) && prompted && i > 0 {
			i, back = i-1, true
			continue
		}
		i, back = i+1, false
	}
	return nil
}

func describeHost(h sbx.Host) string {
	out := h.OS + "/" + h.Arch
	if h.Distro != "" {
		out += " · " + h.Distro
		if h.Version != "" {
			out += " " + h.Version
		}
	}
	return out
}

func installedVersion(plan sbx.Plan) (version, why string) {
	if plan.Bin != "" {
		if _, err := os.Stat(plan.Bin); err == nil {
			got, err := sbx.VersionAt(plan.Bin)
			if err != nil {
				return "", fmt.Sprintf("%s exists but its version is unreadable (%v)", plan.Bin, err)
			}
			return got, ""
		}
	}
	if !sbx.Installed() {
		return "", sbx.Binary + " is not on PATH"
	}
	got, err := sbx.Version()
	if err != nil {
		return "", fmt.Sprintf("%s is on PATH but its version is unreadable (%v)", sbx.Binary, err)
	}
	return got, ""
}

// printPlan is --print's plan: the release and its install steps under "sbx", then what init asks
// of gh and git under "git". Every path stays absolute so each line can be copied.
func printPlan(plan sbx.Plan) {
	ui.Section(ui.SectionSbx)
	ui.Storef("sbx %s — %s", sbx.Release, plan.Asset)
	if plan.Prefix != "" {
		ui.Notef("prefix: %s", plan.Prefix)
	}
	if plan.Note != "" {
		ui.Notef("%s", plan.Note)
	}
	if plan.Alt != "" {
		ui.Notef("from your package manager instead, if you would rather it owned this: %s", plan.Alt)
	}
	ui.Hostf("download %s", plan.URL)
	if plan.Provenance != nil {
		ui.Notef("verify sha256 against %s, subject %s", plan.Provenance.Asset, plan.Provenance.Subject)
	} else {
		ui.Notef("no published digest covers this asset; the download's sha256 is reported, not verified")
	}
	for _, s := range plan.Steps {
		ui.Appf("%s: %s", s.What, strings.Join(s.Argv, " "))
	}
	if plan.Bin != "" {
		ui.Notef("then %s must exist", plan.Bin)
	}
	if plan.Packaged != nil {
		ui.Notef("or, at the prompt: %s (%s)", strings.Join(plan.Packaged.Argv, " "), plan.Packaged.Asset)
	}
	ui.Appf("%s %s — only while sbx diagnose says Docker is signed out", sbx.Binary, strings.Join(sbx.LoginArgs(), " "))
	ui.Appf("%s %s — after init, only while sbx's store lacks an env key", sbx.Binary, strings.Join(sbx.SetupArgs(), " "))
	ui.Appf("%s %s", sbx.Binary, strings.Join(sbx.VersionJSONArgs(), " "))
	ui.Section(ui.SectionGit)
	ui.Appf("gh auth status")
	ui.Appf("gh auth login")
	ui.Appf("%s %s", sbx.Binary, strings.Join(sbx.SecretSetArgs("github"), " "))
	ui.Appf("git config --get user.name")
	ui.Appf("git config --get user.email")
}

func interactiveTerminal() bool {
	return agentio.IsReaderTTY(os.Stdin) && agentio.IsWriterTTY(os.Stderr)
}

func prefixWritable(prefix string) error {
	if prefix == "" {
		return nil // the installer owns placement (the MSI)
	}
	dir := filepath.Clean(prefix)
	for {
		if fi, err := os.Stat(dir); err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", dir)
			}
			f, err := os.CreateTemp(dir, ".proveo-write-probe-*")
			if err != nil {
				return fmt.Errorf("%s is not writable by this user — re-run with `sudo -E` "+
					"or choose a prefix you own (the default, %s, needs no root)",
					dir, sbx.DefaultPrefix(homeDir()))
			}
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil // walked to the root and found nothing; let the step speak
		}
		dir = parent
	}
}

// fetch downloads url to dest and returns the hex sha256 of what arrived; ctx ends it early.
func fetch(ctx context.Context, url, dest string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %s", url, resp.Status)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		_ = os.Remove(dest)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func publishedDigest(ctx context.Context, p sbx.Provenance) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", p.Asset, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %s", p.Asset, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return sbx.DigestFromProvenance(body, p.Subject)
}

func resolveBin(plan sbx.Plan) string {
	if plan.Bin != "" {
		if _, err := os.Stat(plan.Bin); err == nil {
			return plan.Bin
		}
	}
	return sbx.Binary
}

// sbxPathDir is the prefix's bin directory once it holds sbx; "" otherwise.
func sbxPathDir(plan sbx.Plan) string {
	if plan.Bin == "" || resolveBin(plan) != plan.Bin {
		return ""
	}
	return filepath.Dir(plan.Bin)
}

// settle records on d what its applied steps settled, for a revisit to carry.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (REVISIT)
func settle(d initDecisions, steps []applyStep, applied sbx.Plan) initDecisions {
	if slices.ContainsFunc(steps, func(s applyStep) bool { return s.label == rowPath }) {
		d.pathDone = filepath.Dir(applied.Bin)
	}
	return d
}

// rcAdds reports whether the shell's rc file already names dir.
func rcAdds(dir string) bool {
	sh, ok := shell.Detect(os.Getenv("SHELL"))
	if !ok {
		return false
	}
	home, _ := os.UserHomeDir()
	content, _ := os.ReadFile(sh.RCFile(runtime.GOOS, home))
	return shell.AlreadyConfigured(string(content), dir)
}

// ensureSbxOnPath puts dir on PATH in the shell's rc when write allows it, and returns the item
// that says what changed or what is left to do; a zero Item when dir is on PATH already.
func ensureSbxOnPath(dir string, write bool) sandbox.Item {
	if onPath(dir) {
		return sandbox.Item{}
	}
	sh, ok := shell.Detect(os.Getenv("SHELL"))
	home, _ := os.UserHomeDir()
	rc := sh.RCFile(runtime.GOOS, home)
	line := sh.PathLine(dir)
	if !write || !ok || !sh.Supported {
		return sandbox.Item{Warn: true, Text: ui.Tilde(dir) + " — not on PATH", Detail: []string{"add to " + ui.Tilde(rc) + ": " + line}}
	}
	notWritten := func(err error) sandbox.Item {
		return sandbox.Item{Warn: true, Text: ui.Tilde(rc) + " — not written", Detail: append(errLines(err), "add it yourself: "+line)}
	}
	content, _ := os.ReadFile(rc) // a missing rc is fine; it gets created
	if shell.AlreadyConfigured(string(content), dir) {
		return sandbox.Item{Warn: true, Text: ui.Tilde(dir) + " — not on this shell's PATH", Detail: []string{ui.Tilde(rc) + " adds it; a new shell picks it up"}}
	}
	if err := appendRC(rc, sh.SbxBlock(dir)); err != nil {
		return notWritten(err)
	}
	return sandbox.Item{Ok: true, Text: ui.Tilde(dir) + " — added to PATH in " + ui.Tilde(rc), Detail: []string{"a new shell picks it up"}}
}

// verify asks sbx for its verdict on this host and keeps it as the result's readiness: the failing
// checks as items, and whether a run can start. A failing check a run needs is its error.
func verify(bin string, rep *initReport) error {
	if bin == sbx.Binary && !sbx.Installed() {
		return verifyWith(bin, rep, nil, nil)
	}
	checks, err := sbx.DiagnoseAt(bin)
	rep.heard(checks, err)
	return verifyWith(bin, rep, checks, err)
}

// verifyWith is verify on a diagnose that already ran: checks and err are what sbx said.
func verifyWith(bin string, rep *initReport, checks []sbx.Check, err error) error {
	rep.readiness, rep.ready, rep.judged = nil, false, true
	if bin == sbx.Binary && !sbx.Installed() {
		rep.readiness = []sandbox.Item{{Warn: true, Text: "sbx — not on this shell's PATH",
			Detail: []string{"a new shell finds it; `" + sbx.Binary + " diagnose` there gives the host's verdict"}}}
		return nil
	}
	if _, err := sbx.VersionAt(bin); err != nil {
		rep.readiness = append(rep.readiness, sandbox.Item{Warn: true, Text: "sbx — reports no version", Detail: errLines(err)})
	}
	if err != nil {
		first, _, _ := strings.Cut(err.Error(), "\n")
		rep.readiness = append(rep.readiness, sandbox.Item{Warn: true, Text: "sbx diagnose — gave no answer",
			Detail: []string{ui.TildeIn(first), "`proveo init --force` reinstalls the pinned " + sbx.Release}})
		return nil
	}
	items, blockers := checkItems(checks)
	rep.readiness = append(rep.readiness, items...)
	if len(blockers) > 0 {
		return hostNotReady{names: sbx.CheckNames(blockers)}
	}
	rep.ready = true
	return nil
}

// outsideARunNote follows a failing row a run does not need.
const outsideARunNote = "a run does not need this check; it affects %s"

// loginFix is how a host signs in to Docker: on a terminal, or with a token in CI.
const loginFix = "`sbx login` on a terminal; CI: echo \"$DOCKER_PAT\" | sbx login --username <name> --password-stdin"

// checkItems are sbx's failing rows as items, × for a check a run needs and ⚠ for one it does not,
// each with sbx's evidence and hint dim; blockers are the rows a run needs. A signed-out
// Authentication row reads as the Docker login item.
func checkItems(checks []sbx.Check) (items []sandbox.Item, blockers []sbx.Check) {
	for _, c := range checks {
		if c.Name == sbx.AuthenticationCheck {
			if sbx.DockerAuth([]sbx.Check{c}, nil) == sbx.AuthOut {
				items = append(items, loginItem(c))
			}
			continue
		}
		if !c.Failed() {
			continue
		}
		affects := c.Affects()
		it := sandbox.Item{Fail: affects == "", Warn: affects != "", Text: c.Name + " — " + c.Summary(), Detail: slices.Clone(c.Evidence())}
		switch {
		case c.Hint != "":
			it.Detail = append(it.Detail, c.Hint)
		case affects == "":
			it.Detail = append(it.Detail, "`proveo init --force` reinstalls the pinned "+sbx.Release)
		}
		if affects != "" {
			it.Detail = append(it.Detail, fmt.Sprintf(outsideARunNote, affects))
		}
		items = append(items, it)
	}
	return sandbox.WithPaths(items, ui.TildeIn), sbx.RunBlockers(checks)
}

// loginItem is a signed-out Authentication row: its evidence, the way to sign in, and why a run goes on.
func loginItem(c sbx.Check) sandbox.Item {
	detail := append(slices.Clone(c.Evidence()), loginFix)
	if affects := c.Affects(); affects != "" {
		detail = append(detail, fmt.Sprintf(outsideARunNote, affects))
	}
	return sandbox.Item{Warn: true, Text: rowLogin + " — not signed in", Detail: detail}
}
