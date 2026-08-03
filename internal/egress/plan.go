// SPEC: _spec/_paradigms/egress-boundary.puml
package egress

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Command is a docker CLI invocation: the argv AFTER the literal `docker`.
type Command []string

// Plan is the full egress topology for one run, as pure data so it can be
// golden-tested without Docker. Ports the orchestration in
// defs/lib/egress.sh `proveo_egress_prepare`.
type Plan struct {
	Networks        []Command // `network create ...`
	Sidecars        []Command // `run -d ...` (squid, proxy, ollama)
	Connects        []Command // `network connect ...`
	AgentArgs       []string  // appended to the agent's `docker run`
	Cleanup         []Command // teardown: `rm -f ...`, `network rm ...`
	CAWaitPath      string    // host path to await before trusting the CA (firewall mode)
	OllamaContainer string    // local-model sidecar to await before launching the agent
	SquidContainer  string    // Squid sidecar to await (accepting on :3128) before the agent
	ProxyContainer  string    // MITM inspector sidecar, named so its logs can be captured at teardown
	UsesSquid       bool      // proxy/firewall stage a Squid config + logs dir
	Images          []string  // every sidecar image, for the preflight (in add order)
	// AgentNetwork names the user-defined Docker network the agent runs on, or ""
	// when the agent is on the default bridge. It exists solely so an optional DinD
	// sidecar can be attached to that network by alias in broker mode. It is left
	// empty for proxy/firewall on purpose: those put the agent on an --internal
	// network, and attaching an internet-capable daemon to it would defeat egress
	// enforcement — so DinD is never wired there (see cmd/proveo dind gating).
	AgentNetwork string
}

// Options parameterizes a Plan. Zero values are sensible: images default to the
// proveo/* names, GID falls back to UID.
type Options struct {
	Mode        string // "open" | "allowlist" | "review" (aliases resolved by Canonical)
	Credentials string
	SessionID   string
	AgentName   string // e.g. "claudecode-mcp" (sanitized into network names)
	UID, GID    string
	LocalModel  string // optional Ollama model
	ModelsDir   string // host Ollama model store, mounted read-only at /models
	// Broker (enforced modes): every provider to hold a route for, plus the host
	// env-file holding their keys. The proxy derives routes from the keys actually
	// present in that file; this list narrows the set when the operator asks for a
	// smaller blast radius, or pins a vendor-locked harness to its own vendor.
	Providers     []string
	BrokerEnvFile string
	// ProviderDomains are extra write-allowlisted domains (space/comma separated),
	// passed to the proxy's egress policy (PROVEO_EGRESS_PROVIDER_DOMAINS).
	ProviderDomains string
	ReviewSocket    string
	AuthVar         string
	// WriteHosts are every reachable provider's endpoints. The broker pins ONE
	// provider for injection, but the policy must permit writes wherever the
	// allowlist permits connections — otherwise Squid lets a host through and the
	// inspector blocks it, which under review also surfaces as a consent prompt for
	// a host that was already sanctioned.
	WriteHosts []string
	// Host paths for the firewall-mode inspector.
	ConfDir  string // holds the generated CA cert
	FlowsDir string // holds flows.ndjson
	// Host paths for Squid (proxy + firewall).
	SquidConfigDir string // mounted read-only at /etc/squid
	SquidLogDir    string // mounted at /var/log/squid
	// Image overrides.
	SquidImage, ProxyImage, OllamaImage string
	// HostOllama routes --local-model at the host's Ollama (host.docker.internal)
	// in broker mode instead of a CPU-only sidecar — for hosts (macOS) that can't
	// pass a GPU into a Linux container, where a sidecar would be unusably slow. The
	// locked modes (proxy/firewall) ignore it and keep the isolated sidecar.
	HostOllama bool
	// OllamaGPU adds `--gpus all` to the Ollama sidecar so it is GPU-accelerated
	// (Linux + NVIDIA container runtime). Without it the sidecar runs on CPU.
	OllamaGPU bool
}

const (
	caContainerPath = "/etc/proveo/mitmproxy-ca-cert.pem"
	squidUpstream   = "http://squid:3128"
	inspectProxyURL = "http://mitm:8888"
	// Ollama endpoint roots for --local-model: the in-network sidecar alias, or the
	// host gateway for the host-GPU path (macOS, broker mode).
	sidecarOllamaBase = "http://ollama:11434"
	hostOllamaBase    = "http://host.docker.internal:11434"
	// dnsBlackhole is the agent's DNS upstream in proxy/firewall modes. The
	// agent resolves nothing itself (the proxy resolves target hosts), so
	// pointing external resolution at a dead address closes the DNS-tunneling
	// exfil channel while Docker still resolves the sidecar aliases internally.
	dnsBlackhole = "0.0.0.0"
)

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

