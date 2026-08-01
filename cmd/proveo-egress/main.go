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

	"github.com/proveo-ca/proveo/internal/broker"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/egresspolicy"
	"github.com/proveo-ca/proveo/internal/egressproxy"
	"github.com/proveo-ca/proveo/internal/provider"
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
			Hosts:     splitCSV(env("PROVEO_EGRESS_BROKER_HOSTS", "")),
			Header:    env("PROVEO_EGRESS_BROKER_HEADER", ""),
			Query:     env("PROVEO_EGRESS_BROKER_QUERY", ""),
			ValueFile: env("PROVEO_EGRESS_BROKER_VALUE_FILE", ""),
			Strip:     splitCSV(env("PROVEO_EGRESS_BROKER_STRIP", "")),
		},
	}

	// Provider-driven broker: resolve host/header/value from the registry using a
	// mounted secret env-file. Explicit PROVEO_EGRESS_BROKER_* env still wins.
	if name := env("PROVEO_EGRESS_PROVIDER", ""); name != "" {
		secrets := parseEnvFile(env("PROVEO_EGRESS_BROKER_ENVFILE", ""))
		if r, ok := provider.Resolve(name, func(k string) string { return secrets[k] }); ok {
			if len(cfg.Broker.Hosts) == 0 {
				cfg.Broker.Hosts = r.Hosts
			}
			if cfg.Broker.Header == "" {
				cfg.Broker.Header = r.Header
			}
			if cfg.Broker.Query == "" {
				cfg.Broker.Query = r.Query
			}
			if cfg.Broker.Value == "" {
				cfg.Broker.Value = r.Value
			}
		} else {
			// Never echo secrets — only the (non-secret) provider name.
			ui.Warnf("proveo-egress: provider %q not broker-injectable; running inspect-only", name)
		}
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
// envTruthy reports whether an env flag is set to a truthy spelling.
func envTruthy(k string) bool {
	switch strings.ToLower(strings.TrimSpace(env(k, ""))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func buildPolicy(bc broker.Config) egresspolicy.Config {
	providerHosts := bc.Hosts // set by the provider-driven broker block above
	custom := splitCSV(strings.ReplaceAll(env("PROVEO_EGRESS_PROVIDER_DOMAINS", ""), " ", ","))

	write := append([]string{}, providerHosts...)
	write = append(write, egresspolicy.DefaultWriteHosts...)
	write = append(write, custom...)
	write = append(write, splitCSV(env("PROVEO_EGRESS_WRITE_HOSTS", ""))...)

	// DLP targets: every provider key value present, plus the resolved inject value.
	var secrets []string
	for _, v := range parseEnvFile(env("PROVEO_EGRESS_BROKER_ENVFILE", "")) {
		if v != "" {
			secrets = append(secrets, v)
		}
	}
	if bc.Value != "" {
		secrets = append(secrets, strings.TrimSpace(strings.TrimPrefix(bc.Value, "Bearer ")))
	}

	return egresspolicy.Config{
		OpenNetwork:       envTruthy("PROVEO_EGRESS_OPEN"),
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
