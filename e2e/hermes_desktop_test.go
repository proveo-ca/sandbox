//go:build e2e

// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (GATEWAY, PREPARE, NOUS GUEST), _spec/defs/hermes/hermes-persistence.puml (EVERY SESSION)

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// TestHermesDesktopOpensOnTheSandboxGateway drives `proveo run hermes --addon desktop`
// with the real Hermes Desktop and a host Ollama model: the app must open on the sandbox's
// SSH gateway, its backend must start inside the sandbox on the host's GPU model, and a
// session must outlive the sandbox through the proveo home.
func TestHermesDesktopOpensOnTheSandboxGateway(t *testing.T) {
	requireHarness(t, "hermes")
	home, err := os.UserHomeDir()
	if err != nil || !hermesdesktop.Installed(home) {
		t.Skip("Hermes Desktop is not installed on this host (proveo run hermes --addon desktop installs it)")
	}
	model := hostQwen(t)
	proveoBin := buildProveo(t)
	proveoHome := t.TempDir()
	work := t.TempDir()
	userData := hermesdesktop.UserDataDir(proveoHome)
	t.Cleanup(func() { quitDesktop(t, userData) })
	runEnv := []string{"PROVEO_HOME=" + proveoHome}
	durable := filepath.Join(proveoHome, "hermes", "data")

	k := launchKit(t, proveoBin, "hermes", work, runEnv, "--addon", "desktop", "--local-model", model)

	t.Run("gateway", func(t *testing.T) {
		registry := filepath.Join(userData, "connections.json")
		waitUntil(4*time.Minute, func() bool { _, err := os.Stat(registry); return err == nil })
		b, err := os.ReadFile(registry)
		if err != nil {
			t.Fatalf("read the registry proveo hands Desktop: %v", err)
		}
		type conn struct{ ID, Kind, Host, User, RemoteHermesPath string }
		var reg struct {
			Primary     string `json:"primary"`
			Connections []conn `json:"connections"`
		}
		if err := json.Unmarshal(b, &reg); err != nil {
			t.Fatalf("parse %s: %v", b, err)
		}
		i := slices.IndexFunc(reg.Connections, func(c conn) bool { return c.ID == reg.Primary })
		if i < 0 {
			t.Fatalf("connections.json primary %q names no connection: %s", reg.Primary, b)
		}
		want := conn{reg.Primary, "ssh", k.name + ".sbx", "hermes", "/opt/proveo/shims/hermes"}
		if got := reg.Connections[i]; got != want {
			t.Errorf("primary connection = %+v, want %+v", got, want)
		}
	})

	t.Run("configured", func(t *testing.T) {
		prepared := func() bool {
			s := k.pane(200)
			return strings.Contains(s, "configured (model") || strings.Contains(s, "was not configured")
		}
		if !waitUntil(4*time.Minute, prepared) {
			t.Fatalf("the desktop hold never reported its prepare step\n%s", k.pane(40))
		}
		if s := k.pane(200); strings.Contains(s, "was not configured") {
			t.Errorf("entrypoint --prepare failed:\n%s", tail(s, 20))
		}
		out, rc := hermesExec(t, k.name, `echo "home=$HERMES_HOME"; cat "$HERMES_HOME/config.yaml"`, time.Minute)
		if rc != 0 {
			t.Fatalf("read hermes config as the agent user: rc %d\n%s", rc, out)
		}
		for _, want := range []string{
			"home=" + durable, "guest: false", "provider: custom", "host.docker.internal:11434", model,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("sandbox hermes config lacks %q:\n%s", want, out)
			}
		}
		if !strings.Contains(out, "local_runtime:") || !strings.Contains(configSection(out, "local_runtime"), "enabled: false") {
			t.Errorf("hermes's own llama.cpp must stay off in the VM:\n%s", out)
		}
	})

	t.Run("attached in the sandbox", func(t *testing.T) {
		timeout := durationEnv(t, "PROVEO_TEST_DESKTOP_ATTACH", 5*time.Minute)
		attached := "attached to " + k.name
		if !waitUntil(timeout, func() bool { return strings.Contains(k.pane(200), attached) }) {
			t.Fatalf("no %q within %s\n%s", attached, timeout, k.pane(40))
		}
		if screen := k.pane(200); strings.Contains(screen, "runs on this HOST") {
			t.Errorf("proveo warned the session ran on the host:\n%s", tail(screen, 40))
		}
	})

	marker := fmt.Sprintf("proveo-e2e-%d", time.Now().UnixNano())
	t.Run("chat on the host model", func(t *testing.T) {
		q := "Respond with only the word PONG. Reference " + marker + "."
		out, rc := hermesExec(t, k.name, "/opt/proveo/shims/hermes chat -Q --oneshot -q "+shellQuote([]string{q}),
			durationEnv(t, "PROVEO_TEST_TIMEOUT", 10*time.Minute))
		if rc != 0 || !answeredPong(out) {
			t.Fatalf("hermes chat via %s: rc %d\n%s", model, rc, tail(out, 30))
		}
		if fi, err := os.Stat(filepath.Join(durable, "state.db")); err != nil || fi.Size() == 0 {
			t.Errorf("the session store must live in the proveo home %s: %v", durable, err)
		}
	})

	t.Run("session survives the sandbox", func(t *testing.T) {
		if _, ok := k.end(); !ok {
			t.Fatalf("run 1 did not end within the teardown budget\n%s", k.pane(40))
		}
		_ = exec.Command(sbx.Binary, "rm", "--force", k.name).Run()
		quitDesktop(t, userData)

		k2 := launchKit(t, proveoBin, "hermes", work, runEnv, "--addon", "tui", "--local-model", model)
		out, rc := hermesExec(t, k2.name, `/opt/proveo/shims/hermes sessions export --format jsonl --only user-prompts -`, 2*time.Minute)
		if rc != 0 || !strings.Contains(out, marker) {
			t.Errorf("run 2 (sandbox %s, same proveo home) lost run 1's session %s: rc %d\n%s", k2.name, marker, rc, tail(out, 20))
		}
	})
}

