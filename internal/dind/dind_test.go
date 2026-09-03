package dind

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvEnabled(t *testing.T) {
	t.Setenv("PROVEO_DIND", "")
	if EnvEnabled() {
		t.Fatal("expected false with empty env")
	}
	t.Setenv("PROVEO_DIND", "1")
	if !EnvEnabled() {
		t.Fatal("PROVEO_DIND=1 should enable")
	}
	t.Setenv("PROVEO_DIND", "true")
	if !EnvEnabled() {
		t.Fatal("PROVEO_DIND=true should enable")
	}
	t.Setenv("PROVEO_DIND", "0")
	if EnvEnabled() {
		t.Fatal("PROVEO_DIND=0 should not enable")
	}
}

func TestScopeHasDockerfiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if ScopeHasDockerfiles(root) {
		t.Fatal("empty dir should not match")
	}
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ScopeHasDockerfiles(root) {
		t.Fatal("Dockerfile at root should match")
	}

	nested := t.TempDir()
	deep := nested
	for i := 0; i < 3; i++ {
		deep = filepath.Join(deep, "d")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(deep, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ScopeHasDockerfiles(nested) {
		t.Fatal("compose.yml within maxdepth should match")
	}

	// Prune via .gitignore directory basename
	pruned := t.TempDir()
	if err := os.WriteFile(filepath.Join(pruned, ".gitignore"), []byte("vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pruned, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pruned, "vendor", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ScopeHasDockerfiles(pruned) {
		t.Fatal("Dockerfile under gitignored dir basename should be pruned")
	}
}

func TestShouldStart(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVEO_DIND", "")

	if ShouldStart(false, root, true, func() bool { return true }) {
		t.Fatal("non-capable must never start")
	}
	if ShouldStart(true, root, false, nil) {
		t.Fatal("non-interactive without env must not start")
	}
	if !ShouldStart(true, root, true, func() bool { return true }) {
		t.Fatal("interactive yes should start")
	}
	if ShouldStart(true, root, true, func() bool { return false }) {
		t.Fatal("interactive no should not start")
	}
	t.Setenv("PROVEO_DIND", "1")
	if !ShouldStart(true, root, false, nil) {
		t.Fatal("PROVEO_DIND=1 should start without prompt")
	}
}

func TestPromptYesNo(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if !PromptYesNo(strings.NewReader("y\n"), &out) {
		t.Fatal("y should be yes")
	}
	if PromptYesNo(strings.NewReader("n\n"), &out) {
		t.Fatal("n should be no")
	}
	if PromptYesNo(strings.NewReader("\n"), &out) {
		t.Fatal("empty should be no")
	}
}

type fakeRunner struct {
	calls [][]string
}

func (f *fakeRunner) Run(args ...string) error {
	f.calls = append(f.calls, append([]string(nil), args...))
	return nil
}

func TestStartAndAgentArgs(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	var warn strings.Builder
	s, err := Start(r, "opencode", "/tmp/scope", &warn)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "proveo-dind-opencode" {
		t.Fatalf("name = %q", s.Name)
	}
	args := s.AgentArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--link") || !strings.Contains(joined, "DOCKER_HOST=tcp://docker:2375") {
		t.Fatalf("agent args missing link/host: %v", args)
	}
	// last call should be docker run ... docker:dind
	last := r.calls[len(r.calls)-1]
	if last[0] != "run" || last[len(last)-1] != "docker:dind" {
		t.Fatalf("unexpected start call: %v", last)
	}
	// The severity, not a phrase inside the sentence: the design language puts
	// it in the mark, and "warn: " is the mark plain mode degrades to. The
	// substance is asserted separately, because a warning that does not name
	// --privileged is not the warning this sidecar needs.
	if !strings.Contains(warn.String(), "warn: ") {
		t.Errorf("the privileged-sidecar warning lost its severity:\n%s", warn.String())
	}
	if !strings.Contains(warn.String(), "--privileged") {
		t.Errorf("the warning does not name --privileged:\n%s", warn.String())
	}
	s.Cleanup(r)
	if s.Name != "" {
		t.Fatal("cleanup should clear name")
	}
}

func TestModeSupported(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode string
		want bool
	}{
		{"open", true},
		{"OPEN", true},
		{"  open  ", true},
		{"allowlist", false},
		{"review", false},
		{"broker", false},
		{"firewall", false},
		{"proxy", false},
		{"", false},
	} {
		if got := ModeSupported(tc.mode); got != tc.want {
			t.Errorf("ModeSupported(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestEnvAndLinkArgs(t *testing.T) {
	t.Parallel()
	s := &Sidecar{Name: "proveo-dind-x"}

	env := strings.Join(s.EnvArgs(), " ")
	if !strings.Contains(env, "DOCKER_HOST=tcp://docker:2375") {
		t.Fatalf("EnvArgs missing DOCKER_HOST: %q", env)
	}
	if strings.Contains(env, "--link") {
		t.Fatalf("EnvArgs must not carry --link: %q", env)
	}

	if link := strings.Join(s.LinkArgs(), " "); link != "--link proveo-dind-x:docker" {
		t.Fatalf("LinkArgs = %q", link)
	}

	// AgentArgs composes link + env (default-bridge attachment).
	all := strings.Join(s.AgentArgs(), " ")
	if !strings.Contains(all, "--link") || !strings.Contains(all, "DOCKER_HOST=tcp://docker:2375") {
		t.Fatalf("AgentArgs should carry both link and host: %q", all)
	}
}

func TestConnectNetwork(t *testing.T) {
	t.Parallel()
	s := &Sidecar{Name: "proveo-dind-opencode"}

	r := &fakeRunner{}
	if err := s.ConnectNetwork(r, "sess-broker-net"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 docker call, got %v", r.calls)
	}
	if got, want := strings.Join(r.calls[0], " "), "network connect --alias docker sess-broker-net proveo-dind-opencode"; got != want {
		t.Fatalf("connect call = %q, want %q", got, want)
	}

	// No-op guards: empty network, and nil / cleaned sidecar.
	empty := &fakeRunner{}
	if err := s.ConnectNetwork(empty, ""); err != nil || len(empty.calls) != 0 {
		t.Fatalf("empty network must be a no-op: err=%v calls=%v", err, empty.calls)
	}
	var nilSc *Sidecar
	if err := nilSc.ConnectNetwork(empty, "net"); err != nil || len(empty.calls) != 0 {
		t.Fatalf("nil sidecar must be a no-op: err=%v calls=%v", err, empty.calls)
	}
}

// slowDaemonRunner answers the first n docker commands with an error, then succeeds —
// the shape of a daemon that is still starting inside a container that is already
// up.
type slowDaemonRunner struct {
	failFirst int
	calls     [][]string
}

func (f *slowDaemonRunner) Run(args ...string) error {
	f.calls = append(f.calls, args)
	if len(f.calls) <= f.failFirst {
		return errors.New("Cannot connect to the Docker daemon")
	}
	return nil
}

// `docker run -d` returns when the CONTAINER is up, not when its daemon is
// listening. Handing the agent a shell in that gap is what made a working posture
// report "Cannot connect to the Docker daemon at tcp://docker:2375" — so the wait
// has to outlast a slow start rather than probe once.
func TestWaitReadyOutlastsASlowDaemonStart(t *testing.T) {
	t.Parallel()
	var slept time.Duration
	clock := time.Now()
	r := &slowDaemonRunner{failFirst: 8}
	sc := &Sidecar{Name: "proveo-dind-cecli"}

	err := sc.WaitReady(r, 90*time.Second,
		func() time.Time { return clock },
		func(d time.Duration) { slept += d; clock = clock.Add(d) })
	if err != nil {
		t.Fatalf("WaitReady over a slow start = %v, want nil", err)
	}
	if len(r.calls) != 9 {
		t.Errorf("probed %d times, want 9 (8 refusals then success)", len(r.calls))
	}
	if slept == 0 {
		t.Error("WaitReady spun without sleeping — that is a busy loop, not a wait")
	}
	// The readiness question must be asked THROUGH the sidecar, so it tests the
	// daemon's own socket rather than whatever the host's DOCKER_HOST points at.
	want := []string{"exec", "proveo-dind-cecli", "docker", "version"}
	if got := r.calls[0]; len(got) != len(want) {
		t.Errorf("probe argv = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("probe argv = %v, want %v", got, want)
				break
			}
		}
	}
}

// A daemon that never answers must fail with a named cause, not hang forever.
func TestWaitReadyGivesUpWithADiagnosis(t *testing.T) {
	t.Parallel()
	clock := time.Now()
	r := &slowDaemonRunner{failFirst: 1 << 30}
	sc := &Sidecar{Name: "proveo-dind-opencode"}

	err := sc.WaitReady(r, 5*time.Second,
		func() time.Time { return clock },
		func(d time.Duration) { clock = clock.Add(d) })
	if err == nil {
		t.Fatal("a daemon that never answers must be an error")
	}
	if !strings.Contains(err.Error(), "proveo-dind-opencode") {
		t.Errorf("the error must name the sidecar, got %q", err)
	}
}

// A nil sidecar is nothing to wait for, not a failure.
func TestWaitReadyIgnoresANilSidecar(t *testing.T) {
	t.Parallel()
	var sc *Sidecar
	if err := sc.WaitReady(&slowDaemonRunner{}, time.Second, nil, nil); err != nil {
		t.Errorf("nil sidecar = %v, want nil", err)
	}
}
