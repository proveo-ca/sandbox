// SPEC: _spec/_paradigms/egress-boundary.puml, _spec/_conventions/design-decision-ids.puml, _spec/internal/egress/egress-tiers.puml, _spec/internal/egress/teardown-and-signals.puml, _spec/_paradigms/credential-boundary.puml, _spec/defs/claudecode/chrome-bridge.puml
package egress

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Command is a docker CLI invocation: the argv AFTER the literal `docker`.
type Command []string

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
	AgentNetwork    string
}

// Options parameterizes a Plan. Zero values are sensible: images default to the
// proveo/* names, GID falls back to UID.
type Options struct {
	Mode          string // "open" | "allowlist" | "review" (aliases resolved by Canonical)
	Credentials   string
	SessionID     string
	AgentName     string // e.g. "claudecode-mcp" (sanitized into network names)
	UID, GID      string
	LocalModel    string // optional Ollama model
	ModelsDir     string // host Ollama model store, mounted read-only at /models
	Providers     []string
	BrokerEnvFile string
	// ProviderDomains are extra write-allowlisted domains (space/comma separated),
	// passed to the proxy's egress policy (PROVEO_EGRESS_PROVIDER_DOMAINS).
	ProviderDomains string
	// ProviderHosts are the inference-provider endpoints this run legitimately
	// reaches, and they carry the policy's on-provider DLP exemption
	// (PROVEO_EGRESS_PROVIDER_HOSTS). They are stated independently of the broker
	// because the exemption has to hold when nothing is brokered: under
	// --credentials forward the agent carries its own key, and the provider's own
	// host is the one destination that key belongs on.
	ProviderHosts []string
	ReviewSocket  string
	AuthVar       string
	WriteHosts    []string
	// Host paths for the firewall-mode inspector.
	ConfDir  string // holds the generated CA cert
	FlowsDir string // holds flows.ndjson
	// Host paths for Squid (proxy + firewall).
	SquidConfigDir string // mounted read-only at /etc/squid
	SquidLogDir    string // mounted at /var/log/squid
	// Image overrides.
	SquidImage, ProxyImage, OllamaImage string
	HostOllama                          bool
	// OllamaGPU adds `--gpus all` to the Ollama sidecar so it is GPU-accelerated
	// (Linux + NVIDIA container runtime). Without it the sidecar runs on CPU.
	OllamaGPU bool
	// HostBridge makes host.docker.internal resolve to the REAL host gateway, so
	// the agent can reach a relay `proveo run` holds open. Only the open+forward
	// paths honour it.
	HostBridge bool
}

const (
	caContainerPath = "/etc/proveo/mitmproxy-ca-cert.pem"
	squidUpstream   = "http://squid:3128"
	inspectProxyURL = "http://mitm:8888"
	// Ollama endpoint roots for --local-model: the in-network sidecar alias, or the
	// host gateway for the host-GPU path (macOS, broker mode).
	sidecarOllamaBase = "http://ollama:11434"
	hostOllamaBase    = "http://host.docker.internal:11434"
	dnsBlackhole      = "0.0.0.0"
	// host.docker.internal, pinned either to the container's own loopback (the
	// name resolves, the host does not answer) or to the real host gateway.
	hostLoopbackAlias = "--add-host=host.docker.internal:127.0.0.1"
	hostGatewayAlias  = "--add-host=host.docker.internal:host-gateway"
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

// hostAlias is the host.docker.internal mapping for a bridge-network agent.
func (o Options) hostAlias() string {
	if o.HostBridge {
		return hostGatewayAlias
	}
	return hostLoopbackAlias
}

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

func BuildPlan(o Options) (Plan, error) {
	o.Mode, _ = Canonical(o.Mode)
	for _, m := range modeBuilders {
		if m.name == o.Mode {
			return m.build(o), nil
		}
	}
	return Plan{}, fmt.Errorf("egress: unknown mode %q", o.Mode)
}

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
			return Plan{AgentArgs: []string{"--network=bridge", o.hostAlias()}}
		}
		if o.HostOllama {
			args := []string{"--network=bridge", hostGatewayAlias}
			return Plan{AgentArgs: append(args, localModelArgs(o.LocalModel, hostOllamaBase)...)}
		}
		b := newBuilder(o)
		net := o.SessionID + "-" + o.safeAgent() + "-open-net"
		b.network(net, false)
		b.p.AgentArgs = []string{"--network", net}
		if o.HostBridge {
			// A user-defined network gets no host.docker.internal on Linux unless
			// asked; Docker Desktop adds it anyway, so this is harmless there.
			b.p.AgentArgs = append(b.p.AgentArgs, hostGatewayAlias)
		}
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

const capDropAll = "--cap-drop=ALL"

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
	if len(o.ProviderHosts) > 0 {
		c = append(c, "-e", "PROVEO_EGRESS_PROVIDER_HOSTS="+strings.Join(o.ProviderHosts, ","))
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
		"-e", "NO_PROXY=" + noProxyHosts, "-e", "no_proxy=" + noProxyHosts,
	}
}

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

func localModelArgs(model, base string) []string {
	return []string{
		"-e", "PROVEO_LOCAL_MODEL=" + model,
		"-e", "OLLAMA_HOST=" + base, "-e", "OLLAMA_API_BASE=" + base,
		// Both spellings: the OpenAI SDKs read OPENAI_BASE_URL, litellm (cecli/aider)
		// reads OPENAI_API_BASE. Setting only the first left every litellm-backed
		// harness with no endpoint for the local model.
		"-e", "OPENAI_BASE_URL=" + base + "/v1", "-e", "OPENAI_API_BASE=" + base + "/v1",
		"-e", "OPENAI_API_KEY=ollama",
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
