// SPEC: _spec/internal/egressproxy/mitm-and-flow-record.puml, _spec/defs/claudecode/claudecode-egress-topology.puml
//
// Package egressproxy is the Go egress inspection proxy (TLS-terminating MITM).
package egressproxy

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/martian/v3"
	"github.com/google/martian/v3/mitm"
	"github.com/google/martian/v3/proxyutil"

	"github.com/proveo-ca/proveo/internal/broker"
	"github.com/proveo-ca/proveo/internal/egresspolicy"
)

// Config is the proxy's runtime configuration (populated from env in main).
type Config struct {
	Listen      string        // e.g. ":8888"
	UpstreamURL string        // Squid, e.g. "http://squid:3128" (empty => direct)
	CACertOut   string        // path to write the generated CA cert PEM (agent trusts it)
	FlowsPath   string        // NDJSON flow log (empty => no recording)
	CAName      string        // CA common name
	CAOrg       string        // CA organization
	CAValidity  time.Duration // CA validity
	Broker      broker.Config // credential broker policy

	// EnforcePolicy attaches the read-allow/write-deny/DLP policy (fixes S1). It
	// is the destination/method/content gate the broker + recorder alone lack.
	EnforcePolicy bool
	Policy        egresspolicy.Config
}

// brokerModifier adapts the stdlib-only broker to martian's RequestModifier.
type brokerModifier struct{ b *broker.Broker }

func (m brokerModifier) ModifyRequest(req *http.Request) error {
	m.b.Apply(req)
	return nil
}

type policyModifier struct {
	pol *egresspolicy.Policy
	rec *Recorder
}

func (m policyModifier) ModifyRequest(req *http.Request) error {
	d := m.pol.Decide(req)
	if d.Allow {
		return nil
	}
	m.rec.RecordBlock(req, d.Reason)

	ctx := martian.NewContext(req)
	if ctx == nil { // no in-flight context (e.g. unit tests): fall back to the logged error
		return fmt.Errorf("egress policy: blocked (%s)", d.Reason)
	}
	ctx.SkipRoundTrip() // never contact the upstream, even if the hijack below fails

	conn, brw, err := ctx.Session().Hijack()
	if err != nil {
		return fmt.Errorf("egress policy: blocked (%s)", d.Reason)
	}
	// martian closes conn after we return (Hijack contract); we just write + flush.
	_ = conn
	res := proxyutil.NewResponse(http.StatusForbidden,
		strings.NewReader("egress policy: blocked ("+d.Reason+")\n"), req)
	res.Header.Set("Content-Type", "text/plain; charset=utf-8")
	res.Close = true
	_ = res.Write(brw)
	_ = brw.Flush()
	return nil
}

// reqChain runs request modifiers in order, stopping at the first error.
type reqChain []martian.RequestModifier

func (c reqChain) ModifyRequest(req *http.Request) error {
	for _, m := range c {
		if err := m.ModifyRequest(req); err != nil {
			return err
		}
	}
	return nil
}

func build(cfg Config) (*martian.Proxy, *broker.Broker, func(), error) {
	b, err := broker.New(cfg.Broker)
	if err != nil {
		return nil, nil, nil, err
	}

	ca, priv, err := mitm.NewAuthority(orDefault(cfg.CAName, "Proveo Egress CA"),
		orDefault(cfg.CAOrg, "Proveo"), orDefaultDur(cfg.CAValidity, 365*24*time.Hour))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("egressproxy: generate CA: %w", err)
	}
	if cfg.CACertOut != "" {
		if err := writeCACert(cfg.CACertOut, ca); err != nil {
			return nil, nil, nil, fmt.Errorf("egressproxy: write CA cert: %w", err)
		}
	}
	mc, err := mitm.NewConfig(ca, priv)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("egressproxy: mitm config: %w", err)
	}

	p := martian.NewProxy()
	p.SetMITM(mc)
	if cfg.UpstreamURL != "" {
		u, err := url.Parse(cfg.UpstreamURL)
		if err != nil {
			p.Close()
			return nil, nil, nil, fmt.Errorf("egressproxy: bad upstream %q: %w", cfg.UpstreamURL, err)
		}
		p.SetDownstreamProxy(u)
	}
	rec, err := NewRecorder(cfg.FlowsPath)
	if err != nil {
		p.Close()
		return nil, nil, nil, fmt.Errorf("egressproxy: open flow log: %w", err)
	}

	// Request pipeline: broker (inject/strip) then policy (destination/method/DLP).
	// Ordered so the broker's provider-host injection is never DLP-flagged.
	var reqMods []martian.RequestModifier
	if b.Active() {
		reqMods = append(reqMods, brokerModifier{b})
	}
	if cfg.EnforcePolicy {
		reqMods = append(reqMods, policyModifier{pol: egresspolicy.New(cfg.Policy), rec: rec})
	}
	switch len(reqMods) {
	case 1:
		p.SetRequestModifier(reqMods[0])
	default:
		if len(reqMods) > 1 {
			p.SetRequestModifier(reqChain(reqMods))
		}
	}
	if rec != nil {
		p.SetResponseModifier(rec)
	}
	closer := func() {
		p.Close()
		if rec != nil {
			_ = rec.Close()
		}
	}
	return p, b, closer, nil
}

// Run builds and serves the proxy until the listener is closed.
func Run(cfg Config) error {
	p, b, closer, err := build(cfg)
	if err != nil {
		return err
	}
	defer closer()

	l, err := net.Listen("tcp", orDefault(cfg.Listen, ":8888"))
	if err != nil {
		return fmt.Errorf("egressproxy: listen %q: %w", cfg.Listen, err)
	}
	brokerState := "inert"
	if b.InjectReady() {
		brokerState = "inject+strip"
	} else if b.Active() {
		brokerState = "strip-only"
	}
	policyState := "off"
	if cfg.EnforcePolicy {
		policyState = "read-allow/write-deny/dlp"
	}
	fmt.Fprintf(os.Stderr, "🚀 proveo-egress on %s → upstream %q (MITM on, broker=%s, policy=%s)\n",
		l.Addr(), cfg.UpstreamURL, brokerState, policyState)
	return p.Serve(l)
}

func writeCACert(path string, ca *x509.Certificate) error {
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	return os.WriteFile(path, pemBytes, 0o644)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func orDefaultDur(v, d time.Duration) time.Duration {
	if v == 0 {
		return d
	}
	return v
}
