// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

type initOptions struct {
	printOnly bool
	prefix    string
	skipLogin bool
	skipSetup bool
	force     bool
	yes       bool
}

func initCmd() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install and sign in to the sbx backend proveo runs on (alias for --init)",
		Long: "Detect this host, install the pinned Docker Sandboxes release (v" + sbx.Release +
			"), check every prerequisite a run depends on, and sign in.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return doInit(o) },
	}
	cmd.Flags().BoolVar(&o.printOnly, "print", false, "show the plan and the host's verdict without changing anything")
	cmd.Flags().StringVar(&o.prefix, "prefix", "", "where to install sbx (default ~/.docker/sbx)")
	cmd.Flags().BoolVar(&o.skipLogin, "skip-login", false, "install and check, but do not run `sbx login`")
	cmd.Flags().BoolVar(&o.skipSetup, "skip-setup", false, "do not run `sbx setup`, the first-run wizard")
	cmd.Flags().BoolVar(&o.force, "force", false, "reinstall even when this host already carries a usable sbx")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "take the defaults without prompting (implied when there is no terminal)")
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

	ui.Section(ui.SectionRun)
	ui.Hostf("host: %s", describeHost(host))
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

	checks := reportPrereqs(host)
	if stop := sbx.Blocking(checks, sbx.BlocksInstall); len(stop) > 0 && !o.printOnly {
		return fmt.Errorf("nothing good follows an install here: %s", strings.Join(sbx.Names(stop), ", "))
	}

	installed, why := installedVersion(plan)
	if o.printOnly {
		printPlan(plan, installed)
		return nil
	}

	d, err := decide(plan, host, installed, checks, o)
	if err != nil {
		return err
	}

	// A prefix chosen at the prompt has to reach the plan, which was built
	// around the default one.
	if d.Prefix != plan.Prefix {
		if plan, err = sbx.PlanFor(host, d.Prefix, download); err != nil {
			return err
		}
		installed, why = installedVersion(plan)
	}

	switch d.Install {
	case installSkip:
		ui.Section(ui.SectionStarting)
		ui.Okf("sbx %s stays as it is (`--force` reinstalls)", installed)
	case installPackaged:
		if plan.Packaged == nil {
			return fmt.Errorf("this release ships no distro package for %s", describeHost(host))
		}
		ui.Section(ui.SectionStarting)
		ui.Appf("installing the distro package — it will ask for your password")
		if err := installPackage(*plan.Packaged, download); err != nil {
			return err
		}
		plan.Bin, plan.Prefix = "", "" // the package manager owns placement, like the MSI
	default:
		ui.Section(ui.SectionStarting)
		if installed != "" {
			ui.Appf("replacing sbx %s with %s", installed, sbx.Release)
		} else if why != "" {
			ui.Appf("installing sbx: %s", why)
		}
		if err := install(plan, download); err != nil {
			return err
		}
	}

	bin := resolveBin(plan)
	if err := finish(bin, plan, d); err != nil {
		return err
	}

	if stop := sbx.Blocking(checks, sbx.BlocksRun); len(stop) > 0 {
		ui.Section(ui.SectionResults)
		ui.Warnf("sbx is installed, but this host cannot run a sandbox yet")
		for _, c := range stop {
			ui.Notef("%s: %s", c.Name, c.Fix)
		}
		return fmt.Errorf("host not ready to run: %s", strings.Join(sbx.Names(stop), ", "))
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

func reportPrereqs(h sbx.Host) []sbx.Prereq {
	checks := sbx.Prereqs(h, sbx.DefaultProbe())
	if len(checks) == 0 {
		return nil
	}
	ui.Section(ui.SectionExecution)
	for _, c := range checks {
		switch {
		case c.OK:
			ui.Okf("%s — %s", c.Name, c.Detail)
		case c.Blocks == sbx.BlocksInstall:
			ui.Failf("%s — %s", c.Name, c.Detail)
			ui.Notef("fix: %s", c.Fix)
		default:
			ui.Warnf("%s — %s", c.Name, c.Detail)
			ui.Notef("%s", c.Fix)
		}
	}
	return checks
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

func printPlan(plan sbx.Plan, installed string) {
	ui.Section(ui.SectionStarting)
	if installed != "" {
		ui.Notef("this host carries sbx %s", installed)
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
	ui.Appf("%s %s", sbx.Binary, strings.Join(sbx.LoginArgs(), " "))
	ui.Appf("%s %s", sbx.Binary, strings.Join(sbx.SetupArgs(), " "))
	ui.Appf("%s %s", sbx.Binary, strings.Join(sbx.VersionJSONArgs(), " "))
	if plan.Packaged != nil {
		ui.Notef("or, at the prompt: %s (%s)", strings.Join(plan.Packaged.Argv, " "), plan.Packaged.Asset)
	}
}

func decide(plan sbx.Plan, host sbx.Host, installed string, checks []sbx.Prereq, o initOptions) (initDecisions, error) {
	if o.yes || !interactiveTerminal() {
		d := defaultDecisions(plan, installed, o)
		ui.Section(ui.SectionStarting)
		ui.Notef("no prompt (%s) — %s", promptlessBecause(o), describeDecisions(d))
		return d, nil
	}
	return askDecisions(plan, host, installed, checks, o)
}

func promptlessBecause(o initOptions) string {
	if o.yes {
		return "--yes"
	}
	return "no terminal to ask through"
}

func interactiveTerminal() bool {
	return agentio.IsReaderTTY(os.Stdin) && agentio.IsWriterTTY(os.Stderr)
}

func installPackage(p sbx.Packaged, download string) error {
	dest := filepath.Join(download, p.Asset)
	ui.Appf("downloading %s", p.Asset)
	sum, err := fetch(p.URL, dest)
	if err != nil {
		return err
	}
	ui.Notef("sha256 %s (no published digest covers this asset)", sum)

	c := exec.Command(p.Argv[0], p.Argv[1:]...)
	c.Dir = download
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(p.Argv, " "), err)
	}
	return nil
}

func install(plan sbx.Plan, download string) error {
	if err := prefixWritable(plan.Prefix); err != nil {
		return err
	}
	dest := filepath.Join(download, plan.Asset)
	ui.Appf("downloading %s", plan.Asset)
	sum, err := fetch(plan.URL, dest)
	if err != nil {
		return err
	}

	if plan.Provenance != nil {
		want, err := publishedDigest(*plan.Provenance)
		if err != nil {
			return err
		}
		if sum != want {
			return fmt.Errorf("%s does not match the digest published for %s: got %s, want %s",
				plan.Asset, plan.Provenance.Subject, sum, want)
		}
		ui.Okf("sha256 %s — matches the release's provenance", sum)
	} else {
		ui.Notef("sha256 %s (no published digest covers this asset)", sum)
	}

	for _, s := range plan.Steps {
		ui.Appf("%s", s.What)
		c := exec.Command(s.Argv[0], s.Argv[1:]...)
		c.Dir = s.Dir
		if c.Dir == "" {
			c.Dir = download
		}
		c.Env = append(os.Environ(), s.Env...)
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s (%s): %w", s.What, strings.Join(s.Argv, " "), err)
		}
	}

	if plan.Bin != "" {
		if _, err := os.Stat(plan.Bin); err != nil {
			return fmt.Errorf("the installer reported success but %s is not there: %w", plan.Bin, err)
		}
		ui.Okf("installed %s", plan.Bin)
	}
	return nil
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

// fetch downloads url to dest and returns the hex sha256 of what arrived.
func fetch(url, dest string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Minute}
	resp, err := client.Get(url)
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

func publishedDigest(p sbx.Provenance) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(p.URL)
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

func finish(bin string, plan sbx.Plan, d initDecisions) error {
	if plan.Bin != "" {
		ensureSbxOnPath(filepath.Dir(plan.Bin), d.AddPath)
	}

	if d.Login {
		ui.Section(ui.SectionCredentials)
		ui.Hostf("signing in to Docker — `%s login` owns this, and proveo never sees the credential", sbx.Binary)
		if err := interactive(bin, sbx.LoginArgs()); err != nil {
			return fmt.Errorf("sbx login: %w", err)
		}
	}

	if d.Wizard {
		ui.Section(ui.SectionStarting)
		ui.Appf("running the first-run wizard (`%s setup`) — 0.42 stopped opening it on its own", sbx.Binary)
		if err := interactive(bin, sbx.SetupArgs()); err != nil {
			ui.Warnf("`%s setup` did not complete (%v) — run it yourself if a sandbox misbehaves", sbx.Binary, err)
		}
	}

	if d.Baseline != "" {
		ui.Section(ui.SectionEgress)
		ui.Appf("binding the host-wide network baseline: `%s %s`", sbx.Binary,
			strings.Join(sbx.PolicyInitArgs(d.Baseline), " "))
		if err := interactive(bin, sbx.PolicyInitArgs(d.Baseline)); err != nil {
			ui.Warnf("the baseline was not changed (%v) — `%s %s` by hand", err, sbx.Binary,
				strings.Join(sbx.PolicyInitArgs(d.Baseline), " "))
		}
	}

	return verify(bin)
}

func ensureSbxOnPath(dir string, write bool) {
	if onPath(dir) {
		return
	}
	sh, ok := shell.Detect(os.Getenv("SHELL"))
	home, _ := os.UserHomeDir()
	rc := sh.RCFile(runtime.GOOS, home)
	line := sh.PathLine(dir)

	ui.Section(ui.SectionWorkspace)
	if !write || !ok || !sh.Supported {
		ui.Warnf("%s is not on your PATH, so `sbx` will not resolve in a new shell", dir)
		ui.Notef("add it to %s: %s", rc, line)
		return
	}
	content, _ := os.ReadFile(rc) // a missing rc is fine; it gets created
	if strings.Contains(string(content), dir) {
		ui.Okf("%s already configures %s — restart your shell", rc, dir)
		return
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		ui.Warnf("could not prepare %s (%v) — add it yourself: %s", rc, err, line)
		return
	}
	f, err := os.OpenFile(rc, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		ui.Warnf("could not write %s (%v) — add it yourself: %s", rc, err, line)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(sh.SbxBlock(dir)); err != nil {
		ui.Warnf("could not write %s (%v) — add it yourself: %s", rc, err, line)
		return
	}
	ui.Okf("added %s to PATH in %s — restart your shell or run: source %s", dir, rc, rc)
}

func verify(bin string) error {
	ui.Section(ui.SectionResults)

	if got, err := sbx.VersionAt(bin); err == nil {
		if bin == sbx.Binary {
			ui.Storef("sbx %s on PATH", got)
		} else {
			ui.Storef("sbx %s at %s — not on PATH yet; the export line above fixes a new shell", got, bin)
		}
	} else {
		ui.Warnf("installed sbx does not report a version (%v)", err)
	}

	state, err := serverState(bin)
	switch {
	case err != nil:
		ui.Warnf("could not read the daemon's state (%v) — `%s %s`", err, sbx.Binary,
			strings.Join(sbx.VersionJSONArgs(), " "))
	case state == "running":
		ui.Okf("the sandbox daemon is running — `proveo run <agent>` has a backend")
	default:
		ui.Warnf("the sandbox daemon reports %q; a run would create a sandbox that dies with no output", state)
		ui.Notef("start it by running any sbx command interactively, then re-check with `%s %s`",
			sbx.Binary, strings.Join(sbx.VersionJSONArgs(), " "))
	}

	if allowed, known := sbx.NetworkAllowed("proveo-egress-probe.invalid"); known && allowed {
		ui.Notef("sbx's global network policy currently allows every host, so a run's allowlist adds reach rather than limiting it")
		ui.Notef("bind it once, host-wide: `sbx policy init deny-all` (or `balanced`), then `sbx policy ls`")
	}
	return nil
}

// serverState asks the binary this run installed, falling back to the package's
// PATH-based reader when the two are the same.
func serverState(bin string) (string, error) {
	if bin == sbx.Binary {
		return sbx.ServerState()
	}
	out, err := exec.Command(bin, sbx.VersionJSONArgs()...).Output()
	if err != nil {
		return "", err
	}
	return sbx.ParseServerState(out)
}

func interactive(bin string, args []string) error {
	c := exec.Command(bin, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
	return c.Run()
}
