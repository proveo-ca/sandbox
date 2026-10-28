// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package sandbox

import (
	"slices"
	"sort"
	"strings"
	"testing"

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

func specFor(t *testing.T, target string, extra ...string) (sbx.RunConfig, Workload) {
	t.Helper()
	cfg, w, _ := Spec(specInput(target, extra...))
	return cfg, w
}

// withStoredSecrets states which services sbx holds a secret for, for the duration of one test.
func withStoredSecrets(t *testing.T, names ...string) {
	t.Helper()
	prev := storedSecretNames
	storedSecretNames = func() []string { return names }
	t.Cleanup(func() { storedSecretNames = prev })
}

func anthropicKeyInput(target string) Input {
	in := specInput(target)
	in.Lookup = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-real-key"
		}
		return ""
	}
	return in
}

func TestEveryTargetRunsAsItsOwnProveoWorkload(t *testing.T) {
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
				cfg, w, _ := Spec(in)
				want := sbx.AgentName(m.Name)
				if cfg.Agent != want || !strings.HasSuffix(cfg.KitDir, "/"+want) {
					t.Errorf("Spec(%q) agent %q kit dir %q; want both named %q — sbx registers the dir name",
						target, cfg.Agent, cfg.KitDir, want)
				}
				if cfg.Image != "" {
					t.Errorf("Spec(%q) passes -t %s; the recipe's FROM carries the image (-t brings the flavor warning back, P1)", target, cfg.Image)
				}
				if w.Recipe.From != in.Image {
					t.Errorf("Spec(%q) recipe FROM %q, want %q", target, w.Recipe.From, in.Image)
				}
				if len(w.Descriptor.Capabilities) == 0 || w.Descriptor.Capabilities[0].Type != sbx.CapSbx {
					t.Errorf("Spec(%q) declares no sbx@1 first; without it the image ENTRYPOINT is PID 1", target)
				}
			})
		}
	}
}

// SPEC: _spec/internal/sbx/kit-image-source.puml
func TestTheRecipeAndTheCreateProcessFollowTheImageSource(t *testing.T) {
	const digest = "sha256:ed1aa77cdeb03b2d810fd86ca0d15c7b1a44c9c840580b0539dbf66ac710d01f"
	cases := []struct {
		name     string
		src      sbx.ImageSource
		wantFrom string
		wantEnv  []string
	}{
		{
			name:     "host image: digest-pinned FROM, host builder on the sbx process",
			src:      sbx.ImageSource{Ref: "proveo/codex:local", Host: true, Digest: digest, Builder: "default"},
			wantFrom: "proveo/codex:local@" + digest,
			wantEnv:  []string{"BUILDX_BUILDER=default"},
		},
		{
			name:     "published image the host lacks: FROM as chosen, sbx's builder",
			src:      sbx.ImageSource{Ref: "proveo/codex:local"},
			wantFrom: "proveo/codex:local",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := specInput("codex")
			in.Image = tc.src.Ref
			var asked string
			in.ImageSource = func(ref string) sbx.ImageSource { asked = ref; return tc.src }
			cfg, w, _ := Spec(in)
			if asked != in.Image {
				t.Errorf("Spec resolved the source of %q, want %q", asked, in.Image)
			}
			if w.Recipe.From != tc.wantFrom {
				t.Errorf("Spec(%+v) recipe FROM %q, want %q", tc.src, w.Recipe.From, tc.wantFrom)
			}
			if !slices.Equal(cfg.HostEnv, tc.wantEnv) {
				t.Errorf("Spec(%+v) HostEnv = %q, want %q", tc.src, cfg.HostEnv, tc.wantEnv)
			}
			if slices.ContainsFunc(cfg.Env, func(e string) bool { return strings.HasPrefix(e, "BUILDX_BUILDER") }) {
				t.Errorf("Spec(%+v) Env = %q: the kit build env must not reach the sandbox", tc.src, cfg.Env)
			}
		})
	}
}

func TestAHarnessWithAPublishedKitLaunchesThroughItAndKeepsItsFlags(t *testing.T) {
	cfg, w := specFor(t, "claudecode")
	if want := []string{"claude", "--dangerously-skip-permissions"}; !slices.Equal(w.Recipe.Entrypoint, want) {
		t.Errorf("recipe ENTRYPOINT = %q, want the published kit's %q", w.Recipe.Entrypoint, want)
	}
	if cfg.Command != nil {
		t.Errorf("no extras ⇒ no trailing command (ENTRYPOINT + CMD run as is), got %q", cfg.Command)
	}
	cfg, _ = specFor(t, "claudecode", "-p", "hi")
	if want := []string{"-p", "hi"}; !slices.Equal(cfg.Command, want) {
		t.Errorf("command = %q, want %q — a leading flag appends to the kit's launch (P7)", cfg.Command, want)
	}
}

