package egressproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecorderNeverLogsSecret is the regression guard for recorder.go's core
// invariant: the flow log records host/method/path + status/reason, but NEVER
// the query string (which can carry an injected/brokered secret) — for both the
// observed and blocked record paths.
func TestRecorderNeverLogsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flows.ndjson")
	rec, err := NewRecorder(path)
	if err != nil {
		t.Fatal(err)
	}

	obs := httptest.NewRequest("GET", "https://api.example.com/v1/msg?key=SUPERSECRET&a=1", nil)
	if err := rec.ModifyResponse(&http.Response{StatusCode: 200, Request: obs}); err != nil {
		t.Fatal(err)
	}
	blk := httptest.NewRequest("POST", "https://evil.example/collect?token=SUPERSECRET", nil)
	rec.RecordBlock(blk, "secret")
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SUPERSECRET") {
		t.Fatalf("flow log LEAKED the secret query value:\n%s", raw)
	}

	var recs []flowRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var fr flowRecord
		if err := json.Unmarshal([]byte(line), &fr); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", line, err)
		}
		recs = append(recs, fr)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}

	o := recs[0]
	if o.Decision != "observed" || o.Method != "GET" || o.Host != "api.example.com" ||
		o.Port != "443" || o.Path != "/v1/msg" || o.Status != "200" {
		t.Errorf("observed record wrong: %+v", o)
	}
	b := recs[1]
	if b.Decision != "blocked" || b.Reason != "secret" || b.Method != "POST" ||
		b.Host != "evil.example" || b.Port != "443" || b.Path != "/collect" {
		t.Errorf("blocked record wrong: %+v", b)
	}
}

func TestRecorderDefaultPorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flows.ndjson")
	rec, _ := NewRecorder(path)
	_ = rec.ModifyResponse(&http.Response{StatusCode: 204, Request: httptest.NewRequest("GET", "http://plain.example/x", nil)})
	_ = rec.ModifyResponse(&http.Response{StatusCode: 200, Request: httptest.NewRequest("GET", "https://h.example:8443/y", nil)})
	_ = rec.Close()

	raw, _ := os.ReadFile(path)
	var ports []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var fr flowRecord
		if err := json.Unmarshal([]byte(line), &fr); err != nil {
			t.Fatal(err)
		}
		ports = append(ports, fr.Port)
	}
	if len(ports) != 2 || ports[0] != "80" || ports[1] != "8443" {
		t.Errorf("ports = %v, want [80 8443] (http default / explicit)", ports)
	}
}

// A nil Recorder (no FlowsPath) must be a safe no-op on both paths.
func TestRecorderNilNoop(t *testing.T) {
	var rec *Recorder
	if err := rec.ModifyResponse(&http.Response{StatusCode: 200, Request: httptest.NewRequest("GET", "https://x/", nil)}); err != nil {
		t.Errorf("nil ModifyResponse err = %v", err)
	}
	rec.RecordBlock(httptest.NewRequest("GET", "https://x/", nil), "sink") // must not panic
	if err := rec.Close(); err != nil {
		t.Errorf("nil Close err = %v", err)
	}
}
