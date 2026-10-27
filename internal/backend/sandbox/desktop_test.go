// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package sandbox

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestTheDesktopGatewayLogsInAsTheAgentOverSbxSSH(t *testing.T) {
	t.Parallel()
	if got := DesktopGateway("proveo-hermes-ab12cd34"); got != "hermes@proveo-hermes-ab12cd34.sbx" {
		t.Errorf("DesktopGateway = %q; `ssh hermes@<name>.sbx` lands as uid 1000 (probe H1)", got)
	}
}

func TestTheDesktopHoldSetsUpSSHOpensTheAppAndWaitsForTheStop(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var setup, started []string
	done := make(chan error, 1)
	go func() {
		done <- holdForDesktop(ctx, "proveo-hermes-ab", []string{"/h/.local/bin/hermes", "desktop", "--skip-build"},
			func(args ...string) (string, error) { setup = args; return "", nil },
			func(argv []string) error { started = argv; return nil })
	}()
	select {
	case err := <-done:
		t.Fatalf("the hold returned before the stop: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("a stopped hold = %v, want nil (a stop tears down like a normal exit)", err)
	}
	if !slices.Equal(setup, []string{"setup", "ssh"}) {
		t.Errorf("setup ran %v, want sbx setup ssh", setup)
	}
	if len(started) == 0 || started[1] != "desktop" {
		t.Errorf("the app was not opened: %v", started)
	}
}

func TestAFailedSSHSetupEndsTheHoldWithoutOpeningTheApp(t *testing.T) {
	t.Parallel()
	opened := false
	err := holdForDesktop(context.Background(), "proveo-hermes-ab", []string{"hermes"},
		func(...string) (string, error) { return "not signed in", errors.New("exit 1") },
		func([]string) error { opened = true; return nil })
	if err == nil || opened {
		t.Errorf("err=%v opened=%v; without the *.sbx config the app has nothing to reach", err, opened)
	}
}
