// SPEC: _spec/defs/claudecode/claudecode-egress-topology.puml
//
// Command proveo-egress is the egress inspection sidecar for firewall
// mode: a Go MITM proxy that records flows, brokers credentials, and forwards to
// Squid upstream. It replaces the Python mitmproxy sidecar.
//
// Configuration is by environment so the egress lifecycle can wire it with
// `docker run -e`. Secrets are NOT passed on argv/env: the broker reads provider
// keys from a mounted 0600 env-file (PROVEO_EGRESS_BROKER_ENVFILE) and resolves
// the right one via the provider registry given PROVEO_EGRESS_PROVIDER.
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/broker"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/egresspolicy"
	"github.com/proveo-ca/proveo/internal/egressproxy"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/reviewgate"
	"github.com/proveo-ca/proveo/internal/ui"
)

func main() {
	// Subcommands let defs/lib/egress.sh delegate provider detection + Squid
	// allowlist generation to this single Go source (PROVEO_EGRESS_BIN). With no
	// subcommand the binary serves the proxy (the image ENTRYPOINT).
	switch cmd := firstArg(); cmd {
	case "detect":
		fmt.Println(strings.Join(provider.Detect(mergedLookup()), " "))
		return
	case "provider-allow":
		runProviderAllow()
		return
	case "providers":
		// The registry's provider names (for tooling like update-provider-allow.sh).
		for _, n := range provider.Names() {
			fmt.Println(n)
		}
		return
	case "serve", "":
		// fall through to serve
	default:
		log.Fatalf("proveo-egress: unknown subcommand %q (want: serve|detect|provider-allow|providers)", cmd)
	}
	serve()
}

func serve() {
	cfg := egressproxy.Config{
		Listen:      env("PROVEO_EGRESS_LISTEN", ":8888"),
		UpstreamURL: env("PROVEO_EGRESS_UPSTREAM", ""),
		CACertOut:   env("PROVEO_EGRESS_CA_CERT_OUT", ""),
		FlowsPath:   env("PROVEO_EGRESS_FLOWS", ""),
		Broker: broker.Config{
			Strip: splitCSV(env("PROVEO_EGRESS_BROKER_STRIP", "")),
		},
	}

	// The explicit escape hatch stays a single route: an operator overriding
	// PROVEO_EGRESS_BROKER_HOSTS is naming one destination on purpose, and it
	// suppresses registry-driven routing entirely so the override is total.
	if hosts := splitCSV(env("PROVEO_EGRESS_BROKER_HOSTS", "")); len(hosts) > 0 {
		cfg.Broker.Routes = []broker.Route{{
			Provider:  "explicit",
			Hosts:     hosts,
			Header:    env("PROVEO_EGRESS_BROKER_HEADER", ""),
			Query:     env("PROVEO_EGRESS_BROKER_QUERY", ""),
			ValueFile: env("PROVEO_EGRESS_BROKER_VALUE_FILE", ""),
		}}
	} else {
		cfg.Broker.Routes = registryRoutes()
	}

	// Egress policy (read-allow / write-deny / DLP) — the S1 destination/method/
	// content gate. On by default; PROVEO_EGRESS_POLICY=off disables it.
	if !isOff(env("PROVEO_EGRESS_POLICY", "on")) {
		cfg.Policy = buildPolicy(cfg.Broker)
		cfg.EnforcePolicy = true
	}

	if err := egressproxy.Run(cfg); err != nil {
		log.Fatalf("proveo-egress: %v", err)
	}
}

// buildPolicy derives the egress policy from the resolved provider hosts, a
// default write-allowlist + custom domains, the embedded exfil-sink denylist,
// and the provider secret values (from the mounted broker env-file) for DLP.
func reviewConnect() func(host, port string) bool {
	sock := env("PROVEO_EGRESS_REVIEW_SOCKET", "")
	if sock == "" || !envTruthy("PROVEO_EGRESS_REVIEW") {
		return nil
	}
	ui.Iconf("🛂", "review tier: connections gated by the operator via %s", sock)
	return func(host, port string) bool {
		return reviewgate.AskOverSocket(sock, host, port, reviewgate.DefaultDeadline+5*time.Second)
	}
}

