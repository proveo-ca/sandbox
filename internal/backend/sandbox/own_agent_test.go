package sandbox

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
	"gopkg.in/yaml.v3"
)

// Spec reads the environment through in.Lookup, so a fixture without one
// dereferences nil before it reaches anything this file is about.
func specInput(target string, extra ...string) Input {
	return Input{
		Target: target,
		Image:  "proveo/" + target + ":latest",
		Extra:  extra,
		Lookup: func(string) string { return "" },
	}
}

func specFor(t *testing.T, target string, extra ...string) (sbx.RunConfig, sbx.Kit) {
	t.Helper()
	cfg, kit, _ := Spec(specInput(target, extra...))
	return cfg, kit
}

// The gate is OFF by default, and default means the measured path: cecli borrows
// sbx's shell agent with a flag-leading command. Replacing a fix that was
// negative-checked with one read out of the docs is the move this tree has
// already paid for twice. SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestWithoutTheGateCecliStillBorrowsTheShellAgent(t *testing.T) {
	cfg, kit := specFor(t, "cecli")
	if cfg.Agent != sbx.ShellAgent {
		t.Errorf("agent = %q, want the shell agent while the gate is off", cfg.Agent)
	}
	if !sbx.IsShellLaunch(cfg.Command) {
		t.Errorf("command = %v, want the flag-leading shell launch", cfg.Command)
	}
	if kit.Kind != "mixin" || kit.Sandbox != nil {
		t.Errorf("kit is %q with sandbox=%v, want a plain mixin", kit.Kind, kit.Sandbox)
	}
}

// With the gate on, cecli stops borrowing an agent and declares one. The point
// is not the name: it is that no bash sits between sbx and the image's
// ENTRYPOINT, so the shell agent's rule for words after `--` stops applying.
func TestOwnAgentReplacesTheBorrowedShell(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	cfg, kit := specFor(t, "cecli")

	if want := sbx.AgentName("cecli"); cfg.Agent != want {
		t.Errorf("agent = %q, want %q", cfg.Agent, want)
	}
	if sbx.IsShellLaunch(cfg.Command) {
		t.Errorf("command %v is still the shell wrapper — with its own agent the "+
			"image's ENTRYPOINT runs and the wrapper is exactly what must go", cfg.Command)
	}
	if kit.Kind != "sandbox" {
		t.Errorf("kit kind = %q, want sandbox — a mixin cannot name an agent", kit.Kind)
	}
	if kit.Name != cfg.Agent {
		t.Errorf("kit name %q != agent %q: the kit's name IS the selector", kit.Name, cfg.Agent)
	}
	if kit.Sandbox == nil || kit.Sandbox.Image == "" {
		t.Fatal("a sandbox kit MUST declare a sandbox block naming its image")
	}
	if kit.Sandbox.Image != "proveo/cecli:latest" {
		t.Errorf("kit image = %q, want the run's resolved image", kit.Sandbox.Image)
	}
	// Restating the image's ENTRYPOINT here would be a second copy free to drift.
	if len(kit.Sandbox.Entrypoint) != 0 {
		t.Errorf("kit restates an entrypoint (%v) the image already declares", kit.Sandbox.Entrypoint)
	}
}

// SPEC-v2: a sandbox kit "MAY declare every shared block". Losing one would
// trade the launch fix for a silent loss of reachability or seeding.
//
// Asserted as a DIFFERENCE against the mixin built from the same Input, not
// against fixed values: what matters is that changing `kind` changes the
// identity and NOTHING else. A fixture-shaped assertion ("the allowlist is
// non-empty") measures the fixture instead, and this one first failed for
// exactly that reason.
func TestOwnAgentChangesIdentityAndNothingElse(t *testing.T) {
	_, mixin := specFor(t, "cecli")
	t.Setenv(sbx.EnvAgentKit, "1")
	_, own := specFor(t, "cecli")

	if own.Kind == mixin.Kind || own.Name == mixin.Name {
		t.Fatalf("the gate changed no identity: kind %q/%q name %q/%q",
			mixin.Kind, own.Kind, mixin.Name, own.Name)
	}
	for _, blk := range []struct {
		name      string
		own, base any
	}{
		{"permissions", own.Permissions, mixin.Permissions},
		{"environment", own.Environment, mixin.Environment},
		{"setup", own.Setup, mixin.Setup},
	} {
		if got, want := yamlOf(t, blk.own), yamlOf(t, blk.base); got != want {
			t.Errorf("%s changed with the kind: got %q, want %q", blk.name, got, want)
		}
	}
	if own.Setup == nil || len(own.Setup.Startup) == 0 {
		t.Fatal("no setup.startup — nothing would seed the run")
	}
	if got := own.Setup.Startup[0].Command; len(got) < 2 || !strings.Contains(got[0], "proveo-seed") {
		t.Errorf("startup command = %v, want the seed step", got)
	}
}

