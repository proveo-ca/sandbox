// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	HostOllamaGuestBase  = "http://host.docker.internal:11434"
	HostOllamaPolicyHost = "localhost:11434"
	EnvLocalModelForce   = "PROVEO_LOCAL_MODEL_FORCE"
	EnvLocalModelPull    = "PROVEO_LOCAL_MODEL_PULL"
	EnvLocalModelAuto    = "PROVEO_LOCAL_MODEL_AUTO"
)

// LocalModelEnv points every harness dialect at model on the host's Ollama, as the sandbox reaches it.
func LocalModelEnv(model string) []string {
	base := HostOllamaGuestBase
	return []string{
		"PROVEO_LOCAL_MODEL=" + model,
		"OLLAMA_HOST=" + base, "OLLAMA_API_BASE=" + base,
		"OPENAI_BASE_URL=" + base + "/v1", "OPENAI_API_BASE=" + base + "/v1",
		"OPENAI_API_KEY=ollama",
		"ARCHITECT_MODEL=ollama/" + model, "EDITOR_MODEL=ollama/" + model,
		"SMALL_MODEL=ollama/" + model,
		"ANTHROPIC_BASE_URL=" + base, "ANTHROPIC_AUTH_TOKEN=ollama",
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_MODEL=" + model, "ANTHROPIC_SMALL_FAST_MODEL=" + model,
		"CLAUDE_CODE_EFFORT_LEVEL=low", "CLAUDE_CODE_DISABLE_THINKING=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"API_TIMEOUT_MS=1200000", "CLAUDE_STREAM_FIRST_BYTE_TIMEOUT_MS=1200000",
	}
}

var (
	hostOllamaURL   = "http://127.0.0.1:11434"
	ollamaRegistry  = "https://registry.ollama.ai"
	availableMemory = hostAvailableMemory
)

type ollamaModel struct {
	Name string `json:"name"`
	Size uint64 `json:"size"`
}

