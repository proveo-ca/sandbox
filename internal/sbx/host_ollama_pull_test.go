// SPEC: _spec/internal/sbx/host-inference.puml, _spec/internal/ui/output-vocabulary.puml
package sbx

import (
	"bytes"
	"fmt"
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
	if err := pullOllama("qwen3", ui.New(&out)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.ContainsAny(got, "\r\x1b") || strings.Contains(got, "pulling qwen3") {
		t.Errorf("a stream that is not a terminal received a progress frame: %q", got)
	}
	if !strings.Contains(got, "pulled qwen3") {
		t.Errorf("the pull's result line is missing: %q", got)
	}
}

func TestAFailedPullReturnsItsErrorAndLeavesNoFrame(t *testing.T) {
	pullServer(t, `{"error":"manifest unknown"}`)
	var out bytes.Buffer
	err := pullOllama("qwen3", ui.New(&out))
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("pullOllama = %v, want the server's error", err)
	}
	if strings.ContainsAny(out.String(), "\r\x1b") {
		t.Errorf("a failed pull left control bytes behind: %q", out.String())
	}
}
