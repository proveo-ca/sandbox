package egresspolicy

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func newReq(t *testing.T, method, rawurl, body string) *http.Request {
	t.Helper()
	var b io.Reader
	if body != "" {
		b = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, rawurl, b)
	if err != nil {
		t.Fatalf("newReq(%s %s): %v", method, rawurl, err)
	}
	return req
}

func tableCfg() Config {
	return Config{
		ProviderHosts:     []string{".anthropic.com"},
		WriteHosts:        []string{".anthropic.com", "api.github.com"},
		DenySinks:         []string{"webhook.site", ".ngrok.io", "pastebin.com"},
		Secrets:           []string{"sk-ant-SECRETKEY-123456", "sekret/with+chars=9999"},
		BlockKnownSecrets: true,
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		method     string
		url        string
		body       string
		wantAllow  bool
		wantReason string
	}{
		{"read anywhere off-provider", "GET", "https://docs.example.com/guide", "", true, ""},
		{"read to sink blocked", "GET", "https://webhook.site/abc123", "", false, ReasonSink},
		{"read to sink subdomain blocked", "GET", "https://x.ngrok.io/collect", "", false, ReasonSink},
		{"HEAD is a read", "HEAD", "https://docs.example.com/", "", true, ""},
		{"OPTIONS is a read", "OPTIONS", "https://docs.example.com/", "", true, ""},
		{"write off-allowlist blocked", "POST", "https://docs.example.com/x", "hi", false, ReasonWrite},
		{"PUT off-allowlist blocked", "PUT", "https://docs.example.com/x", "hi", false, ReasonWrite},
		{"write to allowlisted host allowed", "POST", "https://api.github.com/repos", "{}", true, ""},
		{"write to provider allowed even with secret", "POST", "https://api.anthropic.com/v1/messages", "auth sk-ant-SECRETKEY-123456", true, ""},
		{"exact secret in query blocked", "GET", "https://docs.example.com/?d=sk-ant-SECRETKEY-123456", "", false, ReasonSecret},
		{"url-encoded secret in query blocked", "GET", "https://docs.example.com/?x=sekret%2Fwith%2Bchars%3D9999", "", false, ReasonSecret},
		{"known pattern in query blocked", "GET", "https://docs.example.com/?t=ghp_ABCDEFGHIJKLMNOPQRSTUVWX", "", false, ReasonSecret},
		{"secret in body to allowlisted host still blocked", "POST", "https://api.github.com/x", "key=sk-ant-SECRETKEY-123456", false, ReasonSecret},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(tableCfg())
			got := p.Decide(newReq(t, tc.method, tc.url, tc.body))
			if got.Allow != tc.wantAllow || got.Reason != tc.wantReason {
				t.Errorf("Decide() = {allow:%v reason:%q}, want {allow:%v reason:%q}",
					got.Allow, got.Reason, tc.wantAllow, tc.wantReason)
			}
		})
	}
}

func TestDecideBodyRestored(t *testing.T) {
	t.Parallel()
	p := New(Config{WriteHosts: []string{"api.github.com"}, Secrets: []string{"unrelated-secret"}, BlockKnownSecrets: true})
	req := newReq(t, "POST", "https://api.github.com/x", "the original body")
	if d := p.Decide(req); !d.Allow {
		t.Fatalf("expected allow, got %+v", d)
	}
	// The scanner read the body; downstream must still see it intact.
	rest, _ := io.ReadAll(req.Body)
	if string(rest) != "the original body" {
		t.Errorf("body not restored after scan: got %q", rest)
	}
}

func TestDecideLargeBodyStreamsPastScanWindow(t *testing.T) {
	t.Parallel()
	const secret = "sk-ant-SECRETKEY-123456"

	// (a) large clean body → allowed, and reads back byte-for-byte (nothing dropped).
	big := strings.Repeat("A", maxBodyScan+4096)
	req := newReq(t, "POST", "https://api.github.com/x", big)
	if d := New(tableCfg()).Decide(req); !d.Allow {
		t.Fatalf("large clean body: expected allow, got %+v", d)
	}
	if rest, _ := io.ReadAll(req.Body); string(rest) != big {
		t.Errorf("large body not streamed intact: got %d bytes, want %d", len(rest), len(big))
	}

	// (b) secret within the scan window → blocked.
	within := secret + strings.Repeat("A", maxBodyScan)
	if d := New(tableCfg()).Decide(newReq(t, "POST", "https://api.github.com/x", within)); d.Reason != ReasonSecret {
		t.Errorf("secret within scan window: got %+v, want ReasonSecret", d)
	}

	// (c) secret only beyond the window → not scanned (bounded), but still forwarded.
	beyond := strings.Repeat("A", maxBodyScan) + secret
	rq := newReq(t, "POST", "https://api.github.com/x", beyond)
	if d := New(tableCfg()).Decide(rq); !d.Allow {
		t.Errorf("secret beyond scan window: expected allow (bounded scan), got %+v", d)
	}
	if got, _ := io.ReadAll(rq.Body); string(got) != beyond {
		t.Errorf("beyond-window body not streamed intact: got %d bytes, want %d", len(got), len(beyond))
	}
}

