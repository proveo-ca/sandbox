// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// DesktopSSHUser is the account Hermes Desktop logs into; it maps to the agent's uid 1000 in the hermes image.
const DesktopSSHUser = "hermes"

// seedGate holds a command until this boot's seed has run, as the agent launch does.
const seedGate = "/usr/local/bin/proveo-await-seed"

// DesktopHermesPath is the hermes Desktop runs in the sandbox; a login shell's PATH does not reach /opt/hermes/bin.
const DesktopHermesPath = "/opt/proveo/shims/hermes"

// DesktopGateway is the SSH gateway Hermes Desktop adds for sandbox name.
func DesktopGateway(name string) string { return DesktopSSHUser + "@" + sbx.SSHHost(name) }

// desktopHold is one Hermes Desktop session driving sandbox name over sbx's SSH.
type desktopHold struct {
	name     string
	launch   []string
	prepare  []string // configures hermes in the sandbox without launching it
	userData string   // proveo-owned Desktop userData; "" leaves the app's own
	setup    func(...string) (string, error)
	exec     func(name, user string, argv []string) (string, error)
	start    func(argv, env []string) error
	attached func(name string) bool
	poll     time.Duration
	deadline time.Duration
}

var proveohomeRoot = func() string { return proveohome.Root(os.Getenv) }

func newDesktopHold(name string, launch, entrypoint []string) desktopHold {
	var prepare []string
	if len(entrypoint) > 0 {
		prepare = append(append([]string{seedGate}, entrypoint...), "--prepare")
	}
	return desktopHold{
		name: name, launch: launch, prepare: prepare,
		userData: hermesdesktop.UserDataDir(proveohomeRoot()),
		setup:    SbxRun, exec: sbxExecAs, start: startDetached, attached: desktopAttached,
		poll: 3 * time.Second, deadline: 90 * time.Second,
	}
}

// run registers the gateway, opens the app on it, watches for its backend in the sandbox, and waits for the stop.
func (h desktopHold) run(ctx context.Context) error {
	if out, err := h.setup(sbx.SetupSSHArgs()...); err != nil {
		return fmt.Errorf("sbx setup ssh: %w: %s", err, strings.TrimSpace(out))
	}
	if len(h.prepare) > 0 {
		if out, err := h.exec(h.name, DesktopSSHUser, h.prepare); err != nil {
			ui.Warnf("desktop: hermes in %s was not configured (%v) — Desktop may offer Nous or a CPU-only local model\n%s",
				h.name, err, lastLines(out, 6))
		} else {
			ui.Okf("desktop: hermes in %s configured (model, durable home, no Nous guest, no in-VM llama.cpp)", h.name)
		}
	}
	var env []string
	if h.userData != "" {
		if err := hermesdesktop.RegisterGateway(h.userData, DesktopSSHUser, sbx.SSHHost(h.name), DesktopHermesPath); err != nil {
			ui.Warnf("desktop: could not register the gateway in %s (%v)", h.userData, err)
		} else {
			env = append(env, hermesdesktop.UserDataEnv+"="+h.userData)
			ui.Hostf("desktop: Hermes Desktop opens on the gateway %s (its data: %s)", DesktopGateway(h.name), h.userData)
		}
	}
	if env == nil {
		ui.Storef("desktop: Hermes Desktop → Settings → Gateways → Add → SSH: %s", DesktopGateway(h.name))
	}
	if len(h.launch) > 0 {
		if err := h.start(h.launch, env); err != nil {
			ui.Warnf("desktop: could not open Hermes Desktop (%v) — open it yourself and pick the gateway %s", err, DesktopGateway(h.name))
		}
	}
	ui.Notef("this terminal holds %s for Hermes Desktop; Ctrl+C ends the run and tears the sandbox down", h.name)
	watched := make(chan struct{})
	if h.attached != nil {
		go func() { h.watch(ctx); close(watched) }()
	} else {
		close(watched)
	}
	<-ctx.Done()
	<-watched
	return nil
}

// watch reports when Desktop's backend starts in the sandbox, and warns once when it has not by the deadline.
func (h desktopHold) watch(ctx context.Context) {
	tick := time.NewTicker(h.poll)
	defer tick.Stop()
	late := time.After(h.deadline)
	warned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-late:
			if !warned {
				warned = true
				ui.Warnf("desktop: no Hermes Desktop session reached %s after %s — a chat on the app's local backend runs on this HOST "+
					"(host files, host tools, Nous's free model); pick the gateway %s in the app", h.name, h.deadline, DesktopGateway(h.name))
			}
		case <-tick.C:
			if h.attached(h.name) {
				ui.Okf("desktop: Hermes Desktop attached to %s", h.name)
				return
			}
		}
	}
}

// desktopServeProbe exits 0 while Desktop's `hermes serve --isolated --host 127.0.0.1` runs in the sandbox.
const desktopServeProbe = `for f in /proc/[0-9]*/cmdline; do tr '\0' ' ' <"$f"; echo; done 2>/dev/null | grep -q '[s]erve --isolated --host 127.0.0.1'`

// sbxExecAs runs argv in sandbox name as user, with the workload's env.
func sbxExecAs(name, user string, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), guestExecDeadline)
	defer cancel()
	out, err := exec.CommandContext(ctx, sbx.Binary, append([]string{"exec", "-u", user, "-w", "/", name, "--"}, argv...)...).CombinedOutput()
	return string(out), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func desktopAttached(name string) bool {
	return exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", desktopServeProbe).Run() == nil
}

// startDetached starts argv with env added, without waiting for it; the app outlives the run.
func startDetached(argv, env []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), env...)
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}
