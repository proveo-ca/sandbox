// Package broker is the credential broker for the enforced egress modes.
//
// SPEC: _spec/_paradigms/credential-boundary.puml
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

type Route struct {
	Provider  string
	Hosts     []string
	Header    string
	Query     string
	Value     string
	ValueFile string
}

type Config struct {
	Routes []Route
	Strip  []string
}

// Broker applies the inject/strip policy to requests.
type Broker struct {
	routes []route
	strip  []string
}

type route struct {
	provider string
	hosts    []string
	header   string
	query    string
	value    string
	ready    bool
}

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
// in configuration order.
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
// broker treats as on-route.
func (b *Broker) Hosts() []string {
	var out []string
	for i := range b.routes {
		out = append(out, b.routes[i].hosts...)
	}
	return out
}

// Apply mutates req in place: inject on the matching provider's hosts, strip
// credential headers everywhere else.
func (b *Broker) Apply(req *http.Request) {
	if rt := b.match(hostOf(req)); rt != nil {
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
	for _, name := range b.strip {
		req.Header.Del(name)
	}
}

func (b *Broker) onRoute(host string) bool { return b.match(host) != nil }

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
	return strings.TrimRight(string(data), "\r\n"), nil
}
