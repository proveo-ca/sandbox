// SPEC: _spec/internal/egressproxy/mitm-and-flow-record.puml
package egressproxy

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

type flowRecord struct {
	TS       string `json:"ts"`
	Source   string `json:"source"`
	Decision string `json:"decision"`
	Protocol string `json:"protocol"`
	Method   string `json:"method"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Path     string `json:"path"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

// Recorder is a martian ResponseModifier that appends one NDJSON line per flow.
type Recorder struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// NewRecorder opens path for append. A nil Recorder is a valid no-op.
func NewRecorder(path string) (*Recorder, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	return &Recorder{f: f, enc: json.NewEncoder(f)}, nil
}

// ModifyResponse records the completed flow. It reads only method/host/port/path
// and status — never headers, body, or query string.
func (r *Recorder) ModifyResponse(res *http.Response) error {
	if r == nil || res == nil || res.Request == nil {
		return nil
	}
	u := res.Request.URL
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	rec := flowRecord{
		TS:       time.Now().UTC().Format(time.RFC3339),
		Source:   "proveo-egress",
		Decision: "observed",
		Protocol: u.Scheme,
		Method:   res.Request.Method,
		Host:     u.Hostname(),
		Port:     port,
		Path:     u.Path, // query intentionally omitted — may carry injected secret
		Status:   itoa(res.StatusCode),
		Reason:   "egress_flow",
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enc.Encode(&rec) // Encoder appends '\n'
}

func (r *Recorder) RecordBlock(req *http.Request, reason string) {
	if r == nil || req == nil || req.URL == nil {
		return
	}
	u := req.URL
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	rec := flowRecord{
		TS:       time.Now().UTC().Format(time.RFC3339),
		Source:   "proveo-egress",
		Decision: "blocked",
		Protocol: u.Scheme,
		Method:   req.Method,
		Host:     u.Hostname(),
		Port:     port,
		Path:     u.Path, // query intentionally omitted — may carry a secret
		Reason:   reason,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.enc.Encode(&rec)
}

// Close flushes and closes the underlying file.
func (r *Recorder) Close() error {
	if r == nil || r.f == nil {
		return nil
	}
	return r.f.Close()
}

func itoa(n int) string {
	if n == 0 {
		return ""
	}
	// small non-alloc-heavy int->string without importing strconv widely
	buf := [12]byte{}
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