// hostQwen is the Qwen 3.8 build this host's Ollama serves, as the model row picks it.
func hostQwen(t *testing.T) string {
	t.Helper()
	want := "qwen3.8:latest"
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		want = "qwen3.8:27b-mlx"
	}
	tags, err := ollamaTags()
	if err != nil {
		t.Skipf("Ollama unreachable on the host (%v) — no local model to run against", err)
	}
	model, why := resolveOllamaTag(env("PROVEO_TEST_QWEN_TAG", want), tags)
	if why != "" {
		t.Skipf("local model %q: %s", want, why)
	}
	if err := sbx.LocalModelFits(model, os.Getenv); err != nil {
		t.Skipf("local model %q does not fit this host right now: %v", model, err)
	}
	return model
}

// hermesExec runs script as the agent user, the account Desktop's SSH session uses.
func hermesExec(t *testing.T, name, script string, timeout time.Duration) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sbx.Binary, "exec", "-u", "hermes", "-w", "/", name, "--", "bash", "-c", script)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok && ctx.Err() == nil {
		return string(out), ee.ExitCode()
	}
	return string(out), -1
}

// configSection is the indented block under a top-level YAML key.
func configSection(yaml, key string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(yaml, "\n") {
		switch {
		case strings.HasPrefix(line, key+":"):
			in = true
		case in && line != "" && line[0] != ' ':
			return b.String()
		case in:
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// quitDesktop ends the Desktop instance proveo opened on userData; the app outlives the run by design.
func quitDesktop(t *testing.T, userData string) {
	t.Helper()
	var pids []string
	if out, err := exec.Command("pgrep", "-f", userData).Output(); err == nil {
		pids = append(pids, strings.Fields(string(out))...)
	}
	if out, err := exec.Command("lsof", "-t", "+D", userData).Output(); err == nil {
		pids = append(pids, strings.Fields(string(out))...)
	}
	for _, p := range slices.Clone(pids) {
		if out, err := exec.Command("ps", "-o", "ppid=", "-p", p).Output(); err == nil {
			parent := strings.TrimSpace(string(out))
			if cmd, err := exec.Command("ps", "-o", "command=", "-p", parent).Output(); err == nil &&
				strings.Contains(string(cmd), "Hermes.app/Contents/MacOS/") {
				pids = append(pids, parent)
			}
		}
	}
	slices.Sort(pids)
	var live []int
	for _, p := range slices.Compact(pids) {
		if pid, err := strconv.Atoi(p); err == nil && pid > 1 && pid != os.Getpid() {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			live = append(live, pid)
		}
	}
	waitUntil(5*time.Second, func() bool {
		return !slices.ContainsFunc(live, func(pid int) bool { return syscall.Kill(pid, 0) == nil })
	})
	for _, pid := range live {
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