func listOllama(path string) ([]ollamaModel, error) {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(hostOllamaURL + path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	var body struct {
		Models []ollamaModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return body.Models, nil
}

// matchTag finds want among models; an untagged name means ":latest", as in Ollama.
func matchTag(models []ollamaModel, want string) (ollamaModel, bool) {
	full := want
	if !strings.Contains(want[strings.LastIndex(want, "/")+1:], ":") {
		full = want + ":latest"
	}
	for _, m := range models {
		if m.Name == want || m.Name == full {
			return m, true
		}
	}
	return ollamaModel{}, false
}

func sameRepo(models []ollamaModel, want string) []string {
	repo := want
	if i := strings.LastIndex(want, ":"); i > strings.LastIndex(want, "/") {
		repo = want[:i]
	}
	var out []string
	for _, m := range models {
		if strings.HasPrefix(m.Name, repo+":") {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

// EnsureHostOllama checks the host Ollama serves model, pulling it when missing,
// and returns a *NoFitError when loading it would overflow the host's free memory.
func EnsureHostOllama(model string, getenv func(string) string, p *ui.Printer) error {
	tags, err := listOllama("/api/tags")
	if err != nil {
		return fmt.Errorf("no Ollama answers on 127.0.0.1:11434 (%v) — local models on sbx run on the host's Ollama: "+
			"install it from https://ollama.com/download and start it", err)
	}
	m, ok := matchTag(tags, model)
	if !ok {
		hint := ""
		if near := sameRepo(tags, model); len(near) > 0 {
			hint = "; installed: " + strings.Join(near, ", ")
		}
		if envflag.Off(getenv(EnvLocalModelPull)) {
			return fmt.Errorf("the host's Ollama has no %q — `ollama pull %s`%s", model, model, hint)
		}
		if p == nil {
			p = ui.New(io.Discard)
		}
		if size, known := registrySize(model); known {
			p.Asyncf("%s is not in the host's Ollama — pulling %.1f GiB", model, gib(size))
			if err := checkFit(ollamaModel{Name: model, Size: size}, getenv); err != nil {
				p.Warnf("it would not fit in memory right now (%v); pulling anyway, the load check below decides", err)
			}
		} else {
			p.Asyncf("%s is not in the host's Ollama — pulling", model)
		}
		if err := pullOllama(model, p); err != nil {
			return fmt.Errorf("pull %s into the host's Ollama: %w%s", model, err, hint)
		}
		if tags, err = listOllama("/api/tags"); err != nil {
			return err
		}
		if m, ok = matchTag(tags, model); !ok {
			return fmt.Errorf("pulled %s but the host's Ollama does not list it", model)
		}
	}
	if resident(model) {
		return nil
	}
	return checkFit(m, getenv)
}

// libraryRef splits an Ollama library tag into repo and tag; ok is false for other registries.
func libraryRef(model string) (repo, tag string, ok bool) {
	repo, tag = model, "latest"
	if i := strings.LastIndex(model, ":"); i > strings.LastIndex(model, "/") {
		repo, tag = model[:i], model[i+1:]
	}
	if first, _, nested := strings.Cut(repo, "/"); nested && strings.ContainsAny(first, ".:") {
		return "", "", false
	}
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return repo, tag, true
}

// registrySize is the download size of model from the Ollama registry manifest.
func registrySize(model string) (uint64, bool) {
	repo, tag, ok := libraryRef(model)
	if !ok {
		return 0, false
	}
	req, err := http.NewRequest(http.MethodGet, ollamaRegistry+"/v2/"+repo+"/manifests/"+tag, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var man struct {
		Config struct {
			Size uint64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size uint64 `json:"size"`
		} `json:"layers"`
	}
	if json.NewDecoder(resp.Body).Decode(&man) != nil {
		return 0, false
	}
	total := man.Config.Size
	for _, l := range man.Layers {
		total += l.Size
	}
	return total, total > 0
}

// pullOllama streams the host Ollama's /api/pull; a TTY gets a live progress line, anything else the start and end.
func pullOllama(model string, p *ui.Printer) error {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	resp, err := http.Post(hostOllamaURL+"/api/pull", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var last time.Time
	drawn := false
	dec := json.NewDecoder(resp.Body)
	for {
		var ev struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Total     uint64 `json:"total"`
			Completed uint64 `json:"completed"`
		}
		if err := dec.Decode(&ev); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		switch {
		case ev.Error != "":
			if drawn {
				_, _ = fmt.Fprintln(p.W)
			}
			return errors.New(ev.Error)
		case ev.Total > 0 && !p.Plain && time.Since(last) > time.Second:
			last, drawn = time.Now(), true
			_, _ = fmt.Fprintf(p.W, "\r%s● %s  pulling %s: %5.1f%% of %.1f GiB", ui.ANSI(ui.ColorAsync), ui.ANSIReset, model,
				100*float64(ev.Completed)/float64(ev.Total), gib(ev.Total))
		case ev.Status == "success":
			if drawn {
				_, _ = fmt.Fprint(p.W, "\r\033[K")
			}
			p.Okf("pulled %s", model)
		}
	}
	return nil
}

func gib(b uint64) float64 { return float64(b) / (1 << 30) }

// LocalModelFits reports whether an installed host model fits the host's free
// memory by the same estimate EnsureHostOllama reports on; nil when it fits or
// cannot be judged.
func LocalModelFits(model string, getenv func(string) string) error {
	tags, err := listOllama("/api/tags")
	if err != nil {
		return nil
	}
	m, ok := matchTag(tags, model)
	if !ok || resident(model) {
		return nil
	}
	return checkFit(m, getenv)
}

// resident reports whether the host's Ollama already holds model in memory.
func resident(model string) bool {
	loaded, err := listOllama("/api/ps")
	if err != nil {
		return false
	}
	_, ok := matchTag(loaded, model)
	return ok
}

// NoFitError reports a model larger than the free host memory Ollama would load it into.
type NoFitError struct {
	Model               string
	Need, Weights, Free uint64
	Pool                string
}

func (e *NoFitError) Error() string {
	return fmt.Sprintf("%s needs ~%.1f GiB (%.1f GiB weights + cache headroom) and the host has %.1f GiB of %s free — "+
		"%s; free memory, pick a smaller tag, or set %s=1",
		e.Model, gib(e.Need), gib(e.Weights), gib(e.Free), e.Pool, e.Consequence(), EnvLocalModelForce)
}

// Consequence names what loading the model anyway costs.
func (e *NoFitError) Consequence() string {
	if e.Pool == poolVRAM {
		return "Ollama runs the overflow on the CPU, several times slower"
	}
	return "loading it pushes the host into swap"
}

// FittingHostModels keeps the candidates the host's Ollama has installed and
// whose load fits the free memory Ollama would use, in candidate order.
func FittingHostModels(candidates []string, getenv func(string) string) []string {
	tags, err := listOllama("/api/tags")
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range candidates {
		if m, ok := matchTag(tags, c); ok && (resident(c) || checkFit(m, getenv) == nil) {
			out = append(out, c)
		}
	}
	return out
}

func checkFit(m ollamaModel, getenv func(string) string) error {
	need := m.Size + m.Size/8 + 1<<30
	mem, known := availableMemory()
	if !known || need <= mem.Free {
		return nil
	}
	if envflag.On(getenv(EnvLocalModelForce)) {
		return nil
	}
	return &NoFitError{Model: m.Name, Need: need, Weights: m.Size, Free: mem.Free, Pool: mem.Pool}
}

// HostModelRef reports whether base points the agent at the host's Ollama.
func HostModelRef(base string) bool {
	return strings.TrimRight(base, "/") == HostOllamaGuestBase
}
