package egress

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

func baseOpts(mode string) Options {
	return Options{
		Mode: mode, SessionID: "proveo-sess", AgentName: "claudecode-mcp",
		UID: "1000", GID: "1000", ModelsDir: "/home/tester/.ollama/models",
		ConfDir: "/state/mitmproxy/confdir", FlowsDir: "/state/mitmproxy/flows",
		SquidConfigDir: "/state/squid/config", SquidLogDir: "/state/squid/logs",
	}
}

func TestBuildPlanGolden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts Options
	}{
		{name: "open_forward", opts: fwd(baseOpts("open"))},
		{name: "open_forward_local_model", opts: withModel(fwd(baseOpts("open")), "gemma4")},
		{name: "review", opts: baseOpts("review")},
		{name: "open_broker", opts: baseOpts("open")},
		{name: "allowlist", opts: baseOpts("allowlist")},
		{name: "allowlist_inject", opts: withBroker(baseOpts("allowlist"), "anthropic", "/state/inject/broker.env")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := BuildPlan(tc.opts)
			if err != nil {
				t.Fatalf("BuildPlan(%s): unexpected error: %v", tc.name, err)
			}
			got := p.Render()
			golden := filepath.Join("testdata", tc.name+".golden")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden %s (run with -update to create): %v", golden, err)
			}
			if got != string(want) {
				t.Errorf("BuildPlan(%s) mismatch with %s (-update to refresh):\n--- got ---\n%s", tc.name, golden, got)
			}
		})
	}
}

func withModel(o Options, m string) Options { o.LocalModel = m; return o }

func fwd(o Options) Options { o.Credentials = "forward"; return o }
func withBroker(o Options, p, f string) Options {
	o.Providers = []string{p}
	o.BrokerEnvFile = f
	return o
}

// TestHostBridgeResolvesTheHostGateway covers the Claude in Chrome bridge: with
// it on, host.docker.internal names the REAL host so the agent can reach the
// `proveo run` relay; off, the open+forward path keeps pinning the name to the
// container's own loopback, and no other tier grows a route to the host.
func TestHostBridgeResolvesTheHostGateway(t *testing.T) {
	t.Parallel()
	args := func(o Options) string {
		p, err := BuildPlan(o)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(p.AgentArgs, " ")
	}
	off := args(fwd(baseOpts("open")))
	if !strings.Contains(off, "--add-host=host.docker.internal:127.0.0.1") {
		t.Errorf("bridge off must keep the loopback pin: %s", off)
	}
	o := fwd(baseOpts("open"))
	o.HostBridge = true
	on := args(o)
	if !strings.Contains(on, "--add-host=host.docker.internal:host-gateway") || strings.Contains(on, ":127.0.0.1") {
		t.Errorf("bridge on must map the host gateway and drop the loopback pin: %s", on)
	}
	// Local model on a session network: the bridge adds the gateway alias there too.
	o = withModel(fwd(baseOpts("open")), "gemma4")
	o.HostBridge = true
	if got := args(o); !strings.Contains(got, "--add-host=host.docker.internal:host-gateway") {
		t.Errorf("bridge + sidecar model must still name the host: %s", got)
	}
	// Broker mode parks the agent behind a proxy; the bridge must not punch a hole.
	o = baseOpts("open")
	o.HostBridge = true
	if got := args(o); strings.Contains(got, "host-gateway") {
		t.Errorf("broker tier must ignore HostBridge: %s", got)
	}
}

