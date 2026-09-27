// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	HostOllamaGuestBase  = "http://host.docker.internal:11434"
	HostOllamaPolicyHost = "localhost:11434"
	EnvLocalModelForce   = "PROVEO_LOCAL_MODEL_FORCE"
)

var (
	hostOllamaURL   = "http://127.0.0.1:11434"
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

// EnsureHostOllama checks the host Ollama serves model and that loading it fits in memory.
func EnsureHostOllama(model string, getenv func(string) string) error {
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
		return fmt.Errorf("the host's Ollama has no %q — `ollama pull %s`%s", model, model, hint)
	}
	if loaded, err := listOllama("/api/ps"); err == nil {
		if _, ok := matchTag(loaded, model); ok {
			return nil
		}
	}
	return checkFit(m, getenv)
}

func gib(b uint64) float64 { return float64(b) / (1 << 30) }

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
