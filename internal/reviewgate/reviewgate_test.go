package reviewgate

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCachesOneAnswerPerHost(t *testing.T) {
	t.Parallel()
	var calls int
	g := New(func(host, port string) bool { calls++; return true })
	for i := 0; i < 5; i++ {
		if got := g.Decide("api.example", "443"); got != Allow {
			t.Fatalf("verdict %q, want allow", got)
		}
	}
	if calls != 1 {
		t.Errorf("asked %d times for one host, want 1", calls)
	}
}

func TestNilAskerDeniesEverything(t *testing.T) {
	t.Parallel()
	if got := New(nil).Decide("api.example", "443"); got != Deny {
		t.Errorf("with no way to consent the verdict must be deny, got %q", got)
	}
}

func TestTimeoutFailsClosed(t *testing.T) {
	t.Parallel()
	g := New(func(string, string) bool { time.Sleep(2 * time.Second); return true })
	g.Deadline = 50 * time.Millisecond
	start := time.Now()
	if got := g.Decide("slow.example", "443"); got != Deny {
		t.Errorf("verdict %q, want deny on timeout", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Decide blocked %v, must return at the deadline", elapsed)
	}
}

// Concurrent CONNECTs to one new host must raise a single prompt.
func TestConcurrentAsksCollapseToOnePrompt(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := 0
	g := New(func(string, string) bool {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		return true
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.Decide("burst.example", "443") }()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("raised %d prompts for one host, want 1", calls)
	}
}

// A deep directory must still yield a bindable socket: sun_path is ~104 bytes and
// macOS temp dirs alone eat most of it.
func TestDeepDirStillBinds(t *testing.T) {
	t.Parallel()
	deep := t.TempDir()
	for i := 0; i < 12; i++ {
		deep = filepath.Join(deep, "a-fairly-long-directory-segment")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := len(filepath.Join(deep, SocketName)); got <= maxSockPath {
		t.Fatalf("test setup: path is only %d bytes, not deep enough to exercise the fallback", got)
	}
	g := New(func(string, string) bool { return true })
	if err := g.Listen(deep); err != nil {
		t.Fatalf("a deep dir must still bind (via the short-path fallback): %v", err)
	}
	defer func() { _ = g.Close() }()
	if !AskOverSocket(Path(deep), "api.example", "443", time.Second) {
		t.Error("the fallback socket must be reachable by the same Path() the server used")
	}
}

func TestSocketRoundTripAndPermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := New(func(host, port string) bool { return host == "allowed.example" })
	if err := g.Listen(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	if !AskOverSocket(Path(dir), "allowed.example", "443", time.Second) {
		t.Error("an allowed host must come back allowed over the socket")
	}
	if AskOverSocket(Path(dir), "other.example", "443", time.Second) {
		t.Error("a denied host must come back denied")
	}
}

// A gate that cannot be reached must not become an open door.
func TestUnreachableSocketDenies(t *testing.T) {
	t.Parallel()
	if AskOverSocket(Path(t.TempDir()), "api.example", "443", 200*time.Millisecond) {
		t.Error("dialling a nonexistent gate must deny, not allow")
	}
}

func TestDecisionsAreReported(t *testing.T) {
	t.Parallel()
	g := New(func(host, string2 string) bool { return host == "yes.example" })
	g.Decide("yes.example", "443")
	g.Decide("no.example", "443")
	d := g.Decisions()
	if d["yes.example"] != Allow || d["no.example"] != Deny {
		t.Errorf("Decisions() = %v", d)
	}
}

// The fallback socket must live in its OWN directory. The caller bind-mounts
// filepath.Dir(socket) into a sidecar, so a bare file in TempDir would expose
// every host temp file to the agent.
func TestFallbackSocketGetsItsOwnDirectory(t *testing.T) {
	t.Parallel()
	deep := t.TempDir()
	for i := 0; i < 12; i++ {
		deep = filepath.Join(deep, "a-fairly-long-directory-segment")
	}
	sock := Path(deep)
	if dir := filepath.Dir(sock); dir == os.TempDir() {
		t.Fatalf("fallback socket sits directly in TempDir (%s): mounting its dir would expose all of it", dir)
	}
	if len(sock) > maxSockPath {
		t.Errorf("fallback path is %d bytes, over the %d sun_path ceiling", len(sock), maxSockPath)
	}
	if filepath.Base(sock) != SocketName {
		t.Errorf("fallback basename = %q, want %q so the container path is predictable", filepath.Base(sock), SocketName)
	}
}

// A stale socket left by a killed run would make the next one collide.
func TestCloseUnlinksTheSocket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := New(func(string, string) bool { return true })
	if err := g.Listen(dir); err != nil {
		t.Fatal(err)
	}
	path := Path(dir)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket missing while listening: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket survived Close (%v): a stale socket outlives its run", err)
	}
}
