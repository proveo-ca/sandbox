package sandbox

import (
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func stubEntrypoint(target string) func(string) []string {
	return func(string) []string { return []string{"dumb-init", "--", target + "-entrypoint"} }
}

func specInput(target string, extra ...string) Input {
	return Input{
		Target:          target,
		Image:           "proveo/" + target + ":latest",
		Extra:           extra,
		Lookup:          func(string) string { return "" },
		ImageEntrypoint: stubEntrypoint(target),
	}
}

func specFor(t *testing.T, target string, extra ...string) (sbx.RunConfig, sbx.Kit) {
	t.Helper()
	cfg, kit, _ := Spec(specInput(target, extra...))
	return cfg, kit
}

// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestOptingOutStillBorrowsTheShellAgent(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "0")
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

func TestOwnAgentReplacesTheBorrowedShell(t *testing.T) {
	cfg, kit := specFor(t, "cecli") // no env: the kit path is the DEFAULT now

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
	if len(kit.Sandbox.Entrypoint) == 0 {
		t.Error("kit declares no entrypoint, so sbx opens a shell instead of the harness")
	}
}

func TestOwnAgentChangesIdentityAndNothingElse(t *testing.T) {
	// Opt OUT to get the mixin, then back in: the kit path is the default now,
	// so the comparison has to name both sides explicitly.
	t.Setenv(sbx.EnvAgentKit, "0")
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

// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestOwnAgentDeclaresItsOwnCredentials(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t, "anthropic")
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
		t.Fatal("sandbox kit declares no credentials — the agent would get an UNSET variable " +
			"where borrowing `shell` gave it a proxy-managed sentinel, so it cannot " +
			"authenticate at all")
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

	if len(secrets) == 0 {
		t.Error("no secrets at all — the env-var entries the gate-off path relies on are gone")
	}
	for _, kv := range secrets {
		if kv[0] == "anthropic" {
			t.Fatal("Spec stored a secret under the SERVICE name, which overwrites the " +
				"operator's own entry in a host-wide store")
		}
	}
}

func TestInjectDomainsAreAllowlisted(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t, "anthropic")
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

func TestMixinNeverDeclaresCredentials(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t, "anthropic")
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

// withStoredSecrets states which services sbx holds a secret for, for the
// duration of one test.
func withStoredSecrets(t *testing.T, names ...string) {
	t.Helper()
	prev := storedSecretNames
	storedSecretNames = func() []string { return names }
	t.Cleanup(func() { storedSecretNames = prev })
}

// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestUnstoredServicesAreNotDeclared(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t) // sbx holds nothing
	in := specInput("cecli")
	in.Lookup = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-real-key"
		}
		return ""
	}
	_, kit, _ := Spec(in)
	if len(kit.Credentials) != 0 {
		t.Errorf("declared %d credentials sbx has no secret for — each one is a prompt an "+
			"unattended run stops on", len(kit.Credentials))
	}
}

func TestSpecNeverStoresAServiceNamedSecret(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t, "anthropic")
	in := specInput("cecli")
	in.Lookup = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-real-key"
		}
		return ""
	}
	_, _, secrets := Spec(in)
	for _, kv := range secrets {
		if kv[0] == "anthropic" {
			t.Fatal("Spec would store a secret under the SERVICE name, overwriting whatever " +
				"the operator has there — measured doing exactly that to an OAuth login")
		}
	}
}

// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestAgentKitIsTheDefaultForDefsWithNoBuiltinAgent(t *testing.T) {
	if !sbx.AgentKitEnabled() {
		t.Fatal("PROVEO_SBX_AGENT_KIT defaults OFF — the flip did not take, or the " +
			"environment is overriding it")
	}
	withStoredSecrets(t, "anthropic")

	cfg, kit := specFor(t, "cecli")
	if kit.Kind != "sandbox" || cfg.Agent != sbx.AgentName("cecli") {
		t.Errorf("cecli: kind=%q agent=%q, want a sandbox kit naming proveo-cecli",
			kit.Kind, cfg.Agent)
	}
	for _, target := range []string{"claudecode", "cursor", "opencode"} {
		cfg, kit := specFor(t, target)
		if kit.Kind != "mixin" {
			t.Errorf("%s: kind=%q, want mixin — sbx refuses a kit shadowing a built-in", target, kit.Kind)
		}
		if strings.HasPrefix(cfg.Agent, "proveo-") {
			t.Errorf("%s: agent=%q shadows a built-in", target, cfg.Agent)
		}
	}
}