func (o Options) squidImage() string  { return orElse(o.SquidImage, "ubuntu/squid:latest") }
func (o Options) proxyImage() string  { return orElse(o.ProxyImage, "proveo/egress-proxy:latest") }
func (o Options) ollamaImage() string { return orElse(o.OllamaImage, "ollama/ollama:latest") }
func (o Options) gid() string         { return orElse(o.GID, o.UID) }
func (o Options) user() string {
	if o.UID == "" {
		return ""
	}
	return o.UID + ":" + o.gid()
}
func (o Options) safeAgent() string { return nonAlnum.ReplaceAllString(o.AgentName, "-") }

// modeBuilders is the single source of truth for egress modes: their canonical
// order (for CLI help/validation) and their plan builder (for dispatch). Adding
// a mode is a one-line entry here (D3).
var modeBuilders = []struct {
	name  string
	build func(Options) Plan
}{
	{"open", buildOpen},
	{"allowlist", buildAllowlist},
	{"review", buildReview},
}

var modeAliases = map[string]string{
	"broker":   "open",
	"firewall": "allowlist",
	"proxy":    "review",
}

func Canonical(name string) (canonical string, aliased bool) {
	if to, ok := modeAliases[name]; ok {
		return to, true
	}
	return name, false
}

// Ordered riskier → safer, because the prompt renders them left to right under a
// "safer →" axis. forward hands the real key to the container; broker keeps it in
// the egress layer.
var credentialModes = []string{"forward", "broker"}

func CredentialModes() []string { return append([]string(nil), credentialModes...) }

func ValidCredentials(name string) bool {
	if name == "" {
		return true
	}
	for _, c := range credentialModes {
		if c == name {
			return true
		}
	}
	return false
}

// Modes returns the valid egress mode names in canonical order.
func Modes() []string {
	out := make([]string, len(modeBuilders))
	for i, m := range modeBuilders {
		out[i] = m.name
	}
	return out
}

// ValidMode reports whether name is a known egress mode.
func ValidMode(name string) bool {
	name, _ = Canonical(name)
	for _, m := range modeBuilders {
		if m.name == name {
			return true
		}
	}
	return false
}

// BuildPlan renders the topology for the mode. It encodes the security
// invariant that only Squid is internet-capable: the agent and inspector sit on
// `--internal` networks and can reach the internet only by transiting Squid.
func BuildPlan(o Options) (Plan, error) {
	o.Mode, _ = Canonical(o.Mode)
	for _, m := range modeBuilders {
		if m.name == o.Mode {
			return m.build(o), nil
		}
	}
	return Plan{}, fmt.Errorf("egress: unknown mode %q", o.Mode)
}

// builder accumulates a Plan so every created network/sidecar is paired with its
// teardown command at the moment it's added — no separate hand-synced cleanup
// lists to forget, which was a silent-container-leak hazard (D1).
type builder struct {
	o          Options
	p          Plan
	containers []string // in add order; removed before networks
	nets       []string // in add order
}

func newBuilder(o Options) *builder { return &builder{o: o} }

func (b *builder) network(name string, internal bool) {
	b.p.Networks = append(b.p.Networks, netCreate(name, internal, b.o.SessionID))
	b.nets = append(b.nets, name)
}

func (b *builder) sidecar(cmd Command, name string) {
	b.p.Sidecars = append(b.p.Sidecars, cmd)
	b.containers = append(b.containers, name)
	// Every sidecar run command ends with its image (none takes a trailing
	// container command), so record it here for the image preflight.
	b.p.Images = append(b.p.Images, cmd[len(cmd)-1])
}

// attachLocalModel adds the optional Ollama sidecar + its agent env on net.
// Shared by all three modes so the local-model wiring can't drift (D1). The
// sidecar path is unconditional here; the host-Ollama alternative (macOS) is a
// broker-only branch in buildBroker, since the locked modes must not reach the
// host.
func (b *builder) attachLocalModel(net string) {
	if b.o.LocalModel == "" {
		return
	}
	b.sidecar(ollamaRun(b.o, net), ollamaName(b.o))
	b.p.OllamaContainer = ollamaName(b.o)
	b.p.AgentArgs = append(b.p.AgentArgs, localModelArgs(b.o.LocalModel, sidecarOllamaBase)...)
}

