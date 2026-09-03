// SPEC: _spec/defs/claudecode/claudecode-egress-topology.puml, _spec/internal/egressproxy/mitm-and-flow-record.puml, _spec/_paradigms/credential-boundary.puml, _spec/_conventions/design-decision-ids.puml
//
// Command proveo-egress is the egress inspection sidecar for firewall mode.
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

func reviewConnect() func(host, port string) bool {
	sock := env("PROVEO_EGRESS_REVIEW_SOCKET", "")
	if sock == "" || !envTruthy("PROVEO_EGRESS_REVIEW") {
		return nil
	}
	ui.Hostf("review tier: connections gated by the operator via %s", sock)
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
	// Plus the provider endpoints `proveo run` resolved for this run, which is
	// what keeps the exemption alive when there are no routes at all. Under
	// --credentials forward the broker is inert by design: the agent holds the
	// real key and calls the vendor itself. Deriving the exemption from routes
	// alone left providerHosts empty there, so the DLP scan saw a live
	// credential-shaped header bound for the provider's own API and answered 403
	// — the one destination the key is supposed to reach was the only one denied.
	providerHosts = append(providerHosts, splitCSV(env("PROVEO_EGRESS_PROVIDER_HOSTS", ""))...)
	custom := splitCSV(strings.ReplaceAll(env("PROVEO_EGRESS_PROVIDER_DOMAINS", ""), " ", ","))

	write := append([]string{}, providerHosts...)
	write = append(write, egresspolicy.DefaultWriteHosts...)
	write = append(write, custom...)
	write = append(write, splitCSV(env("PROVEO_EGRESS_WRITE_HOSTS", ""))...)

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
		OpenNetwork:        envTruthy("PROVEO_EGRESS_OPEN"),
		ReviewConnect:      reviewConnect(),
		ProviderHosts:      providerHosts,
		WriteHosts:         write,
		DenySinks:          egresspolicy.DefaultSinks,
		Secrets:            secrets,
		BlockKnownSecrets:  true,
		DecodeScan:         envBool("PROVEO_EGRESS_DLP_DECODE", true),
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
