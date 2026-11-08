// SPEC: _spec/internal/sbx/host-inference.puml (CONTEXT FLOOR)
package sbx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// ContextTag is the host tag that serves model with a ctx-token window; model itself when ctx is 0.
func ContextTag(model string, ctx int) string {
	if ctx <= 0 {
		return model
	}
	suffix := fmt.Sprintf("-ctx%dk", ctx/1024)
	full := withTag(model)
	if strings.HasSuffix(full, suffix) {
		return model
	}
	return full + suffix
}

// EnsureContextTag creates ContextTag(model, ctx) on the host's Ollama from model with
// PARAMETER num_ctx ctx; the tag shares model's blobs.
func EnsureContextTag(model string, ctx int) (string, error) {
	tag := ContextTag(model, ctx)
	if tag == model {
		return model, nil
	}
	body, _ := json.Marshal(map[string]any{
		"model": tag, "from": model, "parameters": map[string]int{"num_ctx": ctx}, "stream": false,
	})
	c := http.Client{Timeout: time.Minute}
	resp, err := c.Post(hostOllamaURL+"/api/create", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	switch {
	case out.Error != "":
		return "", fmt.Errorf("create %s: %s", tag, out.Error)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("create %s: HTTP %d", tag, resp.StatusCode)
	}
	return tag, nil
}

// contextCache is the f16 K+V cache ctx tokens of model cost on the host's Ollama.
func contextCache(model string, ctx int) (uint64, bool) {
	if ctx <= 0 {
		return 0, false
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Post(hostOllamaURL+"/api/show", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var show struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if json.NewDecoder(resp.Body).Decode(&show) != nil {
		return 0, false
	}
	per, ok := kvBytesPerToken(show.ModelInfo)
	return per * uint64(ctx), ok
}

// kvBytesPerToken reads one token's f16 K+V size from Ollama's model_info:
// attention layers × KV heads × (key + value width) × 2 bytes.
func kvBytesPerToken(info map[string]any) (uint64, bool) {
	arch, _ := info["general.architecture"].(string)
	num := func(key string) float64 {
		v, _ := info[arch+"."+key].(float64)
		return v
	}
	keyLen := num("attention.key_length")
	if heads := num("attention.head_count"); keyLen == 0 && heads > 0 {
		keyLen = num("embedding_length") / heads
	}
	valLen := num("attention.value_length")
	if valLen == 0 {
		valLen = keyLen
	}
	var kvHeads float64
	switch v := info[arch+".attention.head_count_kv"].(type) {
	case float64:
		layers := num("block_count")
		if every := num("full_attention_interval"); every > 1 {
			layers = math.Floor(layers / every)
		}
		kvHeads = layers * v
	case []any:
		for _, h := range v {
			n, _ := h.(float64)
			kvHeads += n
		}
	}
	if arch == "" || kvHeads <= 0 || keyLen <= 0 {
		return 0, false
	}
	return uint64(kvHeads * (keyLen + valLen) * 2), true
}
