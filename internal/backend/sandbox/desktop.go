// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// DesktopSSHUser is the account Hermes Desktop logs into; it maps to the agent's uid 1000 in the hermes image.
const DesktopSSHUser = "hermes"

// DesktopGateway is the SSH gateway Hermes Desktop adds for sandbox name.
func DesktopGateway(name string) string { return DesktopSSHUser + "@" + sbx.SSHHost(name) }

// holdForDesktop hands sandbox name to Hermes Desktop over sbx's SSH, then waits until the run is stopped.
func holdForDesktop(ctx context.Context, name string, launch []string, setup func(...string) (string, error), start func([]string) error) error {
	if out, err := setup(sbx.SetupSSHArgs()...); err != nil {
		return fmt.Errorf("sbx setup ssh: %w: %s", err, strings.TrimSpace(out))
	}
	ui.Storef("desktop: Hermes Desktop → Settings → Gateways → Add → SSH: %s", DesktopGateway(name))
	if len(launch) > 0 {
		if err := start(launch); err != nil {
			ui.Warnf("desktop: could not open Hermes Desktop (%v) — open it yourself and add the gateway above", err)
		}
	}
	ui.Notef("this terminal holds %s for Hermes Desktop; Ctrl+C ends the run and tears the sandbox down", name)
	<-ctx.Done()
	return nil
}

// startDetached starts argv without waiting for it; the app outlives the run.
func startDetached(argv []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}