func (b *builder) done() Plan {
	b.p.Cleanup = teardown(b.o.SessionID, b.containers, b.nets)
	return b.p
}

func (o Options) forwardsCredentials() bool { return o.Credentials == "forward" }

func buildOpen(o Options) Plan {
	if o.forwardsCredentials() {
		if o.LocalModel == "" {
			return Plan{AgentArgs: []string{"--network=bridge", "--add-host=host.docker.internal:127.0.0.1"}}
		}
		if o.HostOllama {
			args := []string{"--network=bridge", "--add-host=host.docker.internal:host-gateway"}
			return Plan{AgentArgs: append(args, localModelArgs(o.LocalModel, hostOllamaBase)...)}
		}
		b := newBuilder(o)
		net := o.SessionID + "-" + o.safeAgent() + "-open-net"
		b.network(net, false)
		b.p.AgentArgs = []string{"--network", net}
		b.p.AgentNetwork = net
		b.attachLocalModel(net)
		return b.done()
	}
	b := newBuilder(o)
	agentNet := o.SessionID + "-" + o.safeAgent() + "-open-net"
	egressNet := o.SessionID + "-open-egress-net"
	b.network(agentNet, true)
	b.network(egressNet, false)
	b.sidecar(proxyRun(o, agentNet, ""), proxyName(o))
	b.p.ProxyContainer = proxyName(o)
	b.p.Connects = append(b.p.Connects, netConnect(egressNet, proxyName(o)))
	b.p.AgentArgs = append(b.p.AgentArgs, "--network", agentNet, "--dns", dnsBlackhole,
		"-e", "INSPECT_PROXY="+inspectProxyURL)
	b.p.AgentArgs = append(b.p.AgentArgs, proxyEnvArgs(o, inspectProxyURL)...)
	b.p.AgentArgs = append(b.p.AgentArgs, caTrustArgs(o.ConfDir)...)
	b.p.CAWaitPath = o.ConfDir + "/mitmproxy-ca-cert.pem"
	b.attachLocalModel(agentNet)
	return b.done()
}

func buildAllowlist(o Options) Plan { return buildEnforced(o) }

func buildReview(o Options) Plan { return buildEnforced(o) }

func buildEnforced(o Options) Plan {
	b := newBuilder(o)
	agentNet := o.SessionID + "-" + o.safeAgent() + "-mitm-net"
	enforceNet := o.SessionID + "-mitm-squid-net"
	egressNet := o.SessionID + "-squid-egress-net"
	b.network(agentNet, true)
	b.network(enforceNet, true)
	b.network(egressNet, false)
	b.p.UsesSquid = true
	b.sidecar(squidRun(o, egressNet), squidName(o))
	b.p.SquidContainer = squidName(o)
	b.sidecar(proxyRun(o, agentNet, squidUpstream), proxyName(o))
	b.p.ProxyContainer = proxyName(o)
	b.p.Connects = append(b.p.Connects,
		netConnectAlias(enforceNet, squidName(o), "squid"),
		netConnect(enforceNet, proxyName(o)),
	)
	b.p.AgentArgs = append(b.p.AgentArgs, "--network", agentNet, "--dns", dnsBlackhole,
		"-e", "INSPECT_PROXY="+inspectProxyURL, "-e", "ENFORCEMENT_PROXY="+squidUpstream)
	b.p.AgentArgs = append(b.p.AgentArgs, proxyEnvArgs(o, inspectProxyURL)...)
	b.p.AgentArgs = append(b.p.AgentArgs, caTrustArgs(o.ConfDir)...)
	b.p.CAWaitPath = o.ConfDir + "/mitmproxy-ca-cert.pem"
	b.attachLocalModel(agentNet)
	return b.done()
}

// --- command builders ------------------------------------------------------

func label(sid string) string { return "proveo.egress.session=" + sid }

func netCreate(name string, internal bool, sid string) Command {
	c := Command{"network", "create", "--label", label(sid)}
	if internal {
		c = append(c, "--internal")
	}
	return append(c, name)
}

func netConnect(net, container string) Command {
	return Command{"network", "connect", net, container}
}
func netConnectAlias(net, container, alias string) Command {
	return Command{"network", "connect", "--alias", alias, net, container}
}

func squidName(o Options) string  { return o.SessionID + "-squid" }
func proxyName(o Options) string  { return o.SessionID + "-egress" }
func ollamaName(o Options) string { return o.SessionID + "-ollama" }

