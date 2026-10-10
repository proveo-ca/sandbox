//go:build e2e

// SPEC: _spec/packages/lib/opencode-seed-progress.puml
package e2e

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestOpenCodeSeedProgressTerminalHandoff(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	root := repoRoot(t)
	home := t.TempDir()
	marker := filepath.Join(home, "released")
	status := filepath.Join(home, "status")
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TERM=xterm-256color",
		"SANDBOX_VM_ID=synthetic-opencode", "PROVEO_WORKDIR=" + home,
		"PROVEO_SEED_PROGRESS=1", "PROVEO_SEED_STATUS_FILE=" + status,
		"PROVEO_INSTRUCTIONS_MARKER=" + marker,
		"PROVEO_SEED_REFUSED=" + filepath.Join(home, "refused"), "PROVEO_INSTRUCTIONS_WAIT=5",
	}
	producer := exec.CommandContext(ctx, bash, "-c", `source "$1/packages/lib/entrypoint-lib.sh"
proveo_seed_step credentials "staging session history" opencode
sleep 0.8
proveo_seed_step workspace "preparing shared folders" opencode
sleep 0.8
proveo_seed_step setup "rendering agents" opencode
sleep 0.8
: > "$PROVEO_INSTRUCTIONS_MARKER"`, "bash", root)
	producer.Env = env
	var log bytes.Buffer
	producer.Stderr = &log
	if err := producer.Start(); err != nil {
		t.Fatal(err)
	}
	consumer := exec.CommandContext(ctx, "sh", filepath.Join(root, "packages/lib/proveo-await-seed"),
		"sh", "-c", `printf 'NATIVE_READY\n'`)
	consumer.Env = env
	terminal, err := pty.StartWithSize(consumer, &pty.Winsize{Rows: 24, Cols: 140})
	if err != nil {
		_ = producer.Wait()
		t.Fatal(err)
	}
	defer terminal.Close()
	drained := make(chan string, 1)
	go func() {
		var output bytes.Buffer
		_, _ = io.Copy(&output, terminal)
		drained <- output.String()
	}()
	if err := producer.Wait(); err != nil {
		t.Fatalf("seed producer: %v\n%s", err, log.String())
	}
	if err := consumer.Wait(); err != nil {
		t.Fatalf("seed consumer: %v", err)
	}
	var output string
	select {
	case output = <-drained:
	case <-ctx.Done():
		t.Fatal("terminal output did not close")
	}
	for _, label := range []string{"credentials: staging session history", "workspace: preparing shared folders", "setup: rendering agents"} {
		if !strings.Contains(output, label) || !strings.Contains(log.String(), label) {
			t.Errorf("spinner and seed log must both carry %q:\n%s\n%s", label, output, log.String())
		}
	}
	for _, frame := range []string{"(0s) |", "(0s) /", "(0s) -", "(0s) \\"} {
		if !strings.Contains(output, frame) {
			t.Errorf("missing animated frame %q:\n%s", frame, output)
		}
	}
	if !strings.Contains(output, "\r\x1b[2KNATIVE_READY") {
		t.Errorf("spinner did not clear before native handoff:\n%s", output)
	}
}