func TestAHarnessWithoutAPublishedKitLaunchesTheImageEntrypoint(t *testing.T) {
	cfg, w := specFor(t, "hermes", "--yes")
	if len(w.Recipe.Entrypoint) != 0 {
		t.Errorf("recipe ENTRYPOINT = %q; FROM already carries the image's own", w.Recipe.Entrypoint)
	}
	if want := []string{"--yes"}; !slices.Equal(cfg.Command, want) {
		t.Errorf("command = %q, want %q — a leading flag appends to ENTRYPOINT + CMD; a leading -- would re-run the entrypoint (P7)", cfg.Command, want)
	}
	if cfg, _ := specFor(t, "hermes"); cfg.Command != nil {
		t.Errorf("no extras ⇒ the image ENTRYPOINT runs untouched, got %q", cfg.Command)
	}
}

func TestNoImageEntrypointLaunchesThroughBash(t *testing.T) {
	for _, image := range [][]string{nil, {}} {
		in := specInput("hermes")
		in.ImageEntrypoint = func(string) []string { return image }
		cfg, w, _ := Spec(in)
		if !slices.Equal(w.Recipe.Entrypoint, []string{"bash"}) || !sbx.IsShellLaunch(cfg.Command) {
			t.Errorf("image entrypoint %v: recipe %q command %v, want bash with the shell launch", image, w.Recipe.Entrypoint, cfg.Command)
		}
	}
}

func TestAWorkloadDeclaresProveosCredentialsProxyManaged(t *testing.T) {
	withStoredSecrets(t, "anthropic")
	in := anthropicKeyInput("hermes")
	in.Detected = []string{"anthropic"}
	_, w, secrets := Spec(in)
	var c *sbx.CredentialV3
	for _, cred := range w.Credentials() {
		if cred.Service == "anthropic" {
			c = &cred
		}
	}
	if c == nil || c.APIKey == nil {
		t.Fatalf("no anthropic credential: %+v", w.Credentials())
	}
	if !c.APIKey.ProxyManaged || c.APIKey.Name != "ANTHROPIC_API_KEY" || len(c.APIKey.Inject) == 0 {
		t.Errorf("credential = %+v; the env must hold a sentinel and the proxy must know where to inject", c.APIKey)
	}
	for _, kv := range secrets {
		if kv[0] == "ANTHROPIC_API_KEY" || kv[0] == "anthropic" {
			t.Errorf("Spec stored %q; beside sbx's subscription slot it outranks it in every sandbox", kv[0])
		}
	}
}

func TestAPublishedKitOwnsItsServiceAndProveoAddsNoSecondOwner(t *testing.T) {
	withStoredSecrets(t, "anthropic")
	in := anthropicKeyInput("claudecode")
	in.Detected = []string{"anthropic"}
	_, w, _ := Spec(in)
	n := 0
	for _, c := range w.Credentials() {
		if c.Service == "anthropic" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("anthropic declared %d times; one credential, one owner (C6) — the published claude kit's", n)
	}
}

func TestInjectDomainsAreAllowlisted(t *testing.T) {
	withStoredSecrets(t, "anthropic")
	_, w, _ := Spec(anthropicKeyInput("hermes"))
	if len(w.Credentials()) == 0 {
		t.Fatal("no credential built from the lookup alone — the test would prove nothing")
	}
	allowed := map[string]bool{}
	for _, a := range w.Allow() {
		allowed[a] = true
	}
	for _, c := range w.Credentials() {
		if c.APIKey == nil {
			continue
		}
		for _, inj := range c.APIKey.Inject {
			if !allowed[inj.Domain] {
				t.Errorf("credential %q injects into %q, which is not in the runtime allow list", c.Service, inj.Domain)
			}
		}
	}
}

func TestUnstoredServicesAreNotDeclared(t *testing.T) {
	withStoredSecrets(t)
	_, w, _ := Spec(anthropicKeyInput("hermes"))
	if n := len(w.Credentials()); n != 0 {
		t.Errorf("declared %d credentials sbx has no secret for — each one is a prompt an unattended run stops on", n)
	}
}

func TestSpecNeverStoresAServiceNamedSecret(t *testing.T) {
	withStoredSecrets(t, "anthropic")
	_, _, secrets := Spec(anthropicKeyInput("hermes"))
	for _, kv := range secrets {
		if kv[0] == "anthropic" {
			t.Fatal("Spec would store a secret under the SERVICE name, overwriting the operator's own entry")
		}
	}
}

func TestSpecNeverStoresAnAPIKeyBesideASubscriptionSlot(t *testing.T) {
	for _, tc := range []struct {
		name, target, key string
		stored            bool
	}{
		{name: "anthropic key under claudecode", target: "claudecode", key: "ANTHROPIC_API_KEY"},
		{name: "openai key under codex", target: "codex", key: "OPENAI_API_KEY"},
		{name: "xai key has no oauth slot", target: "opencode", key: "XAI_API_KEY", stored: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := specInput(tc.target)
			in.Man.Env = append(in.Man.Env, manifest.EnvVar{Name: tc.key, Secret: true})
			in.Lookup = func(k string) string {
				if k == tc.key {
					return "sk-real"
				}
				return ""
			}
			_, _, secrets := Spec(in)
			got := slices.ContainsFunc(secrets, func(kv [2]string) bool { return kv[0] == tc.key })
			if got != tc.stored {
				t.Errorf("Spec(%s with %s) stored it = %v, want %v", tc.target, tc.key, got, tc.stored)
			}
		})
	}
}

