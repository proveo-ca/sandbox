// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml
package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestTheDesktopGatewayLogsInAsTheAgentOverSbxSSH(t *testing.T) {
	t.Parallel()
	if got := DesktopGateway("proveo-hermes-ab12cd34"); got != "hermes@proveo-hermes-ab12cd34.sbx" {
		t.Errorf("DesktopGateway = %q; `ssh hermes@<name>.sbx` lands as uid 1000 (probe H1)", got)
	}
}

func testHold(t *testing.T, attached func(string) bool) (desktopHold, *[]string, *[]string, *[]string) {
	t.Helper()
	var setup, started, env []string
	return desktopHold{
		name: "proveo-hermes-ab", launch: []string{"/h/.local/bin/hermes", "desktop", "--skip-build"},
		userData: filepath.Join(t.TempDir(), "desktop", "hermes"),
		setup:    func(args ...string) (string, error) { setup = args; return "", nil },
		start:    func(argv, e []string) error { started, env = argv, e; return nil },
		attached: attached, poll: 5 * time.Millisecond, deadline: 40 * time.Millisecond,
	}, &setup, &started, &env
}

func captureUI(t *testing.T) *strings.Builder {
	t.Helper()
	var out strings.Builder
	orig := ui.Default
	t.Cleanup(func() { ui.Default = orig })
	ui.Default = ui.New(&out)
	return &out
}

func TestTheDesktopHoldOpensTheAppOnARegisteredGatewayAndWaitsForTheStop(t *testing.T) {
	h, setup, started, env := testHold(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("the hold returned before the stop: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("a stopped hold = %v, want nil (a stop tears down like a normal exit)", err)
	}
	if !slices.Equal(*setup, []string{"setup", "ssh"}) {
		t.Errorf("setup ran %v, want sbx setup ssh", *setup)
	}
	if len(*started) == 0 || (*started)[1] != "desktop" {
		t.Errorf("the app was not opened: %v", *started)
	}
	if want := hermesdesktop.UserDataEnv + "=" + h.userData; !slices.Contains(*env, want) {
		t.Errorf("the app must open on proveo's userData, not the operator's: env %v", *env)
	}
	b, err := os.ReadFile(filepath.Join(h.userData, "connections.json"))
	if err != nil || !strings.Contains(string(b), `"proveo-hermes-ab.sbx"`) {
		t.Errorf("the gateway was not registered: %v %s", err, b)
	}
}

func TestAFailedSSHSetupEndsTheHoldWithoutOpeningTheApp(t *testing.T) {
	h, _, started, _ := testHold(t, nil)
	h.setup = func(...string) (string, error) { return "not signed in", errors.New("exit 1") }
	if err := h.run(context.Background()); err == nil || len(*started) > 0 {
		t.Errorf("err=%v started=%v; without the *.sbx config the app has nothing to reach", err, *started)
	}
}

func TestTheDesktopHoldWarnsWhenNoSessionReachesTheSandbox(t *testing.T) {
	out := captureUI(t)
	h, _, _, _ := testHold(t, func(string) bool { return false })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = h.run(ctx)
	if got := out.String(); strings.Count(got, "runs on this HOST") != 1 {
		t.Errorf("want one host-backend warning, got:\n%s", got)
	}
}

func TestTheDesktopHoldConfirmsAnAttachedSession(t *testing.T) {
	out := captureUI(t)
	var polls atomic.Int32
	h, _, _, _ := testHold(t, func(string) bool { return polls.Add(1) >= 2 })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = h.run(ctx)
	got := out.String()
	if !strings.Contains(got, "attached to proveo-hermes-ab") || strings.Contains(got, "runs on this HOST") {
		t.Errorf("an attached session must be confirmed and never warned about:\n%s", got)
	}
}