// TestLocalModelRouting covers where --local-model inference runs: the host's
// Ollama (macOS, broker) vs an in-network sidecar, GPU-accelerated or not.
func TestLocalModelRouting(t *testing.T) {
	t.Parallel()

	t.Run("host ollama (broker) spawns no sidecar and reaches the host gateway", func(t *testing.T) {
		t.Parallel()
		o := withModel(fwd(baseOpts("open")), "gemma4")
		o.HostOllama = true
		p, err := BuildPlan(o)
		if err != nil {
			t.Fatal(err)
		}
		if p.OllamaContainer != "" || len(p.Sidecars) != 0 {
			t.Fatalf("host-Ollama must not spawn a sidecar; container=%q sidecars=%d", p.OllamaContainer, len(p.Sidecars))
		}
		args := strings.Join(p.AgentArgs, " ")
		for _, want := range []string{
			"--add-host=host.docker.internal:host-gateway",
			"OLLAMA_API_BASE=http://host.docker.internal:11434",
			"ANTHROPIC_BASE_URL=http://host.docker.internal:11434",
		} {
			if !strings.Contains(args, want) {
				t.Errorf("host-Ollama agent args missing %q\nargs: %s", want, args)
			}
		}
	})

	t.Run("default broker uses the in-network sidecar, no GPU", func(t *testing.T) {
		t.Parallel()
		p, err := BuildPlan(withModel(fwd(baseOpts("open")), "gemma4"))
		if err != nil {
			t.Fatal(err)
		}
		if p.OllamaContainer == "" {
			t.Fatal("default local-model broker must spawn an Ollama sidecar")
		}
		render := p.Render()
		if !strings.Contains(render, "--network-alias ollama") {
			t.Errorf("sidecar should register the ollama alias\n%s", render)
		}
		if strings.Contains(render, "--gpus") {
			t.Errorf("sidecar must not request a GPU unless OllamaGPU is set\n%s", render)
		}
		if !strings.Contains(strings.Join(p.AgentArgs, " "), "OLLAMA_API_BASE=http://ollama:11434") {
			t.Error("sidecar env must point at the ollama alias")
		}
	})

	t.Run("OllamaGPU adds --gpus all to the sidecar", func(t *testing.T) {
		t.Parallel()
		o := withModel(fwd(baseOpts("open")), "gemma4")
		o.OllamaGPU = true
		p, err := BuildPlan(o)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.Render(), "--gpus all") {
			t.Errorf("OllamaGPU must add --gpus all to the sidecar\n%s", p.Render())
		}
	})
}

func TestBuildPlanUnknownMode(t *testing.T) {
	t.Parallel()
	if _, err := BuildPlan(Options{Mode: "nope"}); err == nil {
		t.Fatal("BuildPlan(mode=nope) = nil error, want error")
	}
}

func TestModesAndValidMode(t *testing.T) {
	t.Parallel()
	if got := Modes(); len(got) != 3 || got[0] != "open" || got[1] != "allowlist" || got[2] != "review" {
		t.Errorf("Modes() = %v, want [broker proxy firewall]", got)
	}
	for _, m := range Modes() {
		if !ValidMode(m) {
			t.Errorf("ValidMode(%q) = false for a listed mode", m)
		}
	}
	if ValidMode("nope") || ValidMode("") {
		t.Error("ValidMode must reject unknown/empty modes")
	}
}

// Plan.Images must name every sidecar image so the CLI preflight can ready
// them (pull or build) before any network/container exists.
func TestPlanImages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{name: "open+forward has no sidecars", opts: fwd(baseOpts("open")), want: nil},
		{name: "open+forward with model needs ollama", opts: withModel(fwd(baseOpts("open")), "gemma4"), want: []string{"ollama/ollama:latest"}},
		{name: "review needs squid and the inspector", opts: baseOpts("review"), want: []string{"ubuntu/squid:latest", "proveo/egress-proxy:latest"}},
		{name: "allowlist needs squid and the inspector", opts: baseOpts("allowlist"), want: []string{"ubuntu/squid:latest", "proveo/egress-proxy:latest"}},
		{name: "open+broker needs the inspector but no squid", opts: baseOpts("open"), want: []string{"proveo/egress-proxy:latest"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := BuildPlan(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(p.Images, " "); got != strings.Join(tc.want, " ") {
				t.Errorf("BuildPlan(%s).Images = %v, want %v", tc.opts.Mode, p.Images, tc.want)
			}
		})
	}
}

