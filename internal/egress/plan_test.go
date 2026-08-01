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

// fwd marks the forwarded-credential tier: nothing to inject, so no MITM and the
// agent keeps an internet-capable bridge (the DinD / pinned-TLS shape).
func fwd(o Options) Options { o.Credentials = "forward"; return o }
func withBroker(o Options, p, f string) Options {
	o.Provider = p
	o.BrokerEnvFile = f
	return o
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
		if !strings.Contains(proxy, "PROVEO_EGRESS_PROVIDER=anthropic") {
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

	t.Run("local model bypasses the proxy via NO_PROXY", func(t *testing.T) {
		t.Parallel()
		p, _ := BuildPlan(withModel(baseOpts("allowlist"), "gemma4"))
		if j := strings.Join(p.AgentArgs, " "); !strings.Contains(j, "NO_PROXY=ollama") {
			t.Errorf("local-model AgentArgs must set NO_PROXY for ollama; got %q", j)
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

func TestBuildPlanAgentNetwork(t *testing.T) {
	t.Parallel()
	// Broker + local model: agent is on a user-defined bridge, so AgentNetwork is
	// exposed for a DinD sidecar to attach to by alias.
	if p, _ := BuildPlan(withModel(fwd(baseOpts("open")), "gemma4")); p.AgentNetwork == "" {
		t.Error("broker+local-model should set AgentNetwork (user-defined bridge)")
	}
	// Broker without a model: agent is on the default bridge → empty (DinD uses --link).
	if p, _ := BuildPlan(fwd(baseOpts("open"))); p.AgentNetwork != "" {
		t.Errorf("broker (no model) should leave AgentNetwork empty, got %q", p.AgentNetwork)
	}
	// Enforced-egress modes must NEVER expose an agent network for a DinD attach:
	// doing so would put an internet-capable daemon on the agent's internal net.
	for _, mode := range []string{"review", "allowlist"} {
		if p, _ := BuildPlan(baseOpts(mode)); p.AgentNetwork != "" {
			t.Errorf("%s must not set AgentNetwork (DinD attach would bypass egress), got %q", mode, p.AgentNetwork)
		}
	}
}
