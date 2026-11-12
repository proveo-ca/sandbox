// SPEC: _spec/internal/sbx/host-inference.puml, _spec/internal/ui/output-vocabulary.puml
package sbx

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/ui"
)

// pullServer streams three progress events and a success from a fake host Ollama.
func pullServer(t *testing.T, last string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			http.NotFound(w, r)
			return
		}
		for _, done := range []int{1, 2, 3} {
			fmt.Fprintf(w, `{"status":"pulling","total":4294967296,"completed":%d}`+"\n", done<<30)
		}
		fmt.Fprintln(w, last)
	}))
	t.Cleanup(srv.Close)
	orig, every := hostOllamaURL, pullFrameEvery
	t.Cleanup(func() { hostOllamaURL, pullFrameEvery = orig, every })
	hostOllamaURL, pullFrameEvery = srv.URL, 0
}

func TestAPullWritesNoProgressFrameIntoAPlainStream(t *testing.T) {
	pullServer(t, `{"status":"success"}`)
	var out bytes.Buffer
	if _, err := pullOllama("qwen3", ui.New(&out)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.ContainsAny(got, "\r\x1b") || strings.Contains(got, "pulling qwen3") {
		t.Errorf("a stream that is not a terminal received a progress frame: %q", got)
	}
}

func TestAPullsStartAndEndReachTheLogAndNotTheScreen(t *testing.T) {
	pullServer(t, `{"status":"success"}`)
	var term, log bytes.Buffer
	defer ui.TeeTo(&log)()
	pulled, err := pullOllama("qwen3", ui.NewTerminal(&term, 120))
	if err != nil {
		t.Fatal(err)
	}
	if pulled != 4294967296 {
		t.Errorf("pulled = %d, want the pull's total", pulled)
	}
	if !strings.Contains(term.String(), "pulling qwen3") || strings.Contains(term.String(), "pulled qwen3") {
		t.Errorf("the terminal holds the live frame and no end line: %q", term.String())
	}
	if !strings.Contains(log.String(), "pulled qwen3 (4.0 GiB)") || strings.Contains(log.String(), "pulling qwen3:") {
		t.Errorf("the run log holds the end and never a frame: %q", log.String())
	}
}

func TestAPullsSizeAddsUpEveryLayer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		for _, ev := range []struct {
			digest      string
			total, done int
		}{{"sha256:a", 4 << 30, 1 << 30}, {"sha256:a", 4 << 30, 4 << 30}, {"sha256:b", 1 << 30, 1 << 30}, {"sha256:c", 512, 512}} {
			fmt.Fprintf(w, `{"status":"pulling %s","digest":"%s","total":%d,"completed":%d}`+"\n", ev.digest, ev.digest, ev.total, ev.done)
		}
		fmt.Fprintln(w, `{"status":"success"}`)
	}))
	t.Cleanup(srv.Close)
	orig := hostOllamaURL
	t.Cleanup(func() { hostOllamaURL = orig })
	hostOllamaURL = srv.URL
	pulled, err := pullOllama("qwen3", ui.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(5<<30 + 512); pulled != want {
		t.Errorf("pulled = %d, want %d: every layer once, not the largest", pulled, want)
	}
}

func TestAFailedPullReturnsItsErrorAndLeavesNoFrame(t *testing.T) {
	pullServer(t, `{"error":"manifest unknown"}`)
	var out bytes.Buffer
	_, err := pullOllama("qwen3", ui.New(&out))
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("pullOllama = %v, want the server's error", err)
	}
	if strings.ContainsAny(out.String(), "\r\x1b") {
		t.Errorf("a failed pull left control bytes behind: %q", out.String())
	}
}
