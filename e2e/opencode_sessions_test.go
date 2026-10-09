//go:build e2e && !windows

// SPEC: _spec/defs/opencode/native-v2-integration.puml
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestOpenCodeTwoSessionsSurviveProveoRuns(t *testing.T) {
	requireDocker(t)
	image := os.Getenv("PROVEO_E2E_OPENCODE_IMAGE")
	if image == "" {
		image = "proveo/opencode:local"
	}
	if !imageExists(image) {
		t.Skipf("OpenCode image %s is unavailable", image)
	}
	opencodeImageVersion(t, image)
	bin := buildProveo(t)
	home, work := t.TempDir(), t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "PROVEO_HOME=" + home,
		"TERM=xterm-256color", "NO_COLOR=1", "PROVEO_SBX=0", "PROVEO_WIZARD=off",
		"PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_AGENT_EVIDENCE=default",
		"PROVEO_LSP_INSTALL=off", "PROVEO_AUTO_INSTALL_TOOLS=false",
		"PROVEO_GIT_SYNC=off", "OPENCODE_DISABLE_MODELS_FETCH=1",
	}
	run := func(flags, native []string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		args := []string{"run", "opencode", "--egress-mode", "open", "--credentials", "forward",
			"--image", image, "--input", work, "--yes"}
		args = append(args, flags...)
		if len(native) > 0 {
			args = append(append(args, "--"), native...)
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env, cmd.Dir = env, work
		terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 200})
		if err != nil {
			t.Fatal(err)
		}
		defer terminal.Close()
		var out bytes.Buffer
		_, readErr := io.Copy(&out, terminal)
		if err := cmd.Wait(); err != nil {
			t.Fatalf("proveo run opencode %v %v: %v\n%s", flags, native, err, out.String())
		}
		if readErr != nil && !errors.Is(readErr, syscall.EIO) {
			t.Fatalf("read OpenCode terminal: %v", readErr)
		}
		return out.String()
	}
	response := func(out string) string {
		t.Helper()
		for _, line := range strings.Split(out, "\n") {
			var result struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &result) == nil && result.Data.ID != "" {
				return result.Data.ID
			}
		}
		t.Fatalf("native session response missing:\n%s", out)
		return ""
	}
	ids := make([]string, 0, 2)
	for _, title := range []string{"first persisted run", "second persisted run"} {
		body, err := json.Marshal(map[string]any{"title": title, "location": map[string]string{"directory": "/app"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, response(run(nil, []string{"api", "POST", "/api/session", "--data", string(body)})))
	}
	if ids[0] == ids[1] {
		t.Fatal("separate proveo runs reused one session")
	}
	listed := run([]string{"--ls"}, []string{"--format", "json"})
	for _, id := range ids {
		if !strings.Contains(listed, id) {
			t.Fatalf("ended session %s was not resumable after both proveo runs:\n%s", id, listed)
		}
		if got := response(run(nil, []string{"api", "GET", "/api/session/" + id})); got != id {
			t.Fatalf("restored session = %q, want %q", got, id)
		}
	}
	t.Logf("both ended proveo runs remain resumable: %s, %s", ids[0], ids[1])
}