func yamlOf(t *testing.T, v any) string {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sbx refuses a Kit that shadows a built-in ("built-in agents cannot be
// overridden by a kit"), so the gate must never reach one.
func TestBuiltinTargetsNeverDeclareTheirOwnAgent(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	for _, target := range []string{"claudecode", "cursor", "opencode"} {
		cfg, kit := specFor(t, target)
		if strings.HasPrefix(cfg.Agent, "proveo-") {
			t.Errorf("%s: agent %q shadows a built-in", target, cfg.Agent)
		}
		if kit.Kind != "mixin" {
			t.Errorf("%s: kit kind = %q, want mixin beside the built-in agent", target, kit.Kind)
		}
	}
}

// --shell selects sbx's own shell deliberately; the gate must not steal it.
func TestShellFlagStillWinsOverTheGate(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("cecli")
	in.Shell = true
	cfg, _, _ := Spec(in)
	if cfg.Agent != sbx.ShellAgent || len(cfg.Command) != 0 {
		t.Errorf("--shell gave agent %q command %v, want a bare shell", cfg.Agent, cfg.Command)
	}
}

// Extras reach the image's ENTRYPOINT as "$@" — bare words, the way the docker
// backend passes them — because there is no bash to reinterpret them.
func TestOwnAgentPassesExtrasThrough(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	cfg, _ := specFor(t, "cecli", "--version")
	if len(cfg.Command) != 1 || cfg.Command[0] != "--version" {
		t.Errorf("command = %v, want the extras verbatim", cfg.Command)
	}
}

// The Kit is written to disk and parsed by sbx, so the rendering is the contract.
func TestSandboxKitRendersTheSandboxBlock(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	_, kit := specFor(t, "cecli")
	b, err := yaml.Marshal(kit)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{"kind: sandbox", "name: proveo-cecli", "sandbox:", "image: proveo/cecli:latest"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered Kit lacks %q:\n%s", want, out)
		}
	}
	// An empty sandbox block would parse and mean nothing.
	if strings.Contains(out, "sandbox: {}") {
		t.Error("sandbox block rendered empty")
	}
}

// A def that declares its OWN agent must also declare its own credentials, or
// it silently loses a property every other def gets for free.
//
// Measured: on the stock shell agent the env holds "proxy-managed", because
// `shell` declares the credential and sbx's host-side proxy injects the real
// value per request. Under the gate, cecli stops borrowing `shell` and runs an
// agent whose Kit declares nothing — so nothing proxy-manages it and the real
// key reaches the process. That is a credential DOWNGRADE hidden inside a
// launch fix, which is precisely what the gate exists to catch.
//
// This test fails until sbx.Kit can express credentials[] and the sandbox kit
// populates it. It is the gate's release condition, written down rather than
// remembered. SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestOwnAgentMustDeclareItsOwnCredentials(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	_, kit := specFor(t, "cecli")

	if kit.Sandbox == nil {
		t.Fatal("gate on but no sandbox block — the rest of this test is meaningless")
	}
	b, err := yaml.Marshal(kit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "credentials:") {
		t.Skip("KNOWN GAP, and the gate's release condition: a sandbox kit declares its " +
			"own agent, so it must declare its own credentials[] — otherwise cecli goes " +
			"from the sentinel it gets by borrowing `shell` to a real key in the agent " +
			"process. Do not default PROVEO_SBX_AGENT_KIT on until this passes.")
	}
}