// Security invariants — these assert properties, not exact strings.
func TestBuildPlanInvariants(t *testing.T) {
	t.Parallel()

	t.Run("broker mode adds no proxy env and no internal network", func(t *testing.T) {
		t.Parallel()
		p, _ := BuildPlan(fwd(baseOpts("open")))
		if joined := strings.Join(p.AgentArgs, " "); strings.Contains(joined, "HTTP_PROXY") {
			t.Errorf("broker AgentArgs should not set a proxy, got %q", joined)
		}
		if len(p.Networks) != 0 {
			t.Errorf("broker (no model) should create no networks, got %v", p.Networks)
		}
	})

	t.Run("proxy+firewall agent networks are --internal; only egress net is not", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []string{"review", "allowlist"} {
			p, _ := BuildPlan(baseOpts(mode))
			for _, n := range p.Networks {
				j := strings.Join(n, " ")
				isEgress := strings.Contains(j, "squid-egress-net")
				isInternal := strings.Contains(j, "--internal")
				if isEgress && isInternal {
					t.Errorf("%s: egress network must be internet-capable (not --internal): %q", mode, j)
				}
				if !isEgress && !isInternal {
					t.Errorf("%s: non-egress network must be --internal: %q", mode, j)
				}
			}
		}
	})

	t.Run("firewall mode trusts the mitm CA and waits for it", func(t *testing.T) {
		t.Parallel()
		p, _ := BuildPlan(baseOpts("allowlist"))
		j := strings.Join(p.AgentArgs, " ")
		for _, v := range []string{"SSL_CERT_FILE=", "NODE_EXTRA_CA_CERTS=", "INSPECT_PROXY=http://mitm:8888"} {
			if !strings.Contains(j, v) {
				t.Errorf("firewall AgentArgs missing %q; got %q", v, j)
			}
		}
		if p.CAWaitPath == "" {
			t.Error("firewall plan must set CAWaitPath")
		}
	})

	t.Run("firewall wires provider + env-file into the proxy sidecar", func(t *testing.T) {
		t.Parallel()
		p, _ := BuildPlan(withBroker(baseOpts("allowlist"), "anthropic", "/state/inject/broker.env"))
		var proxy string
		for _, c := range p.Sidecars {
			if strings.Contains(strings.Join(c, " "), "proveo/egress-proxy") {
				proxy = strings.Join(c, " ")
			}
		}
		if !strings.Contains(proxy, "PROVEO_EGRESS_PROVIDERS=anthropic") {
			t.Errorf("proxy sidecar missing provider env; got %q", proxy)
		}
		if !strings.Contains(proxy, "/broker:ro") {
			t.Errorf("proxy sidecar missing broker env-file mount; got %q", proxy)
		}
	})

	t.Run("proxy+firewall blackhole external DNS on the agent", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []string{"review", "allowlist"} {
			p, _ := BuildPlan(baseOpts(mode))
			if j := strings.Join(p.AgentArgs, " "); !strings.Contains(j, "--dns 0.0.0.0") {
				t.Errorf("%s: agent must blackhole external DNS (--dns 0.0.0.0); got %q", mode, j)
			}
		}
		// broker mode must NOT blackhole DNS (it has no proxy to resolve for it).
		p, _ := BuildPlan(fwd(baseOpts("open")))
		if strings.Contains(strings.Join(p.AgentArgs, " "), "--dns") {
			t.Error("broker mode must not set --dns")
		}
	})

	// Asserts membership, not a literal: the exempt list is order-independent and
	// has grown. Loopback matters in EVERY proxied mode — an agent asked about
	// http://localhost:3000 must not send that request to the MITM, which would
	// resolve localhost in its own namespace.
	t.Run("the proxy is bypassed for loopback and the ollama sidecar", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			opts Options
		}{
			{"local model", withModel(baseOpts("allowlist"), "gemma4")},
			{"no local model", baseOpts("allowlist")},
			{"review", baseOpts("review")},
		} {
			p, _ := BuildPlan(tc.opts)
			j := strings.Join(p.AgentArgs, " ")
			for _, host := range []string{"localhost", "127.0.0.1", "ollama"} {
				if !strings.Contains(noProxyValue(j), host) {
					t.Errorf("%s: NO_PROXY must exempt %s; got %q", tc.name, host, j)
				}
			}
		}
	})

	t.Run("local model mounts host models read-only and sets the readiness wait", func(t *testing.T) {
		t.Parallel()
		p, _ := BuildPlan(withModel(fwd(baseOpts("open")), "gemma4"))
		var ollama string
		for _, c := range p.Sidecars {
			if strings.Contains(strings.Join(c, " "), "--network-alias ollama") {
				ollama = strings.Join(c, " ")
			}
		}
		if !strings.Contains(ollama, ":/models:ro") {
			t.Errorf("ollama sidecar must bind-mount host models read-only; got %q", ollama)
		}
		if p.OllamaContainer == "" {
			t.Error("local-model plan must set OllamaContainer for the readiness wait")
		}
	})

	t.Run("no ModelsDir means no mount", func(t *testing.T) {
		t.Parallel()
		o := withModel(fwd(baseOpts("open")), "gemma4")
		o.ModelsDir = ""
		p, _ := BuildPlan(o)
		if j := strings.Join(p.Sidecars[0], " "); strings.Contains(j, ":/models:ro") {
			t.Errorf("empty ModelsDir must emit no mount; got %q", j)
		}
	})
}