func TestProveoSeedHooksDeclareEveryEnvNameTheRunPasses(t *testing.T) {
	cfg, w := specFor(t, "claudecode")
	names := envNames(cfg.Env)
	var seed *Hook
	for _, h := range w.Hooks() {
		if sbx.CommandNamesSeed(h.Argv) {
			seed = &h
			break
		}
	}
	if seed == nil {
		t.Fatalf("no seed hook among %+v", w.Hooks())
	}
	if seed.Phase != "startup" {
		t.Errorf("seed hook phase = %s, want startup (re-runs every boot, as the v2 startup did)", seed.Phase)
	}
	for _, n := range names {
		if !slices.Contains(seed.Env, n) {
			t.Errorf("seed hook does not declare %s; a hook sees only the env it names (C7)", n)
		}
	}
}

func TestTheGatewayHooksFollowTheOperatorsChoice(t *testing.T) {
	gateway := func(allowed bool) int {
		n := 0
		in := specInput("claudecode")
		in.MCPGateway = allowed
		_, w, _ := Spec(in)
		for _, h := range w.Hooks() {
			if strings.HasPrefix(h.Description, "proveo:") {
				continue
			}
			if slices.Contains(h.Env, sbx.MCPGatewayEnv) || strings.Contains(h.Script, sbx.MCPGatewayEnv) {
				n++
			}
		}
		return n
	}
	t.Setenv("PROVEO_SBX_MCP", "on")
	if n := gateway(false); n != 0 {
		t.Errorf("gateway off: %d gateway hooks copied; Spec must follow Input.MCPGateway, not the env", n)
	}
	if n := gateway(true); n == 0 {
		t.Error("gateway on: no gateway hook copied")
	}
}

func TestAnAllowedGatewayIsNotBlankedAndTheSeedHookCanSeeIt(t *testing.T) {
	in := specInput("hermes")
	in.MCPGateway = true
	cfg, w, _ := Spec(in)
	if slices.Contains(cfg.Env, sbx.MCPGatewayEnv+"=") {
		t.Errorf("env %v blanks %s for a harness that takes the gateway", cfg.Env, sbx.MCPGatewayEnv)
	}
	var seed *Hook
	for _, h := range w.Hooks() {
		if sbx.CommandNamesSeed(h.Argv) {
			seed = &h
		}
	}
	if seed == nil || !slices.Contains(seed.Env, sbx.MCPGatewayEnv) || !slices.Contains(seed.Env, sbx.MCPSentinelEnv) {
		t.Fatalf("seed hook %+v does not declare the gateway names; a hook sees only the env it names", seed)
	}
}

// SPEC: _spec/internal/sbx/backed-api-key-env.puml
func TestABackedCursorKeyGetsSbxsPlaceholderAndAnUnbackedOneStaysSignIn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		target      string
		service     string
		stored      []string
		wantManaged bool
	}{
		{name: "cursor key stored", target: "cursor", service: "cursor", stored: []string{"cursor"}, wantManaged: true},
		{name: "nothing stored keeps cursor's sign-in", target: "cursor", service: "cursor"},
		{name: "only the env-var-named entry stored is not the service", target: "cursor", service: "cursor", stored: []string{"CURSOR_API_KEY"}},
		{name: "a stored anthropic key leaves claude's subscription path alone", target: "claudecode", service: "anthropic", stored: []string{"anthropic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := specInput(tc.target)
			in.StoredAPIKeys = func() []string { return tc.stored }
			_, w, _ := Spec(in)
			apiKey := credentialAPIKey(t, w, tc.service)
			if got := apiKey["proxyManaged"] == true; got != tc.wantManaged {
				t.Errorf("Spec(%q) with stored %q: %s apiKey.proxyManaged = %v, want %v", tc.target, tc.stored, tc.service, apiKey["proxyManaged"], tc.wantManaged)
			}
		})
	}
}

// credentialAPIKey is the apiKey block of the workload's credential@1 for service.
func credentialAPIKey(t *testing.T, w Workload, service string) map[string]any {
	t.Helper()
	for _, c := range w.Descriptor.Capabilities {
		cfg, ok := c.Config.(map[string]any)
		if c.Type != sbx.CapCredential || !ok || cfg["service"] != service {
			continue
		}
		apiKey, _ := cfg["apiKey"].(map[string]any)
		return apiKey
	}
	t.Fatalf("workload declares no %s credential", service)
	return nil
}