func envTruthy(k string) bool {
	switch strings.ToLower(strings.TrimSpace(env(k, ""))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// registryRoutes builds one broker route per provider whose key is present in
// the mounted secret env-file — the file already holds every detected key, so
// the keys present ARE the providers the session can authenticate.
//
// Deriving the set from the file rather than from a name passed by the host
// keeps the two from disagreeing: a host that pins one provider while the file
// holds three used to leave the other two with the sentinel.
//
// PROVEO_EGRESS_PROVIDERS narrows the set explicitly when the operator wants a
// smaller blast radius; PROVEO_EGRESS_PROVIDER (singular) is still honoured for
// compatibility with a pinned-provider host.
func registryRoutes() []broker.Route {
	secrets := parseEnvFile(env("PROVEO_EGRESS_BROKER_ENVFILE", ""))
	if len(secrets) == 0 {
		return nil
	}
	lookup := func(k string) string { return secrets[k] }
	allow := map[string]bool{}
	for _, n := range append(splitCSV(env("PROVEO_EGRESS_PROVIDERS", "")),
		splitCSV(env("PROVEO_EGRESS_PROVIDER", ""))...) {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" && n != "none" {
			allow[n] = true
		}
	}
	preferVar := env("PROVEO_EGRESS_AUTH_VAR", "")

	var routes []broker.Route
	var skipped []string
	// Registry order, so overlapping suffixes (if ever added) resolve
	// deterministically and the log reads the same way every run.
	for _, name := range provider.Detect(lookup) {
		if len(allow) > 0 && !allow[name] {
			continue
		}
		r, ok := provider.ResolveWith(name, preferVar, lookup)
		if !ok {
			skipped = append(skipped, name) // signed-request providers: not injectable
			continue
		}
		routes = append(routes, broker.Route{
			Provider: name, Hosts: r.Hosts, Header: r.Header, Query: r.Query, Value: r.Value,
		})
	}
	if len(skipped) > 0 {
		// Never echo secrets — only (non-secret) provider names.
		ui.Warnf("proveo-egress: not broker-injectable, passing the agent's own credential through: %s",
			strings.Join(skipped, ", "))
	}
	return routes
}

func buildPolicy(bc broker.Config) egresspolicy.Config {
	// Every route's hosts: the destinations the broker treats as on-route, and so
	// the destinations a provider secret is legitimately allowed to reach.
	var providerHosts []string
	for _, r := range bc.Routes {
		providerHosts = append(providerHosts, r.Hosts...)
	}
	custom := splitCSV(strings.ReplaceAll(env("PROVEO_EGRESS_PROVIDER_DOMAINS", ""), " ", ","))

	write := append([]string{}, providerHosts...)
	write = append(write, egresspolicy.DefaultWriteHosts...)
	write = append(write, custom...)
	write = append(write, splitCSV(env("PROVEO_EGRESS_WRITE_HOSTS", ""))...)

	// DLP targets: every provider key value present, plus each route's injected
	// value. The route values are added bare (without any "Bearer " prefix) so the
	// scanner matches the secret as it would appear re-encoded in a body.
	var secrets []string
	for _, v := range parseEnvFile(env("PROVEO_EGRESS_BROKER_ENVFILE", "")) {
		if v != "" {
			secrets = append(secrets, v)
		}
	}
	for _, r := range bc.Routes {
		if r.Value != "" {
			secrets = append(secrets, strings.TrimSpace(strings.TrimPrefix(r.Value, "Bearer ")))
		}
	}

	return egresspolicy.Config{
		OpenNetwork:       envTruthy("PROVEO_EGRESS_OPEN"),
		ReviewConnect:     reviewConnect(),
		ProviderHosts:     providerHosts,
		WriteHosts:        write,
		DenySinks:         egresspolicy.DefaultSinks,
		Secrets:           secrets,
		BlockKnownSecrets: true,
		// Primary encoding-evasion defense (F1), on by default: decode base64/hex
		// tokens in the URL/body and re-scan for the exact secret + credential
		// shapes. Low false-positive — only fires when a token decodes to a real
		// secret. Disable with PROVEO_EGRESS_DLP_DECODE=off.
		DecodeScan: envBool("PROVEO_EGRESS_DLP_DECODE", true),
		// Opt-in backstop: catches encoded UNKNOWN high-entropy blobs too, but
		// false-positives on legitimate high-entropy URLs (presigned links, JWTs),
		// so it is off by default. Enable with PROVEO_EGRESS_DLP_ENTROPY=on.
		BlockEntropy:       envBool("PROVEO_EGRESS_DLP_ENTROPY", false),
		MaxOutBytesPerHost: envInt("PROVEO_EGRESS_MAX_OUT_BYTES", 16384),
	}
}

func isOff(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "0", "no", "false", "disable", "disabled":
		return true
	}
	return false
}

func envBool(name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envInt(name string, def int64) int64 {
	if v, ok := os.LookupEnv(name); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func firstArg() string {
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	return ""
}

// mergedLookup reads a var from the process env first, then falls back to the
// mounted secret env-file — mirroring the Bash `proveo_egress_key_present`,
// which checks both the environment and PROVEO_EGRESS_ENV_FILE.
func mergedLookup() func(string) string {
	secrets := parseEnvFile(env("PROVEO_EGRESS_ENV_FILE", ""))
	return func(k string) string {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			return v
		}
		return secrets[k]
	}
}

// runProviderAllow prints the Squid provider-allow.conf content for the pinned
// provider (PROVEO_EGRESS_PROVIDER) or, absent that, the auto-detected ones.
func runProviderAllow() {
	var providers []string
	if p := strings.TrimSpace(env("PROVEO_EGRESS_PROVIDER", "")); p != "" && p != "none" {
		providers = []string{p} // ProviderAllowConf normalizes comma/space
	} else {
		providers = provider.Detect(mergedLookup())
	}
	conf, matched, unknown := egress.ProviderAllowConf(providers, env("PROVEO_EGRESS_PROVIDER_DOMAINS", ""))
	if len(unknown) > 0 {
		ui.Warnf("ignoring unknown provider(s): %s", strings.Join(unknown, " "))
	}
	if len(providers) > 0 && len(matched) == 0 {
		ui.Failf("no known egress provider(s); set PROVEO_EGRESS_PROVIDER_DOMAINS to pin custom endpoints")
		os.Exit(1)
	}
	fmt.Print(conf)
}

func env(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseEnvFile reads a mounted KEY=VALUE secret file (the shape of a project
// .env). Missing/unreadable file => empty map (broker degrades to pass-through).
// Tolerates blank lines, `#` comments, a leading `export `, and surrounding
// single/double quotes on the value.
func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	return out
}