// fakeRunner records the docker invocations for Apply/Teardown tests.
type fakeRunner struct{ calls []string }

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	return "", nil
}

func TestApplyOrder(t *testing.T) {
	t.Parallel()
	p, _ := BuildPlan(baseOpts("allowlist"))
	var fr fakeRunner
	if err := p.Apply(&fr); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Networks must be created before sidecars run.
	firstRun, firstNet := -1, -1
	for i, c := range fr.calls {
		if strings.HasPrefix(c, "network create") && firstNet == -1 {
			firstNet = i
		}
		if strings.HasPrefix(c, "run -d") && firstRun == -1 {
			firstRun = i
		}
	}
	if firstNet == -1 || firstRun == -1 || firstNet > firstRun {
		t.Errorf("networks must be created before sidecars: netIdx=%d runIdx=%d calls=%v", firstNet, firstRun, fr.calls)
	}
}

// Which network the agent lands on, read from the argv rather than from a field.
//
// `Plan.AgentNetwork` used to record the same name so the privileged sidecar
// could attach itself to it under the alias `docker`. That sidecar is retired
// (_spec/_plans/retire-dind.puml) and nothing else ever read the field, so it is
// gone — but the topology it described is still a real decision, and asserting it
// through AgentArgs keeps the coverage without keeping a claim no code consults.
func TestBuildPlanAgentLandsOnTheRightNetwork(t *testing.T) {
	t.Parallel()
	// Forward + local model: a USER-DEFINED bridge, so the agent and the Ollama
	// sidecar can resolve each other by name.
	p, _ := BuildPlan(withModel(fwd(baseOpts("open")), "gemma4"))
	net := argvValue(p.AgentArgs, "--network")
	if net == "" || net == "bridge" {
		t.Errorf("forward+local-model should put the agent on a user-defined bridge, got %v", p.AgentArgs)
	}
	if !createsNetwork(p, net) {
		t.Errorf("the plan must CREATE the network it puts the agent on (%q), got %v", net, p.Networks)
	}

	// Forward without a model: nothing to resolve, so the default bridge — and no
	// network is created at all.
	p, _ = BuildPlan(fwd(baseOpts("open")))
	if got := argvValue(p.AgentArgs, "--network"); got != "" {
		t.Errorf("forward with no model should stay on the default bridge, got --network %q", got)
	}
	if len(p.Networks) != 0 {
		t.Errorf("forward with no model creates no network, got %v", p.Networks)
	}

	// The enforced tiers put the agent somewhere with NO route out of its own
	// accord: the network is created `--internal`. That is the property the old
	// field's invariant was really protecting — anything reaching the internet from
	// the agent's network would bypass the proxy chain the tier exists to impose.
	for _, mode := range []string{"review", "allowlist"} {
		p, _ := BuildPlan(baseOpts(mode))
		net := argvValue(p.AgentArgs, "--network")
		if net == "" {
			t.Errorf("%s must put the agent on a named network, got %v", mode, p.AgentArgs)
			continue
		}
		if !createsInternalNetwork(p, net) {
			t.Errorf("%s must create the agent network as --internal (anything internet-capable on it "+
				"would bypass egress); got %v", mode, p.Networks)
		}
	}
}

