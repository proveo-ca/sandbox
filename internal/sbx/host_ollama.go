// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	HostOllamaGuestBase  = "http://host.docker.internal:11434"
	HostOllamaPolicyHost = "localhost:11434"
	EnvLocalModelForce   = "PROVEO_LOCAL_MODEL_FORCE"
	EnvLocalModelPull    = "PROVEO_LOCAL_MODEL_PULL"
)

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

// EnsureHostOllama checks the host Ollama serves model, pulling it when missing, and that loading it fits in memory.
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
		if off(getenv(EnvLocalModelPull)) {
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
	if loaded, err := listOllama("/api/ps"); err == nil {
		if _, ok := matchTag(loaded, model); ok {
			return nil
		}
	}
	return checkFit(m, getenv)
}

func off(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
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
// memory by the same estimate EnsureHostOllama refuses on; nil when it fits or
// cannot be judged.
func LocalModelFits(model string, getenv func(string) string) error {
	tags, err := listOllama("/api/tags")
	if err != nil {
		return nil
	}
	m, ok := matchTag(tags, model)
	if !ok {
		return nil
	}
	return checkFit(m, getenv)
}

func checkFit(m ollamaModel, getenv func(string) string) error {
	need := m.Size + m.Size/8 + 1<<30
	avail, known := availableMemory()
	if !known || need <= avail {
		return nil
	}
	if v := strings.ToLower(strings.TrimSpace(getenv(EnvLocalModelForce))); v == "1" || v == "true" {
		return nil
	}
	return fmt.Errorf("%s needs ~%.1f GiB (%.1f GiB weights + cache headroom) and the host has %.1f GiB available — "+
		"loading it would push the host into swap; free memory, pick a smaller tag, or set %s=1",
		m.Name, gib(need), gib(m.Size), gib(avail), EnvLocalModelForce)
}

func hostAvailableMemory() (uint64, bool) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("vm_stat").Output()
		if err != nil {
			return 0, false
		}
		return parseVMStat(string(out))
	case "linux":
		b, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, false
		}
		return parseMemInfo(string(b))
	}
	return 0, false
}

var (
	vmPageSize = regexp.MustCompile(`page size of (\d+) bytes`)
	vmLine     = regexp.MustCompile(`^(Pages [a-z]+|"[^"]+"|[A-Za-z ]+):\s+(\d+)\.`)
)

// parseVMStat sums the pages macOS can hand out without swapping: free, inactive, speculative, purgeable.
func parseVMStat(out string) (uint64, bool) {
	pm := vmPageSize.FindStringSubmatch(out)
	if pm == nil {
		return 0, false
	}
	page, _ := strconv.ParseUint(pm[1], 10, 64)
	var pages uint64
	seen := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := vmLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if f == nil {
			continue
		}
		switch f[1] {
		case "Pages free", "Pages inactive", "Pages speculative", "Pages purgeable":
			n, _ := strconv.ParseUint(f[2], 10, 64)
			pages += n
			seen = true
		}
	}
	return pages * page, seen
}

func parseMemInfo(out string) (uint64, bool) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "MemAvailable:" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return kb * 1024, true
		}
	}
	return 0, false
}

// HostModelRef reports whether base points the agent at the host's Ollama.
func HostModelRef(base string) bool {
	return strings.TrimRight(base, "/") == HostOllamaGuestBase
}
