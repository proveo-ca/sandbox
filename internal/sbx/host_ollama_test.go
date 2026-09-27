// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const vmStatM4Pro = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                              1000.
Pages active:                          944322.
Pages inactive:                         2000.
Pages speculative:                       300.
Pages throttled:                              0.
Pages wired down:                       198061.
Pages purgeable:                          40.
"Translation faults":                2106416542.
`

func fakeOllama(t *testing.T, tags, ps string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(tags))
		case "/api/ps":
			_, _ = w.Write([]byte(ps))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	orig := hostOllamaURL
	t.Cleanup(func() { hostOllamaURL = orig })
	hostOllamaURL = srv.URL
}

func stubAvailable(t *testing.T, gibs float64, known bool) {
	t.Helper()
	orig := availableMemory
	t.Cleanup(func() { availableMemory = orig })
	availableMemory = func() (uint64, bool) { return uint64(gibs * (1 << 30)), known }
}

const hostTags = `{"models":[
 {"name":"qwen3.8:latest","size":17741872154},
 {"name":"qwen3.8:27b-mlx","size":18174721847},
 {"name":"hf.co/NAKSTStudio/chess-gemma-commentary:Q8_0","size":291546646}]}`

func noEnv(string) string { return "" }

func TestEnsureHostOllamaNamesTheInstalledTagsWhenOneIsMissing(t *testing.T) {
	fakeOllama(t, hostTags, `{"models":[]}`)
	stubAvailable(t, 40, true)
	noPull := func(k string) string {
		if k == EnvLocalModelPull {
			return "0"
		}
		return ""
	}
	err := EnsureHostOllama("qwen3.8:27b", noPull, nil)
	if err == nil {
		t.Fatal("qwen3.8:27b is not installed and pulling is off; want a refusal before the sandbox starts")
	}
	for _, want := range []string{"ollama pull qwen3.8:27b", "qwen3.8:27b-mlx", "qwen3.8:latest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestEnsureHostOllamaTreatsAnUntaggedNameAsLatest(t *testing.T) {
	fakeOllama(t, hostTags, `{"models":[]}`)
	stubAvailable(t, 40, true)
	if err := EnsureHostOllama("qwen3.8", noEnv, nil); err != nil {
		t.Errorf("qwen3.8 ⇒ qwen3.8:latest is installed: %v", err)
	}
}

func TestEnsureHostOllamaRefusesAModelThatWouldSwap(t *testing.T) {
	fakeOllama(t, hostTags, `{"models":[]}`)
	stubAvailable(t, 12, true)
	err := EnsureHostOllama("qwen3.8:latest", noEnv, nil)
	if err == nil || !strings.Contains(err.Error(), EnvLocalModelForce) {
		t.Fatalf("16.5 GiB of weights with 12 GiB available must refuse and name the override, got %v", err)
	}

	force := func(k string) string {
		if k == EnvLocalModelForce {
			return "1"
		}
		return ""
	}
	if err := EnsureHostOllama("qwen3.8:latest", force, nil); err != nil {
		t.Errorf("%s=1 must override the fit check: %v", EnvLocalModelForce, err)
	}
}

func TestEnsureHostOllamaSkipsTheFitCheckForALoadedModel(t *testing.T) {
	fakeOllama(t, hostTags, `{"models":[{"name":"qwen3.8:latest","size":20000000000}]}`)
	stubAvailable(t, 2, true)
	if err := EnsureHostOllama("qwen3.8:latest", noEnv, nil); err != nil {
		t.Errorf("an already-resident model costs no new memory: %v", err)
	}
}

func TestEnsureHostOllamaPassesWhenMemoryIsUnknown(t *testing.T) {
	fakeOllama(t, hostTags, `{"models":[]}`)
	stubAvailable(t, 0, false)
	if err := EnsureHostOllama("qwen3.8:latest", noEnv, nil); err != nil {
		t.Errorf("no reading (windows, freebsd) must not block: %v", err)
	}
}

func TestEnsureHostOllamaExplainsAMissingServer(t *testing.T) {
	orig := hostOllamaURL
	t.Cleanup(func() { hostOllamaURL = orig })
	hostOllamaURL = "http://127.0.0.1:1"
	err := EnsureHostOllama("qwen3.8:latest", noEnv, nil)
	if err == nil || !strings.Contains(err.Error(), "ollama.com/download") {
		t.Errorf("want an install pointer, got %v", err)
	}
}

func TestParseVMStatCountsReclaimablePages(t *testing.T) {
	got, ok := parseVMStat(vmStatM4Pro)
	if want := uint64(1000+2000+300+40) * 16384; !ok || got != want {
		t.Errorf("parseVMStat = %d, %v; want free+inactive+speculative+purgeable = %d", got, ok, want)
	}
	if _, ok := parseVMStat("garbage"); ok {
		t.Error("no page size line ⇒ unknown")
	}
}

func TestParseMemInfoReadsMemAvailable(t *testing.T) {
	got, ok := parseMemInfo("MemTotal:       65536000 kB\nMemFree:  1000 kB\nMemAvailable:   32768000 kB\n")
	if !ok || got != 32768000*1024 {
		t.Errorf("parseMemInfo = %d, %v", got, ok)
	}
	if _, ok := parseMemInfo("MemTotal: 1 kB\n"); ok {
		t.Error("no MemAvailable ⇒ unknown")
	}
}

func TestHostModelRefMatchesOnlyTheOllamaBase(t *testing.T) {
	for base, want := range map[string]bool{
		"http://host.docker.internal:11434":  true,
		"http://host.docker.internal:11434/": true,
		"http://ollama:11434":                false,
		"":                                   false,
	} {
		if got := HostModelRef(base); got != want {
			t.Errorf("HostModelRef(%q) = %v, want %v", base, got, want)
		}
	}
}

// fakePullOllama serves tags that gain model once /api/pull has streamed, and a registry manifest of size bytes.
func fakePullOllama(t *testing.T, model string, size uint64, pullErr string) *int {
	t.Helper()
	pulls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if pulls > 0 && pullErr == "" {
				_, _ = fmt.Fprintf(w, `{"models":[{"name":%q,"size":%d}]}`, model, size)
				return
			}
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/pull":
			pulls++
			_, _ = fmt.Fprintf(w, "{\"status\":\"pulling manifest\"}\n{\"status\":\"pulling x\",\"total\":%d,\"completed\":%d}\n", size, size/2)
			if pullErr != "" {
				_, _ = fmt.Fprintf(w, "{\"error\":%q}\n", pullErr)
				return
			}
			_, _ = w.Write([]byte("{\"status\":\"success\"}\n"))
		default:
			if strings.HasPrefix(r.URL.Path, "/v2/") {
				_, _ = fmt.Fprintf(w, `{"config":{"size":1},"layers":[{"size":%d}]}`, size-1)
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	origHost, origReg := hostOllamaURL, ollamaRegistry
	t.Cleanup(func() { hostOllamaURL, ollamaRegistry = origHost, origReg })
	hostOllamaURL, ollamaRegistry = srv.URL, srv.URL
	return &pulls
}

func TestEnsureHostOllamaPullsAMissingTag(t *testing.T) {
	pulls := fakePullOllama(t, "muse-glimmer:30b-mlx", 19053621992, "")
	stubAvailable(t, 40, true)
	var out strings.Builder
	if err := EnsureHostOllama("muse-glimmer:30b-mlx", noEnv, &out); err != nil {
		t.Fatalf("a missing tag must be pulled, not refused: %v", err)
	}
	if *pulls != 1 || !strings.Contains(out.String(), "pulled muse-glimmer:30b-mlx") {
		t.Errorf("pulls=%d progress=%q", *pulls, out.String())
	}
}

func TestEnsureHostOllamaPullsButWillNotLoadWhatCannotFit(t *testing.T) {
	pulls := fakePullOllama(t, "muse-glimmer:30b-mlx", 19053621992, "")
	stubAvailable(t, 12, true)
	var out strings.Builder
	err := EnsureHostOllama("muse-glimmer:30b-mlx", noEnv, &out)
	if *pulls != 1 || !strings.Contains(out.String(), "would not fit in memory right now") {
		t.Errorf("memory is transient and the download is not: pull with a warning; pulls=%d out=%q", *pulls, out.String())
	}
	if err == nil || !strings.Contains(err.Error(), EnvLocalModelForce) {
		t.Errorf("the load check must still refuse 19 GB with 12 GiB free, got %v", err)
	}
}

func TestEnsureHostOllamaReportsAFailedPull(t *testing.T) {
	fakePullOllama(t, "nope:1", 1000, "pull model manifest: file does not exist")
	stubAvailable(t, 40, true)
	err := EnsureHostOllama("nope:1", noEnv, nil)
	if err == nil || !strings.Contains(err.Error(), "file does not exist") {
		t.Errorf("want Ollama's own error, got %v", err)
	}
}

func TestLibraryRefCoversOllamaNamesOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"muse-glimmer:30b-mlx": "library/muse-glimmer 30b-mlx",
		"qwen3.8":              "library/qwen3.8 latest",
		"someone/model:q4":     "someone/model q4",
	} {
		repo, tag, ok := libraryRef(in)
		if got := repo + " " + tag; !ok || got != want {
			t.Errorf("libraryRef(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, _, ok := libraryRef("hf.co/unsloth/Qwen3-GGUF:Q4_K_M"); ok {
		t.Error("a Hugging Face ref has no Ollama registry manifest; its size must read as unknown")
	}
}
