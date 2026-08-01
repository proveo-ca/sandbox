// Package broker is the credential broker for the firewall egress
// mode. It imports omnigent's credential_proxy principle — "inject
// keys, never expose" — adapted to the fact that the *vendor CLI*, not this
// harness, makes the model call. The real provider secret is confined to the
// egress proxy (this process); on the pinned-provider host it injects the auth
// header, and on every other host it strips the known credential headers so a key
// the agent read from a mounted .env cannot leave via those headers. That blocks
// the common exfil path but is not absolute on its own — a secret re-encoded into
// a request body evades header-strip; the egress DLP scan (internal/egresspolicy)
// is the complementary layer, and neither is a hard guarantee.
//
// This package is intentionally stdlib-only (operates on *http.Request) so the
// security-critical classification and header logic is unit-testable without
// the proxy runtime. The martian adapter lives in internal/egressproxy.
// SPEC: _spec/_paradigms/credential-boundary.puml
package broker

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// DefaultStripHeaders are removed from every off-provider request when the
// broker is active and no explicit strip list is configured.
var DefaultStripHeaders = []string{
	"authorization",
	"x-api-key",
	"x-goog-api-key",
	"api-key",
	"proxy-authorization",
}

// Config declares how the broker treats requests. All fields are optional; an
// empty Config yields an inert broker (Apply is a no-op), so loading the broker
// unconditionally is safe.
type Config struct {
	// Hosts are provider domain suffixes (e.g. ".anthropic.com"). A request to
	// one of these is the pinned provider: inject (if a value is available) or
	// pass the agent's own credential through — never stripped.
	Hosts []string
	// Header is the auth header to set on the provider host (e.g. "x-api-key").
	Header string
	// Query, if set, is a query-param name to set on the provider host (e.g.
	// Gemini "key").
	Query string
	// Value is the secret to inject (may include a "Bearer " prefix). When set it
	// takes precedence over ValueFile — the proxy uses this after resolving the
	// provider registry against a mounted secret env-file.
	Value string
	// ValueFile is the path to a 0600 file holding the secret (may include a
	// "Bearer " prefix). Mounted outside every agent mount. Read once. Used only
	// when Value is empty.
	ValueFile string
	// Strip lists credential headers removed off-provider. Defaults to
	// DefaultStripHeaders when empty *and* injection is configured.
	Strip []string
}

// Broker applies the inject/strip policy to requests.
type Broker struct {
	hosts       []string
	header      string
	query       string
	value       string
	strip       []string
	injectReady bool
}

// New builds a Broker from cfg, reading the secret file if present. It never
// returns the secret in an error. A missing/empty value file is not an error:
// the broker degrades to strip-off-provider + pass-through-on-provider.
func New(cfg Config) (*Broker, error) {
	b := &Broker{
		header: strings.TrimSpace(cfg.Header),
		query:  strings.TrimSpace(cfg.Query),
	}
	for _, h := range cfg.Hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			b.hosts = append(b.hosts, h)
		}
	}
	for _, s := range cfg.Strip {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			b.strip = append(b.strip, s)
		}
	}
	switch {
	case cfg.Value != "":
		b.value = cfg.Value
	case cfg.ValueFile != "":
		v, err := readSecret(cfg.ValueFile)
		if err != nil {
			return nil, fmt.Errorf("broker: reading credential file: %w", err)
		}
		b.value = v
	}
	b.injectReady = len(b.hosts) > 0 && b.value != "" && (b.header != "" || b.query != "")
	// Strip off-provider whenever a provider is configured — even with no value to
	// inject (degrade to strip + pass-through, as documented). Gating this on
	// injectReady left a key the agent read elsewhere free to leave via a header.
	if len(b.hosts) > 0 && len(b.strip) == 0 {
		b.strip = append(b.strip, DefaultStripHeaders...)
	}
	return b, nil
}

// InjectReady reports whether the broker will inject a credential (host list,
// target header/query, and a secret value are all present).
func (b *Broker) InjectReady() bool { return b.injectReady }

// Active reports whether the broker does anything at all.
func (b *Broker) Active() bool { return b.injectReady || len(b.strip) > 0 }

// Apply mutates req in place: inject on the pinned-provider host, strip
// credential headers off-provider. Safe to call on every request.
func (b *Broker) Apply(req *http.Request) {
	if b.isProviderHost(hostOf(req)) {
		// Pinned provider: inject the brokered credential when we hold one,
		// otherwise leave the agent's own credential untouched. NEVER strip
		// here — the provider must keep its auth.
		if b.injectReady {
			if b.header != "" {
				req.Header.Set(b.header, b.value)
			}
			if b.query != "" {
				q := req.URL.Query()
				q.Set(b.query, b.value)
				req.URL.RawQuery = q.Encode()
			}
		}
		return
	}
	// Off-provider: never let a credential header leave.
	for _, name := range b.strip {
		req.Header.Del(name)
	}
}

func (b *Broker) isProviderHost(host string) bool {
	host = strings.ToLower(host)
	for _, suffix := range b.hosts {
		bare := strings.TrimPrefix(suffix, ".")
		if host == bare || strings.HasSuffix(host, "."+bare) {
			return true
		}
	}
	return false
}

// hostOf returns the request's target hostname without port, tolerating both
// server-side (req.Host) and proxy-side (req.URL.Host) request shapes.
func hostOf(req *http.Request) string {
	h := req.Host
	if h == "" && req.URL != nil {
		h = req.URL.Host
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

func readSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Trim only trailing newline(s); preserve any internal characters.
	return strings.TrimRight(string(data), "\r\n"), nil
}
