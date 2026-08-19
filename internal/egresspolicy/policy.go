// Package egresspolicy is the stdlib-only egress policy core for firewall mode.
// SPEC: _spec/internal/egresspolicy/egress-policy-overview.puml, _spec/internal/egresspolicy/egress-policy-components.puml, _spec/internal/egresspolicy/egress-policy-layers.puml, _spec/internal/egresspolicy/egress-policy-decide.puml, _spec/_conventions/design-decision-ids.puml
package egresspolicy

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Reasons a request is blocked (empty => allowed).
const (
	ReasonSink   = "sink"                  // host is a known exfil sink (all methods)
	ReasonReview = "review-denied"         // the operator declined this connection (review tier)
	ReasonWrite  = "write-not-allowlisted" // write method to a non-allowlisted host
	ReasonSecret = "secret"                // a credential/secret was found in the URL or body
	ReasonBudget = "budget"                // outbound byte budget to a non-allowlisted host exceeded
)

// Decision is the outcome of Decide.
type Decision struct {
	Allow  bool
	Reason string // "" when allowed; one of the Reason* constants otherwise
}

// Config declares the policy: read-allow / write-deny, DLP detectors off where empty.
type Config struct {
	ProviderHosts []string
	// WriteHosts are suffixes where write methods (POST/PUT/PATCH/DELETE) are allowed
	// and which are exempt from the outbound byte budget.
	WriteHosts []string
	// DenySinks are suffixes hard-denied for ALL methods (exfil sinks).
	DenySinks     []string
	OpenNetwork   bool
	ReviewConnect func(host, port string) bool
	// Secrets are exact secret values scanned for off-provider (URL + body). Values
	// shorter than minSecretLen are ignored.
	Secrets []string
	// BlockKnownSecrets enables the generic secret-shape patterns (sk-, AKIA, ...).
	BlockKnownSecrets bool
	DecodeScan        bool
	BlockEntropy      bool
	// MaxOutBytesPerHost caps cumulative (query+body) bytes to a non-allowlisted host
	// over the policy's lifetime. 0 => unlimited.
	MaxOutBytesPerHost int64
}

// Policy is the compiled, concurrency-safe enforcer.
type Policy struct {
	providerHosts []string
	writeHosts    []string
	denySinks     []string
	scanner       *scanner
	maxBytes      int64
	openNetwork   bool
	reviewConnect func(host, port string) bool

	mu        sync.Mutex
	outByHost map[string]int64
}

// maxBodyScan caps the request body we buffer for scanning. The full body is
// still buffered+restored for forwarding; a streaming cap is a Phase-2 concern.
const maxBodyScan = 1 << 20 // 1 MiB

// New compiles cfg into a Policy.
func New(cfg Config) *Policy {
	return &Policy{
		providerHosts: normHosts(cfg.ProviderHosts),
		writeHosts:    normHosts(cfg.WriteHosts),
		denySinks:     normHosts(cfg.DenySinks),
		scanner:       newScanner(cfg.Secrets, cfg.BlockKnownSecrets, cfg.DecodeScan, cfg.BlockEntropy),
		maxBytes:      cfg.MaxOutBytesPerHost,
		openNetwork:   cfg.OpenNetwork,
		reviewConnect: cfg.ReviewConnect,
		outByHost:     map[string]int64{},
	}
}

// Decide evaluates req and reports whether to allow it. It may read and restore
// req.Body (bounded) to scan for secrets; the body stays readable downstream.
func (p *Policy) Decide(req *http.Request) Decision {
	host := hostOf(req)

	// On-provider: the broker owns this host. Allow untouched, no DLP, no budget.
	if matchHost(host, p.providerHosts) {
		return Decision{Allow: true}
	}
	// B: exfil-sink denylist (all methods, including the CONNECT tunnel).
	if matchHost(host, p.denySinks) {
		return Decision{Reason: ReasonSink}
	}
	if req.Method == http.MethodConnect {
		if matchHost(host, p.writeHosts) {
			return Decision{Allow: true}
		}
		if p.reviewConnect != nil && !p.reviewConnect(host, portOf(req)) {
			return Decision{Reason: ReasonReview}
		}
		return Decision{Allow: true}
	}
	allowlisted := p.openNetwork || matchHost(host, p.writeHosts)
	// A: write methods only to the write-allowlist.
	if !isReadMethod(req.Method) && !allowlisted {
		return Decision{Reason: ReasonWrite}
	}
	// C(1): DLP secret scan over URL (raw + decoded), headers, and body.
	scan, bodyLen := peekBody(req)
	if p.scanner.active() {
		uri := req.URL.RequestURI()
		dec, _ := url.QueryUnescape(uri)
		if p.scanner.hit(uri) || p.scanner.hit(dec) || p.scanHeaders(req) || p.scanner.hit(string(scan)) {
			return Decision{Reason: ReasonSecret}
		}
	}
	if p.maxBytes > 0 && !allowlisted {
		if p.charge(host, int64(len(req.URL.RequestURI()))+bodyLen) {
			return Decision{Reason: ReasonBudget}
		}
	}
	return Decision{Allow: true}
}

func (p *Policy) scanHeaders(req *http.Request) bool {
	for _, vals := range req.Header {
		for _, v := range vals {
			if p.scanner.hit(v) {
				return true
			}
		}
	}
	return false
}

func (p *Policy) charge(host string, n int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outByHost[host] += n
	return p.outByHost[host] > p.maxBytes
}

func peekBody(req *http.Request) (scan []byte, fullLen int64) {
	if req.Body == nil {
		return nil, 0
	}
	orig := req.Body
	head, err := io.ReadAll(io.LimitReader(orig, maxBodyScan))
	if err != nil {
		// Read error: forward the prefix we have; drop the (unreadable) rest.
		_ = orig.Close()
		req.Body = io.NopCloser(bytes.NewReader(head))
		return head, int64(len(head))
	}
	// Reattach: scanned prefix first, then the still-open original stream (its
	// cursor now sits just past the prefix). The tail is never buffered.
	req.Body = &prefixBody{prefix: bytes.NewReader(head), rest: orig}
	if req.ContentLength >= 0 {
		return head, req.ContentLength
	}
	return head, int64(len(head))
}

// prefixBody serves an already-read prefix, then the remainder of the original
// body — reconstructing the full stream for forwarding without buffering the tail.
type prefixBody struct {
	prefix *bytes.Reader
	rest   io.ReadCloser
}

func (b *prefixBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	return b.rest.Read(p)
}

func (b *prefixBody) Close() error { return b.rest.Close() }

func matchHost(host string, suffixes []string) bool {
	host = strings.ToLower(host)
	for _, s := range suffixes {
		bare := strings.TrimPrefix(s, ".")
		if host == bare || strings.HasSuffix(host, "."+bare) {
			return true
		}
	}
	return false
}

func isReadMethod(m string) bool {
	switch strings.ToUpper(m) {
	case "GET", "HEAD", "OPTIONS", "TRACE":
		return true
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

func normHosts(in []string) []string {
	var out []string
	for _, h := range in {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func portOf(req *http.Request) string {
	if _, port, err := net.SplitHostPort(req.URL.Host); err == nil && port != "" {
		return port
	}
	if req.URL.Scheme == "http" {
		return "80"
	}
	return "443"
}