// sidecarHardening is the privilege-reduction baseline for sidecars: block
// setuid escalation and cap the pid count. Applied to every sidecar.
func sidecarHardening() []string {
	return []string{"--security-opt=no-new-privileges:true", "--pids-limit=256"}
}

// capDropAll drops all Linux capabilities. Used only for sidecars that run as a
// fixed user (the Go proxy, Ollama); Squid is omitted because it needs
// CAP_SETUID/SETGID to drop from root to its own worker user.
const capDropAll = "--cap-drop=ALL"

// proxyMemoryLimit caps the egress proxy container's memory. Request bodies now
// stream past the DLP scan window (internal/egresspolicy bounds the buffer to
// ~1 MiB/request), so steady state is tens of MiB; this is a safety ceiling so a
// burst of large concurrent uploads can't grow the proxy unbounded and OOM the host.
const proxyMemoryLimit = "512m"

func squidRun(o Options, egressNet string) Command {
	c := Command{"run", "-d", "--rm", "--name", squidName(o), "--label", label(o.SessionID)}
	c = append(c, sidecarHardening()...)
	c = append(c, "--network", egressNet,
		"-v", o.SquidConfigDir+":/etc/squid:ro",
		"-v", o.SquidLogDir+":/var/log/squid")
	return append(c, o.squidImage())
}

func proxyRun(o Options, agentNet, upstream string) Command {
	c := Command{"run", "-d", "--rm", "--name", proxyName(o)}
	if u := o.user(); u != "" {
		c = append(c, "--user", u)
	}
	c = append(c, capDropAll)
	c = append(c, sidecarHardening()...)
	c = append(c, "--memory="+proxyMemoryLimit)
	c = append(c, "--label", label(o.SessionID), "--network", agentNet, "--network-alias", "mitm",
		"-e", "PROVEO_EGRESS_LISTEN=:8888",
		"-e", "PROVEO_EGRESS_CA_CERT_OUT=/confdir/mitmproxy-ca-cert.pem",
		"-e", "PROVEO_EGRESS_FLOWS=/flows/flows.ndjson",
		"-v", o.ConfDir+":/confdir",
		"-v", o.FlowsDir+":/flows")
	if upstream != "" {
		c = append(c, "-e", "PROVEO_EGRESS_UPSTREAM="+upstream)
	}
	if o.Mode == "review" {
		c = append(c, "-e", "PROVEO_EGRESS_REVIEW=1")
		if o.ReviewSocket != "" {
			c = append(c, "-e", "PROVEO_EGRESS_REVIEW_SOCKET=/review/"+filepath.Base(o.ReviewSocket),
				"-v", filepath.Dir(o.ReviewSocket)+":/review")
		}
	}
	if o.Mode == "open" {
		c = append(c, "-e", "PROVEO_EGRESS_OPEN=1")
	}
	if len(o.Providers) > 0 {
		c = append(c, "-e", "PROVEO_EGRESS_PROVIDERS="+strings.Join(o.Providers, ","))
	}
	if o.AuthVar != "" {
		c = append(c, "-e", "PROVEO_EGRESS_AUTH_VAR="+o.AuthVar)
	}
	if len(o.WriteHosts) > 0 {
		c = append(c, "-e", "PROVEO_EGRESS_WRITE_HOSTS="+strings.Join(o.WriteHosts, ","))
	}
	if o.ProviderDomains != "" {
		c = append(c, "-e", "PROVEO_EGRESS_PROVIDER_DOMAINS="+o.ProviderDomains)
	}
	if o.BrokerEnvFile != "" {
		c = append(c, "-e", "PROVEO_EGRESS_BROKER_ENVFILE=/broker/broker.env",
			"-v", dirOf(o.BrokerEnvFile)+":/broker:ro")
	}
	return append(c, o.proxyImage())
}

func ollamaRun(o Options, net string) Command {
	c := Command{"run", "-d", "--rm", "--name", ollamaName(o), "--label", label(o.SessionID)}
	c = append(c, capDropAll)
	c = append(c, sidecarHardening()...)
	if o.OllamaGPU { // Linux + NVIDIA runtime: GPU-accelerate local inference
		c = append(c, "--gpus", "all")
	}
	c = append(c, "--network", net, "--network-alias", "ollama",
		"-e", "OLLAMA_HOST=0.0.0.0:11434", "-e", "OLLAMA_MODELS=/models",
		// Agentic coding needs a large context window; Ollama's small default
		// (2–4k) truncates tool schemas + repo context and breaks every
		// local-capable harness. 32k is the agentic floor the vendor docs recommend.
		"-e", "OLLAMA_CONTEXT_LENGTH=32768")
	if o.ModelsDir != "" { // serve the host's pulled models read-only (cf. defs/lib/egress.sh)
		c = append(c, "-v", o.ModelsDir+":/models:ro")
	}
	return append(c, o.ollamaImage())
}

