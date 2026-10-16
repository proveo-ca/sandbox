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
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/proveo-ca/proveo/internal/sbx"
)

var (
	retainedEngineRE = regexp.MustCompile(`retained engine (proveo-[a-z0-9-]+)`)
	openCodeEngineRE = regexp.MustCompile(`proveo-opencode-[0-9a-f]{8}(?:-prepare)?\b`)
	terminalSeq      = regexp.MustCompile("\x1b\\[[0-9;?<=>]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[()*+#].|\x1b.")
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

// terminalText is output without terminal control sequences, with each carriage return as a line break.
func terminalText(out string) string {
	return strings.ReplaceAll(terminalSeq.ReplaceAllString(out, ""), "\r", "\n")
}

// terminalWords is terminalText with each run of whitespace as one space.
func terminalWords(out string) string {
	return strings.Join(strings.Fields(terminalText(out)), " ")
}

// sessionID is the last native session id the output printed.
func sessionID(out string) string {
	id := ""
	for _, line := range strings.Split(terminalText(out), "\n") {
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &response) == nil && response.Data.ID != "" {
			id = response.Data.ID
		}
	}
	return id
}

// onTerminal runs cmd on a pseudo-terminal, approves each sbx credential prompt, stops at an sbx prompt
// the run cannot answer, and returns what it printed.
func onTerminal(cmd *exec.Cmd) ([]byte, error) {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 200})
	if err != nil {
		return nil, err
	}
	var (
		mu      sync.Mutex
		out     bytes.Buffer
		refused string
	)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 32<<10)
		approved := 0
		for {
			n, readErr := terminal.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			words := terminalWords(out.String())
			mu.Unlock()
			for ; approved < strings.Count(strings.ReplaceAll(words, " ", ""), "[A]pproveall"); approved++ {
				_, _ = terminal.Write([]byte("A\r"))
			}
			if prompt := brokenPrompt(words); prompt != "" {
				mu.Lock()
				refused = prompt
				mu.Unlock()
				_ = cmd.Process.Kill()
			}
			if readErr != nil {
				return
			}
		}
	}()
	waitErr := cmd.Wait()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
	}
	_ = terminal.Close()
	mu.Lock()
	defer mu.Unlock()
	if refused != "" {
		return bytes.Clone(out.Bytes()), fmt.Errorf("sbx asked %q, which this run cannot answer", refused)
	}
	return bytes.Clone(out.Bytes()), waitErr
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
			out, err = onTerminal(cmd)
		} else {
			out, err = cmd.CombinedOutput()
		}
		outputs = append(outputs, string(out))
		if err != nil {
			t.Fatalf("managed sbx launch %v (terminal %v): %v\n%s", native, terminal, err, terminalText(string(out)))
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
	if !created.MatchString(terminalWords(prepared)) {
		t.Fatalf("the prepare run never printed \"Created sandbox %s\", so a replacement would go unseen:\n%s", engine, prepared)
	}
	if _, err := os.Stat(openCodeOwnerRecord(t, engine)); err != nil {
		t.Fatalf("prepared engine owner record: %v", err)
	}
	reattached := func(leg, out string) {
		t.Helper()
		words := terminalWords(out)
		if !strings.Contains(words, "re-attaching to "+engine+",") {
			t.Fatalf("%s did not reattach to %s:\n%s", leg, engine, terminalText(out))
		}
		if created.MatchString(words) || strings.Contains(words, "creates a new sandbox named "+engine) {
			t.Fatalf("%s replaced the prepared engine %s:\n%s", leg, engine, terminalText(out))
		}
	}
	result := run(true, "api", "POST", "/api/session", "--data", `{"title":"prepared-reuse-fixture","location":{"directory":`+strconv.Quote(work)+`}}`)
	reattached("api POST", result)
	id := sessionID(result)
	if id == "" {
		t.Fatalf("native session creation missing:\n%s", terminalText(result))
	}
	reread := run(true, "api", "GET", "/api/session/"+id)
	reattached("api GET", reread)
	if got := sessionID(reread); got != id {
		t.Fatalf("ordinary reattach read session %q, want %q:\n%s", got, id, terminalText(reread))
	}

	// Stop the engine.
	if out, err := exec.Command("sbx", "stop", engine).CombinedOutput(); err != nil {
		t.Fatalf("sbx stop %s: %v\n%s", engine, err, out)
	}
	recovered := run(true, "api", "GET", "/api/session/"+id)
	reattached("api GET after sbx stop", recovered)
	if got := sessionID(recovered); got != id {
		t.Fatalf("after sbx stopped the engine, the reattach read session %q, want %q:\n%s", got, id, terminalText(recovered))
	}
	if !strings.Contains(terminalWords(recovered), "re-verifying the history") {
		t.Errorf("the reattach after a clean stop did not recover without asking:\n%s", terminalText(recovered))
	}
}

func TestTerminalLegsReadPtyOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	script := `[ -t 0 ] && [ -t 1 ] && echo TTY_BOTH
printf '\033[1m[A]\033(B\033[mppr'; sleep 0.2; printf 'ove\033[1Call · [R]eview each · [N]o '; IFS= read -r -n1 k; read -r _; echo "key1=$k"
printf '[A]pprove all · [R]eview each · [N]o '; IFS= read -r -n1 k; read -r _; echo "key2=$k"
printf 'Created sandbox proveo-opencode-0123abcd-prepare\n  re-attaching to proveo-opencode-0123abcd, which an earlier run left here\n'
printf '\r\033[2Kproveo: Waiting for seed — starting (1s) |' >&2; printf '\r\033[2K' >&2
printf '{"data":{"id":"ses_fixture"}}\n'`
	out, err := onTerminal(exec.CommandContext(ctx, "bash", "-c", script))
	text := terminalText(string(out))
	if err != nil || !strings.Contains(text, "TTY_BOTH") || !strings.Contains(text, "key1=A") || !strings.Contains(text, "key2=A") {
		t.Fatalf("each approval prompt on a terminal gets A: %v\n%s", err, text)
	}
	if got := sessionID(string(out)); got != "ses_fixture" {
		t.Fatalf("session id behind the seed wait = %q:\n%q", got, out)
	}
	words := terminalWords(string(out))
	if !strings.Contains(words, "re-attaching to proveo-opencode-0123abcd,") {
		t.Fatalf("reattach marker lost: %q", words)
	}
	if regexp.MustCompile(`Created sandbox proveo-opencode-0123abcd(?:\s|$)`).MatchString(words) {
		t.Fatalf("the -prepare engine read as the retained engine: %q", words)
	}
	if _, err := onTerminal(exec.CommandContext(ctx, "bash", "-c", `printf 'Overwrite? (y/N) '; sleep 60`)); err == nil || !strings.Contains(err.Error(), "Overwrite? (y/N)") {
		t.Fatalf("an sbx prompt the run cannot answer must end the leg: %v", err)
	}
	if _, err := onTerminal(exec.CommandContext(ctx, "bash", "-c", "exit 3")); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("exit status lost: %v", err)
	}
	short, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, err := onTerminal(exec.CommandContext(short, "bash", "-c", "sleep 60")); err == nil {
		t.Fatal("a leg past its deadline must fail")
	}
}
