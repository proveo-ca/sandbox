// SPEC: _spec/internal/sbx/host-inference.puml (CONTEXT FLOOR)
package sbx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// qwen35Info is qwen3.8:latest's /api/show model_info, as Ollama 0.40.13 reports it.
const qwen35Info = `{"general.architecture":"qwen35","qwen35.block_count":65,"qwen35.full_attention_interval":4,
 "qwen35.attention.head_count":24,"qwen35.attention.head_count_kv":4,
 "qwen35.attention.key_length":256,"qwen35.attention.value_length":256,"qwen35.embedding_length":5120}`

func decodeInfo(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestContextTagNamesTheDerivedHostTag(t *testing.T) {
	for _, c := range []struct {
		model string
		ctx   int
		want  string
	}{
		{"qwen3.8:latest", 65536, "qwen3.8:latest-ctx64k"},
		{"qwen3.8", 65536, "qwen3.8:latest-ctx64k"},
		{"hf.co/a/b:Q8_0", 65536, "hf.co/a/b:Q8_0-ctx64k"},
		{"qwen3.8:latest-ctx64k", 65536, "qwen3.8:latest-ctx64k"},
		{"qwen3.8:latest", 0, "qwen3.8:latest"},
	} {
		if got := ContextTag(c.model, c.ctx); got != c.want {
			t.Errorf("ContextTag(%q, %d) = %q, want %q", c.model, c.ctx, got, c.want)
		}
	}
}

func TestKVBytesPerTokenCountsOnlyAttentionLayers(t *testing.T) {
	for _, c := range []struct {
		name, info string
		want       uint64
		ok         bool
	}{
		// 65/4 = 16 attention layers × 4 KV heads × (256+256) × 2 B; matches the 80 MiB the
		// RTX 3070 host logged for 5 of them at 4,096 tokens.
		{"hybrid qwen35", qwen35Info, 16 * 4 * 512 * 2, true},
		{"width from embedding/heads", `{"general.architecture":"muse_glimmer","muse_glimmer.block_count":52,
		  "muse_glimmer.attention.head_count":32,"muse_glimmer.attention.head_count_kv":2,"muse_glimmer.embedding_length":6656}`,
			52 * 2 * 416 * 2, true},
		{"per-layer KV heads", `{"general.architecture":"x","x.attention.head_count_kv":[8,0,8],"x.attention.key_length":128}`,
			16 * 256 * 2, true},
		{"MLX tag without attention metadata", `{"general.architecture":"qwen3_5","qwen3_5.block_count":64}`, 0, false},
	} {
		got, ok := kvBytesPerToken(decodeInfo(t, c.info))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: kvBytesPerToken = %d, %v; want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// fakeContextOllama serves qwen3.8:latest with qwen35Info and records /api/create bodies.
func fakeContextOllama(t *testing.T, createStatus int, createBody string) *[]map[string]any {
	t.Helper()
	var creates []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(hostTags))
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/show":
			_, _ = w.Write([]byte(`{"model_info":` + qwen35Info + `}`))
		case "/api/create":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			creates = append(creates, body)
			w.WriteHeader(createStatus)
			_, _ = w.Write([]byte(createBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	orig := hostOllamaURL
	t.Cleanup(func() { hostOllamaURL = orig })
	hostOllamaURL = srv.URL
	return &creates
}

func TestEnsureContextTagDerivesTheModelWithItsWindow(t *testing.T) {
	creates := fakeContextOllama(t, http.StatusOK, `{"status":"success"}`)
	got, err := EnsureContextTag("qwen3.8:latest", 65536)
	if err != nil || got != "qwen3.8:latest-ctx64k" {
		t.Fatalf("EnsureContextTag = %q, %v", got, err)
	}
	if len(*creates) != 1 {
		t.Fatalf("want one /api/create, got %v", *creates)
	}
	c := (*creates)[0]
	params, _ := c["parameters"].(map[string]any)
	if c["model"] != "qwen3.8:latest-ctx64k" || c["from"] != "qwen3.8:latest" || params["num_ctx"] != float64(65536) {
		t.Errorf("create body = %v; Ollama's /v1 honours a Modelfile num_ctx, not a request option", c)
	}
	if got, err := EnsureContextTag("qwen3.8:latest", 0); err != nil || got != "qwen3.8:latest" || len(*creates) != 1 {
		t.Errorf("no floor must leave the model alone: %q, %v, creates %d", got, err, len(*creates))
	}
}

func TestEnsureContextTagReportsOllamasRefusal(t *testing.T) {
	fakeContextOllama(t, http.StatusBadRequest, `{"error":"model not found"}`)
	if _, err := EnsureContextTag("qwen3.8:latest", 65536); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("err = %v, want Ollama's reason", err)
	}
}

func TestTheFitCheckCountsTheHarnessContext(t *testing.T) {
	fakeContextOllama(t, http.StatusOK, `{"status":"success"}`)
	stubPool(t, 7740.0/1024, poolVRAM, true)
	err := EnsureHostOllama("qwen3.8:latest", 65536, noEnv, nil)
	var nofit *NoFitError
	if !errors.As(err, &nofit) {
		t.Fatalf("want a NoFitError, got %v", err)
	}
	const weights = 17741872154
	if want := uint64(weights + 4<<30 + 1<<30); nofit.Need != want || nofit.Cache != 4<<30 || nofit.Context != 65536 {
		t.Errorf("need %d cache %d ctx %d; want %d = weights + 4 GiB (64K of qwen35 K+V) + 1 GiB", nofit.Need, nofit.Cache, nofit.Context, want)
	}
	if b := nofit.Breakdown(); !strings.Contains(b, "4.0 GiB for a 65,536-token context") {
		t.Errorf("breakdown = %q", b)
	}
	if c := nofit.Consequence(); !strings.Contains(c, "~35% on the GPU") {
		t.Errorf("consequence = %q", c)
	}
}