func TestBudget(t *testing.T) {
	t.Parallel()

	t.Run("cumulative query+body over budget blocks non-allowlisted", func(t *testing.T) {
		t.Parallel()
		p := New(Config{MaxOutBytesPerHost: 20})
		first := p.Decide(newReq(t, "GET", "https://x.com/?q=abcdefghij", "")) // RequestURI "/?q=abcdefghij" = 14
		if !first.Allow {
			t.Fatalf("first request (14 <= 20) should allow, got %+v", first)
		}
		second := p.Decide(newReq(t, "GET", "https://x.com/?q=abcdefghij", "")) // 28 > 20
		if second.Allow || second.Reason != ReasonBudget {
			t.Errorf("cumulative 28 > 20 should block budget, got %+v", second)
		}
	})

	t.Run("allowlisted host is exempt from budget", func(t *testing.T) {
		t.Parallel()
		p := New(Config{MaxOutBytesPerHost: 5, WriteHosts: []string{"y.com"}})
		d := p.Decide(newReq(t, "POST", "https://y.com/?q=waytoolongforbudget", "and-a-big-body-too"))
		if !d.Allow {
			t.Errorf("allowlisted host must be budget-exempt, got %+v", d)
		}
	})

	t.Run("provider host is exempt from everything", func(t *testing.T) {
		t.Parallel()
		p := New(Config{ProviderHosts: []string{"api.anthropic.com"}, MaxOutBytesPerHost: 1, Secrets: []string{"sk-ant-SECRETKEY-123456"}, BlockKnownSecrets: true})
		d := p.Decide(newReq(t, "POST", "https://api.anthropic.com/v1/messages", strings.Repeat("x", 10_000)+"sk-ant-SECRETKEY-123456"))
		if !d.Allow {
			t.Errorf("provider host must be exempt, got %+v", d)
		}
	})
}

func TestDecideConnect(t *testing.T) {
	t.Parallel()
	p := New(Config{WriteHosts: []string{"api.github.com"}, DenySinks: []string{"webhook.site"}})
	if d := p.Decide(newReq(t, "CONNECT", "https://example.com:443", "")); !d.Allow {
		t.Errorf("CONNECT to a normal host must be allowed, got %+v", d)
	}
	if d := p.Decide(newReq(t, "CONNECT", "https://webhook.site:443", "")); d.Allow || d.Reason != ReasonSink {
		t.Errorf("CONNECT to a sink must be blocked, got %+v", d)
	}
}

func TestDecideScansHeaders(t *testing.T) {
	t.Parallel()
	p := New(Config{Secrets: []string{"sk-ant-SECRETKEY-123456"}, WriteHosts: []string{"docs.example.com"}})
	// A secret smuggled in a header to an off-provider host must be blocked, even
	// though the broker only strips a fixed set of header NAMES.
	req := newReq(t, "GET", "https://docs.example.com/guide", "")
	req.Header.Set("X-Custom", "Bearer sk-ant-SECRETKEY-123456")
	if d := p.Decide(req); d.Allow || d.Reason != ReasonSecret {
		t.Errorf("secret in a header must block, got %+v", d)
	}
}

func TestWriteDenyByDefault(t *testing.T) {
	t.Parallel()
	// Zero config is fail-closed: reads allowed anywhere (broad scraping), all
	// off-provider writes denied.
	p := New(Config{})
	if d := p.Decide(newReq(t, "GET", "https://anywhere.example/doc?q=x", "")); !d.Allow {
		t.Errorf("reads must be allowed by default, got %+v", d)
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		if d := p.Decide(newReq(t, m, "https://anywhere.example/", "body")); d.Allow || d.Reason != ReasonWrite {
			t.Errorf("write %s must be denied by default, got %+v", m, d)
		}
	}
	// The provider host is always writable — the broker owns it.
	pp := New(Config{ProviderHosts: []string{"api.anthropic.com"}})
	if d := pp.Decide(newReq(t, "POST", "https://api.anthropic.com/v1/messages", "{}")); !d.Allow {
		t.Errorf("provider write must be allowed, got %+v", d)
	}
}

// Race detector coverage for the budget map under concurrent Decide calls.
func TestConcurrentDecide(t *testing.T) {
	t.Parallel()
	p := New(Config{MaxOutBytesPerHost: 1 << 30, DenySinks: []string{"webhook.site"}})
	reqs := make([]*http.Request, 64) // build on the test goroutine (no t.* in workers)
	for i := range reqs {
		reqs[i] = newReq(t, "GET", "https://x.com/?q=abcdef", "")
	}
	var wg sync.WaitGroup
	for _, r := range reqs {
		wg.Add(1)
		go func(r *http.Request) {
			defer wg.Done()
			p.Decide(r)
		}(r)
	}
	wg.Wait()
}
