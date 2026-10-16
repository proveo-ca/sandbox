//go:build e2e && !windows

// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/proveo-ca/proveo/internal/sbx"
)

var (
	retainedEngineRE = regexp.MustCompile(`retained engine (proveo-[a-z0-9-]+)`)
	openCodeEngineRE = regexp.MustCompile(`proveo-opencode-[0-9a-f]{8}(?:-prepare)?\b`)
)

func openCodeOwnerRecord(t *testing.T, engine string) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(cache, "proveo", "sbx-sandboxes", engine+".opencode-cache.json")
}

// removeOpenCodeEngines deletes the workspace's engines and every engine the runs named, with owner records and receipts.
func removeOpenCodeEngines(t *testing.T, work string, outputs []string) {
	engines := []string{sbx.SandboxName("opencode", work)}
	if real, err := filepath.EvalSymlinks(work); err == nil {
		engines = append(engines, sbx.SandboxName("opencode", real))
	}
	for _, name := range append([]string(nil), engines...) {
		engines = append(engines, name+"-prepare")
	}
	for _, out := range outputs {
		engines = append(engines, openCodeEngineRE.FindAllString(out, -1)...)
	}
	seen := map[string]bool{}
	for _, engine := range engines {
		if seen[engine] {
			continue
		}
		seen[engine] = true
		_ = exec.Command("sbx", "rm", "--force", engine).Run()
		record := openCodeOwnerRecord(t, engine)
		_ = os.Remove(record)
		_ = os.Remove(filepath.Join(filepath.Dir(record), engine+".json"))
	}
}

// onTerminal runs cmd on a pseudo-terminal, approves sbx's credential prompt once, and returns what it printed.
func onTerminal(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 200})
	if err != nil {
		return nil, err
	}
	defer terminal.Close()
	defer context.AfterFunc(ctx, func() { _ = terminal.Close() })()
	var out bytes.Buffer
	buf := make([]byte, 32<<10)
	approved := false
	for {
		n, readErr := terminal.Read(buf)
		out.Write(buf[:n])
		if !approved && strings.Contains(ansiSeq.ReplaceAllString(out.String(), ""), "[A]pprove all") {
			approved = true
			_, _ = terminal.Write([]byte("A\r"))
		}
		if readErr != nil {
			break
		}
	}
	return out.Bytes(), cmd.Wait()
}

func TestOpenCodeSbxPreparedSourceSurvivesOrdinaryReattach(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("host sbx runtime unavailable")
	}
	requireDocker(t)
	image := harnessImageName("opencode")
	if !imageExists(image) {
		t.Skipf("OpenCode image %s unavailable", image)
	}
	home, work, source := t.TempDir(), t.TempDir(), t.TempDir()
	create := exec.Command("docker", "run", "--rm", "--network", "none",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--mount", "type=bind,source="+source+",target=/source",
		"--entrypoint", "bash", image, "-c",
		`HOME=/tmp/native-schema XDG_DATA_HOME=/tmp/native-data XDG_STATE_HOME=/tmp/native-state OPENCODE_DB=/source/opencode.db OPENCODE_DISABLE_MODELS_FETCH=1 "$(cat /usr/local/lib/proveo/opencode.native)" session list --format json --standalone`)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create actual V2 source: %v\n%s", err, out)
	}
	bin := buildProveo(t)
	env := append(os.Environ(), "PROVEO_HOME="+home, "PROVEO_WIZARD=off",
		"PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_LSP_INSTALL=off",
		"PROVEO_AUTO_INSTALL_TOOLS=false", "OPENCODE_DISABLE_MODELS_FETCH=1",
		"TERM=xterm-256color", "NO_COLOR=1")
	var outputs []string
	t.Cleanup(func() { removeOpenCodeEngines(t, work, outputs) })
	run := func(terminal bool, native ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		args := append([]string{"run", "opencode", "--yes", "--image", image,
			"--input", work, "--egress-mode", "open", "--"}, native...)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env, cmd.Dir = env, work
		var out []byte
		var err error
		if terminal {
			out, err = onTerminal(ctx, cmd)
		} else {
			out, err = cmd.CombinedOutput()
		}
		outputs = append(outputs, string(out))
		if err != nil {
			t.Fatalf("managed sbx launch %v (terminal %v): %v\n%s", native, terminal, err, out)
		}
		return string(out)
	}
	prepared := run(false, "--proveo-prepare", "--source", source)
	m := retainedEngineRE.FindStringSubmatch(prepared)
	if m == nil {
		t.Fatalf("the prepare run named no retained engine:\n%s", prepared)
	}
	engine := m[1]
	if !strings.Contains(prepared, "no terminal — the agent was not started") {
		t.Errorf("the headless prepare did not stop before the agent:\n%s", prepared)
	}
	created := regexp.MustCompile(`Created sandbox ` + regexp.QuoteMeta(engine) + `(?:\s|$)`)
	if !created.MatchString(plain(prepared)) {
		t.Fatalf("the prepare run never printed \"Created sandbox %s\", so a replacement would go unseen:\n%s", engine, prepared)
	}
	if _, err := os.Stat(openCodeOwnerRecord(t, engine)); err != nil {
		t.Fatalf("prepared engine owner record: %v", err)
	}
	result := run(true, "api", "POST", "/api/session", "--data", `{"title":"prepared-reuse-fixture","location":{"directory":`+strconv.Quote(work)+`}}`)
	var id string
	for _, line := range strings.Split(result, "\n") {
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(ansiSeq.ReplaceAllString(line, ""))), &response) == nil && response.Data.ID != "" {
			id = response.Data.ID
		}
	}
	if id == "" {
		t.Fatalf("native session creation missing: %s", result)
	}
	reread := run(true, "api", "GET", "/api/session/"+id)
	for _, out := range []string{plain(result), plain(reread)} {
		if created.MatchString(out) || strings.Contains(out, "creates a new sandbox named "+engine) {
			t.Fatalf("ordinary reattach replaced the prepared engine:\n%s", out)
		}
	}

	// Stop the engine, as sbx's idle auto-stop does.
	if out, err := exec.Command("sbx", "stop", engine).CombinedOutput(); err != nil {
		t.Fatalf("sbx stop %s: %v\n%s", engine, err, out)
	}
	recovered := run(true, "api", "GET", "/api/session/"+id)
	if !strings.Contains(recovered, id) {
		t.Fatalf("after sbx stopped the engine, the reattach lost session %s:\n%s", id, recovered)
	}
	if !strings.Contains(plain(recovered), "re-verifying the history") {
		t.Errorf("the reattach after a clean stop did not recover without asking:\n%s", recovered)
	}
}