// argvValue returns the value following flag in argv, "" when absent. It also
// answers "" for the `--network=bridge` spelling, which names no created network.
func argvValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func createsNetwork(p Plan, name string) bool {
	for _, c := range p.Networks {
		if len(c) > 0 && c[len(c)-1] == name {
			return true
		}
	}
	return false
}

func createsInternalNetwork(p Plan, name string) bool {
	for _, c := range p.Networks {
		if len(c) == 0 || c[len(c)-1] != name {
			continue
		}
		for _, a := range c {
			if a == "--internal" {
				return true
			}
		}
	}
	return false
}

func TestReviewSocketIsMountedOnlyForReview(t *testing.T) {
	t.Parallel()
	withSock := func(o Options, sock string) Options { o.ReviewSocket = sock; return o }

	got, err := BuildPlan(withSock(baseOpts("review"), "/state/egress/sess/review/review.sock"))
	if err != nil {
		t.Fatal(err)
	}
	rendered := got.Render()
	for _, want := range []string{
		"PROVEO_EGRESS_REVIEW=1",
		"PROVEO_EGRESS_REVIEW_SOCKET=/review/review.sock",
		"/state/egress/sess/review:/review",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("review plan missing %q", want)
		}
	}

	bare, _ := BuildPlan(baseOpts("review"))
	if strings.Contains(bare.Render(), "PROVEO_EGRESS_REVIEW_SOCKET") {
		t.Error("review without a gate must not mount a socket")
	}
	other, _ := BuildPlan(withSock(baseOpts("allowlist"), "/state/egress/sess/review/review.sock"))
	if strings.Contains(other.Render(), "PROVEO_EGRESS_REVIEW") {
		t.Error("the allowlist tier must carry no review wiring at all")
	}
}

// Squid admits every detected provider, so the inspector must permit writes to
// the same set. Narrowing the policy to the PINNED provider means Squid lets a
// host through and the MITM blocks it — and under review that surfaces as a
// consent prompt for a host the allowlist already sanctioned.
func TestPolicyReachMatchesTheAllowlist(t *testing.T) {
	t.Parallel()
	o := baseOpts("allowlist")
	o.Providers = []string{"anthropic"} // routed for injection
	o.WriteHosts = []string{".anthropic.com", ".moonshot.ai", ".kimi.com"}
	p, err := BuildPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Render()
	if !strings.Contains(got, "PROVEO_EGRESS_WRITE_HOSTS=.anthropic.com,.moonshot.ai,.kimi.com") {
		t.Errorf("inspector did not receive every reachable host:\n%s", got)
	}
	// Reach and injection are separate questions, and the allowlist is the wider
	// of the two: the inspector is told to route anthropic while every reachable
	// provider host stays writable.
	if !strings.Contains(got, "PROVEO_EGRESS_PROVIDERS=anthropic") {
		t.Error("the inspector did not receive the routed provider set")
	}
}

// The on-provider DLP exemption travels on its own axis, so it survives a
// posture where nothing is brokered. Under --credentials forward the broker is
// inert and Providers is empty, yet the agent still calls the vendor with its
// own key: the inspector has to be told which hosts that key belongs on, or it
// blocks the one destination the credential is for.
func TestForwardPostureStillNamesProviderHosts(t *testing.T) {
	t.Parallel()
	o := baseOpts("allowlist")
	o.Credentials = "forward"
	o.Providers = nil // inert broker: no routes to derive the exemption from
	o.ProviderHosts = []string{".anthropic.com"}
	o.WriteHosts = []string{".anthropic.com"}
	p, err := BuildPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Render()
	if !strings.Contains(got, "PROVEO_EGRESS_PROVIDER_HOSTS=.anthropic.com") {
		t.Errorf("the inspector was not told which hosts the forwarded key belongs on:\n%s", got)
	}
}

// noProxyValue extracts the NO_PROXY value from a flattened arg string so the
// assertion tests the exempt SET rather than the literal the code happens to emit.
func noProxyValue(joined string) string {
	const key = "NO_PROXY="
	i := strings.Index(joined, key)
	if i < 0 {
		return ""
	}
	rest := joined[i+len(key):]
	if j := strings.Index(rest, " "); j >= 0 {
		return rest[:j]
	}
	return rest
}
