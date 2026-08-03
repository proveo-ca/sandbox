// Package broker is the credential broker for the enforced egress modes. It
// imports omnigent's credential_proxy principle — "inject keys, never expose" —
// adapted to the fact that the *vendor CLI*, not this harness, makes the model
// call. Real provider secrets are confined to the egress proxy (this process);
// on a provider's own hosts it injects that provider's auth header, and on every
// other host it strips the known credential headers so a key the agent read from
// a mounted .env cannot leave via those headers. That blocks the common exfil
// path but is not absolute on its own — a secret re-encoded into a request body
// evades header-strip; the egress DLP scan (internal/egresspolicy) is the
// complementary layer, and neither is a hard guarantee.
//
// The broker holds a ROUTE PER PROVIDER. A single pinned provider could not
// serve a session whose roles span vendors (ARCHITECT_MODEL on one, EDITOR_MODEL
// on another): whichever provider was pinned worked and the rest received the
// "proveo-brokered" sentinel, which the harness reports as an invalid API key.
//
// This package is intentionally stdlib-only (operates on *http.Request) so the
// security-critical classification and header logic is unit-testable without
// the proxy runtime. The martian adapter lives in internal/egressproxy.
// SPEC: _spec/_paradigms/credential-boundary.puml, _spec/_plans/multi-provider-broker.puml
package broker

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// DefaultStripHeaders are removed from every off-route request when the broker
// is active and no explicit strip list is configured.
var DefaultStripHeaders = []string{
	"authorization",
	"x-api-key",
	"x-goog-api-key",
	"api-key",
	"proxy-authorization",
}

// Route is one provider's injection rule. A request is matched to at most one
// route by destination host, and only that route's secret can be injected — a
// provider's key is never attached to a request bound for a different provider.
type Route struct {
	// Provider is the registry name, used only for reporting. Never a secret.
	Provider string
	// Hosts are this provider's domain suffixes (e.g. ".anthropic.com"). A
	// request to one of these is on-route: inject (if a value is available) or
	// pass the agent's own credential through — never stripped.
	Hosts []string
	// Header is the auth header to set on these hosts (e.g. "x-api-key").
	Header string
	// Query, if set, is a query-param name to set instead of a header (e.g.
	// Gemini "key").
	Query string
	// Value is the secret to inject (may include a "Bearer " prefix). Takes
	// precedence over ValueFile.
	Value string
	// ValueFile is the path to a 0600 file holding the secret (may include a
	// "Bearer " prefix). Mounted outside every agent mount. Read once. Used
	// only when Value is empty.
	ValueFile string
}

// Config declares how the broker treats requests. All fields are optional; an
// empty Config yields an inert broker (Apply is a no-op), so loading the broker
// unconditionally is safe.
type Config struct {
	// Routes is one entry per broker-injectable provider, in registry order.
	Routes []Route
	// Strip lists credential headers removed off-route. Defaults to
	// DefaultStripHeaders when empty *and* at least one route is configured.
	Strip []string
}

// Broker applies the inject/strip policy to requests.
type Broker struct {
	routes []route
	strip  []string
}

// route is a validated Route with its secret resolved.
type route struct {
	provider string
	hosts    []string
	header   string
	query    string
	value    string
	ready    bool
}

// New builds a Broker from cfg, reading each route's secret file if present. It
// never returns a secret in an error. A missing/empty value file is not an
// error: that route degrades to strip-off-route + pass-through-on-route.
func New(cfg Config) (*Broker, error) {
	b := &Broker{}
	for _, r := range cfg.Routes {
		rt := route{
			provider: strings.TrimSpace(r.Provider),
			header:   strings.TrimSpace(r.Header),
			query:    strings.TrimSpace(r.Query),
		}
		for _, h := range r.Hosts {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				rt.hosts = append(rt.hosts, h)
			}
		}
		if len(rt.hosts) == 0 {
			continue // a route with no destination can never match
		}
		switch {
		case r.Value != "":
			rt.value = r.Value
		case r.ValueFile != "":
			v, err := readSecret(r.ValueFile)
			if err != nil {
				return nil, fmt.Errorf("broker: reading credential file for %s: %w", rt.provider, err)
			}
			rt.value = v
		}
		rt.ready = rt.value != "" && (rt.header != "" || rt.query != "")
		b.routes = append(b.routes, rt)
	}
	for _, s := range cfg.Strip {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			b.strip = append(b.strip, s)
		}
	}
	// Strip off-route whenever any route is configured — even with no value to
	// inject (degrade to strip + pass-through, as documented). Gating this on
	// readiness left a key the agent read elsewhere free to leave via a header.
	if len(b.routes) > 0 && len(b.strip) == 0 {
		b.strip = append(b.strip, DefaultStripHeaders...)
	}
	return b, nil
}

// InjectReady reports whether any route will inject a credential.
func (b *Broker) InjectReady() bool {
	for i := range b.routes {
		if b.routes[i].ready {
			return true
		}
	}
	return false
}

// Active reports whether the broker does anything at all.
func (b *Broker) Active() bool { return len(b.routes) > 0 || len(b.strip) > 0 }

// InjectingProviders names the providers this broker holds a usable secret for,
// in configuration order. Names only — safe to log.
func (b *Broker) InjectingProviders() []string {
	var out []string
	for i := range b.routes {
		if b.routes[i].ready {
			out = append(out, b.routes[i].provider)
		}
	}
	return out
}

// Hosts is every host suffix across all routes: the set of destinations the
// broker treats as on-route. Used to seed the policy's provider hosts.
func (b *Broker) Hosts() []string {
	var out []string
	for i := range b.routes {
		out = append(out, b.routes[i].hosts...)
	}
	return out
}

// Apply mutates req in place: inject on the matching provider's hosts, strip
// credential headers everywhere else. Safe to call on every request.
func (b *Broker) Apply(req *http.Request) {
	if rt := b.match(hostOf(req)); rt != nil {
		// On-route: inject this provider's brokered credential when we hold one,
		// otherwise leave the agent's own credential untouched. NEVER strip here —
		// the provider must keep its auth.
		if rt.ready {
			if rt.header != "" {
				req.Header.Set(rt.header, rt.value)
			}
			if rt.query != "" {
				q := req.URL.Query()
				q.Set(rt.query, rt.value)
				req.URL.RawQuery = q.Encode()
			}
		}
		return
	}
	// Off-route: never let a credential header leave.
	for _, name := range b.strip {
		req.Header.Del(name)
	}
}

// onRoute reports whether any route owns host — i.e. whether a request there is
// on-route and therefore exempt from header stripping.
func (b *Broker) onRoute(host string) bool { return b.match(host) != nil }

// match returns the first route owning host, or nil. First-match is deliberate:
// registry hosts are disjoint, and if a future entry ever overlapped, the
// earlier (more specific, registry-ordered) route must win rather than the
// choice depending on map iteration order.
func (b *Broker) match(host string) *route {
	host = strings.ToLower(host)
	for i := range b.routes {
		for _, suffix := range b.routes[i].hosts {
			bare := strings.TrimPrefix(suffix, ".")
			if host == bare || strings.HasSuffix(host, "."+bare) {
				return &b.routes[i]
			}
		}
	}
	return nil
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
