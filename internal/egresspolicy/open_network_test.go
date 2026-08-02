package egresspolicy

import (
	"net/http"
	"testing"
)

func TestOpenNetworkDropsWritePinKeepsDLP(t *testing.T) {
	mk := func(open bool) *Policy {
		return New(Config{WriteHosts: []string{".anthropic.com"}, DenySinks: DefaultSinks, Secrets: []string{"sk-supersecretvalue-123"}, OpenNetwork: open})
	}
	post, _ := http.NewRequest("POST", "https://random-host.example/collect", nil)
	if d := mk(false).Decide(post); d.Allow {
		t.Error("allowlist tier must block a write to an unknown host")
	}
	if d := mk(true).Decide(post); !d.Allow {
		t.Errorf("open tier must allow it, got %q", d.Reason)
	}
	leak, _ := http.NewRequest("POST", "https://random-host.example/?k=sk-supersecretvalue-123", nil)
	if d := mk(true).Decide(leak); d.Allow {
		t.Error("open tier must STILL block a secret leak")
	}
}