// The escape hatch has to work, or the default is not reversible in the field.
func TestOptOutIsHonouredForEverySpelling(t *testing.T) {
	for _, v := range []string{"0", "off", "no", "false", "disable", "disabled", "OFF"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(sbx.EnvAgentKit, v)
			cfg, kit := specFor(t, "cecli")
			if kit.Kind != "mixin" || cfg.Agent != sbx.ShellAgent {
				t.Errorf("%s=%q gave kind=%q agent=%q, want the borrowed shell path",
					sbx.EnvAgentKit, v, kit.Kind, cfg.Agent)
			}
		})
	}
}

func TestOwnAgentKitCarriesTheImageEntrypoint(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	tests := []struct {
		name  string
		image []string
		want  []string
	}{
		{
			name:  "def names its own launcher",
			image: []string{"dumb-init", "--", "cecli-entrypoint"},
			want:  []string{"dumb-init", "--", "cecli-entrypoint"},
		},
		{
			name:  "def names a path",
			image: []string{"dumb-init", "--", "/entrypoint.sh"},
			want:  []string{"dumb-init", "--", "/entrypoint.sh"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := specInput("cecli")
			in.ImageEntrypoint = func(string) []string { return tc.image }
			_, kit, _ := Spec(in)
			if kit.Sandbox == nil {
				t.Fatalf("Spec(cecli, image entrypoint %v) rendered no sandbox block", tc.image)
			}
			if diff := cmp.Diff(tc.want, kit.Sandbox.Entrypoint); diff != "" {
				t.Errorf("Spec(cecli, image entrypoint %v) kit.Sandbox.Entrypoint mismatch (-want +got):\n%s",
					tc.image, diff)
			}
		})
	}
}

func TestNoImageEntrypointFallsBackToTheShellAgent(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	tests := []struct {
		name  string
		image []string
	}{
		{name: "image declares none", image: nil},
		{name: "image not inspectable", image: []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := specInput("cecli")
			in.ImageEntrypoint = func(string) []string { return tc.image }
			cfg, kit, _ := Spec(in)
			if kit.Sandbox != nil {
				t.Errorf("Spec(cecli, image entrypoint %v) kit.Sandbox = %+v, want no sandbox block: "+
					"an entrypoint-less sandbox kit opens a shell", tc.image, kit.Sandbox)
			}
			if kit.Kind != "mixin" {
				t.Errorf("Spec(cecli, image entrypoint %v) kit.Kind = %q, want mixin", tc.image, kit.Kind)
			}
			if cfg.Agent != sbx.ShellAgent {
				t.Errorf("Spec(cecli, image entrypoint %v).Agent = %q, want %q",
					tc.image, cfg.Agent, sbx.ShellAgent)
			}
			if !sbx.IsShellLaunch(cfg.Command) {
				t.Errorf("Spec(cecli, image entrypoint %v).Command = %v, want the flag-leading shell launch",
					tc.image, cfg.Command)
			}
		})
	}
}

func TestEveryTargetFollowsItsHarnessAgent(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	for _, m := range ms {
		targets := make([]string, 0, len(m.Images))
		for target := range m.Images {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		for _, target := range targets {
			t.Run(target, func(t *testing.T) {
				in := specInput(target)
				in.Man = m
				cfg, kit, _ := Spec(in)

				wantOwn := sbx.DeclaresOwnAgent(m.Name)
				wantAgent := sbx.BuiltinAgent(m.Name)
				wantKind := "mixin"
				if wantOwn {
					wantAgent, wantKind = sbx.AgentName(m.Name), "sandbox"
				}
				if cfg.Agent != wantAgent {
					t.Errorf("Spec(%q).Agent = %q, want %q (def %q)", target, cfg.Agent, wantAgent, m.Name)
				}
				if kit.Kind != wantKind {
					t.Errorf("Spec(%q) kit.Kind = %q, want %q (def %q)", target, kit.Kind, wantKind, m.Name)
				}
			})
		}
	}
}