func proxyEnvArgs(o Options, proxyURL string) []string {
	return []string{
		"-e", "PROVEO_EGRESS_SESSION_ID=" + o.SessionID,
		"-e", "PROVEO_EGRESS_MODE=" + o.Mode,
		"-e", "HTTP_PROXY=" + proxyURL, "-e", "HTTPS_PROXY=" + proxyURL,
		"-e", "http_proxy=" + proxyURL, "-e", "https_proxy=" + proxyURL,
		// Loopback must never be proxied. Without this, an agent asked about
		// http://localhost:6006 sends that request to the MITM, which resolves
		// localhost in ITS OWN namespace — so the operator's dev server is
		// unreachable and a request that should never have left the container
		// does. localModelArgs sets the same list for the Ollama sidecar; this
		// is the floor that applies whether or not --local-model is in play.
		"-e", "NO_PROXY=" + noProxyHosts, "-e", "no_proxy=" + noProxyHosts,
	}
}

// noProxyHosts are the destinations that bypass HTTP_PROXY: the agent's own
// loopback and the Docker host alias. Not an egress hole — nothing here leaves
// the container's network namespace.
const noProxyHosts = "localhost,127.0.0.1,::1,ollama,host.docker.internal"

func caTrustArgs(confDir string) []string {
	return []string{
		"-v", confDir + "/mitmproxy-ca-cert.pem:" + caContainerPath + ":ro",
		"-e", "PROVEO_EGRESS_CA_CERT=" + caContainerPath,
		"-e", "SSL_CERT_FILE=" + caContainerPath,
		"-e", "REQUESTS_CA_BUNDLE=" + caContainerPath,
		"-e", "NODE_EXTRA_CA_CERTS=" + caContainerPath,
		"-e", "CURL_CA_BUNDLE=" + caContainerPath,
		"-e", "GIT_SSL_CAINFO=" + caContainerPath,
	}
}

// localModelArgs is the agent-agnostic env for --local-model: a SUPERSET each
// harness consumes selectively, so the wiring can't drift per agent (D1). It
// speaks three provider dialects at the same Ollama sidecar, one per harness
// family:
//   - OpenAI-compatible (opencode's ollama provider): OPENAI_BASE_URL + …/v1
//   - litellm/Ollama (cecli/aider): OLLAMA_API_BASE + ollama_chat/<model>
//   - Anthropic Messages (Claude Code): ANTHROPIC_BASE_URL at Ollama's native
//     Anthropic endpoint (Ollama >=0.14), a dummy auth token, and the model-name
//     overrides so `claude` requests <model> instead of a claude-* id. Empty
//     ANTHROPIC_API_KEY forces the local path over any inherited cloud key.
//
// Cursor is intentionally absent: its inference is vendor-pinned (see run wiring).
// base is the Ollama endpoint root: the sidecar alias (sidecarOllamaBase) or the
// host gateway (hostOllamaBase) for the macOS host-GPU path.
func localModelArgs(model, base string) []string {
	return []string{
		"-e", "PROVEO_LOCAL_MODEL=" + model,
		"-e", "OLLAMA_HOST=" + base, "-e", "OLLAMA_API_BASE=" + base,
		"-e", "OPENAI_BASE_URL=" + base + "/v1", "-e", "OPENAI_API_KEY=ollama",
		"-e", "ARCHITECT_MODEL=ollama/" + model, "-e", "EDITOR_MODEL=ollama/" + model,
		"-e", "SMALL_MODEL=ollama/" + model,
		"-e", "ANTHROPIC_BASE_URL=" + base, "-e", "ANTHROPIC_AUTH_TOKEN=ollama",
		"-e", "ANTHROPIC_API_KEY=",
		"-e", "ANTHROPIC_MODEL=" + model, "-e", "ANTHROPIC_SMALL_FAST_MODEL=" + model,
		"-e", "NO_PROXY=" + noProxyHosts, "-e", "no_proxy=" + noProxyHosts,
	}
}

func teardown(sid string, containers, nets []string) []Command {
	var out []Command
	for _, c := range containers {
		out = append(out, Command{"rm", "-f", c})
	}
	for _, n := range nets {
		out = append(out, Command{"network", "rm", n})
	}
	return out
}

func orElse(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}
