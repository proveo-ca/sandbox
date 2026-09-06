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
// `shell` declares the credential and sbx injects host-side per request. An
// agent that declares nothing gets no such treatment and the real key lands in
// the process — a credential downgrade hidden inside a launch fix. This was the
// gate's release condition; it now asserts rather than skips.
// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestOwnAgentDeclaresItsOwnCredentials(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("cecli")
	in.Detected = []string{"anthropic"}
	in.Lookup = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-real-key"
		}
		return ""
	}
	_, kit, secrets := Spec(in)

	if len(kit.Credentials) == 0 {
		t.Fatal("sandbox kit declares no credentials — the agent would hold the real key " +
			"where borrowing `shell` gave it a sentinel")
	}
	var c *sbx.KitCredential
	for i := range kit.Credentials {
		if kit.Credentials[i].Service == "anthropic" {
			c = &kit.Credentials[i]
		}
	}
	if c == nil {
		t.Fatalf("no credential for service anthropic: %+v", kit.Credentials)
	}
	if c.APIKey == nil || !c.APIKey.ProxyManaged {
		t.Fatal("proxyManaged is the entire point: without it the env var holds the credential")
	}
	if c.APIKey.Name != "ANTHROPIC_API_KEY" {
		t.Errorf("apiKey.name = %q, want the env var the agent reads", c.APIKey.Name)
	}
	if len(c.APIKey.Inject) == 0 {
		t.Fatal("a sentinel with no inject target attaches the value nowhere")
	}
	for _, inj := range c.APIKey.Inject {
		if inj.Header == "" || inj.Domain == "" {
			t.Errorf("incomplete inject: %+v", inj)
		}
	}

	// sbx resolves credentials[].service against a secret stored under the
	// SERVICE name; proveo has always stored under the env-var name.
	var haveService bool
	for _, kv := range secrets {
		if kv[0] == "anthropic" {
			haveService = true
		}
	}
	if !haveService {
		t.Error("no secret stored under the service name `anthropic`, so the declaration " +
			"resolves against nothing")
	}
}

// SPEC-v2: every injected domain MUST also appear in permissions.network.allow.
//
// The fixture deliberately DIVERGES the two inputs: in.Detected is empty, so
// credentials.ReachableHosts contributes nothing to the allowlist, while
// in.Lookup still carries the key, so provider.Detect finds anthropic and the
// credential is built. That is the case the widening exists for, and without it
// this test fails.
//
// The first version set in.Detected too, which put the domain in allow by the
// other path — it passed with the widening deleted, measuring its own fixture
// rather than the contract.
func TestInjectDomainsAreAllowlisted(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("cecli")
	in.Detected = nil
	in.Lookup = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-real-key"
		}
		return ""
	}
	_, kit, _ := Spec(in)

	if len(kit.Credentials) == 0 {
		t.Fatal("no credential built from the lookup alone — the fixture no longer " +
			"diverges the two inputs and the test would prove nothing")
	}

	allowed := map[string]bool{}
	for _, a := range kit.Permissions.Network.Allow {
		allowed[a] = true
	}
	for _, c := range kit.Credentials {
		if c.APIKey == nil {
			continue
		}
		for _, inj := range c.APIKey.Inject {
			if !allowed[inj.Domain] {
				t.Errorf("credential %q injects into %q, which is not in network.allow — "+
					"sbx refuses that", c.Service, inj.Domain)
			}
		}
	}
}

// A mixin must declare NO credentials at any time: sbx refuses one repeating a
// service its parent already declares, and every built-in-backed def ships a
// mixin. Measured as `400 ... defined in both "shell" and "credprobe"`.
func TestMixinNeverDeclaresCredentials(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	for _, target := range []string{"claudecode", "cursor", "opencode"} {
		in := specInput(target)
		in.Detected = []string{"anthropic"}
		in.Lookup = func(k string) string {
			if k == "ANTHROPIC_API_KEY" {
				return "sk-ant-real-key"
			}
			return ""
		}
		_, kit, _ := Spec(in)
		if kit.Kind != "mixin" {
			t.Fatalf("%s: kind = %q, want mixin", target, kit.Kind)
		}
		if len(kit.Credentials) != 0 {
			t.Errorf("%s: mixin declares %d credentials — sbx refuses a service the built-in "+
				"agent already declares", target, len(kit.Credentials))
		}
	}
}
