//go:build e2e && !windows

// SPEC: _spec/defs/opencode/engine-local-history.puml
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
	"github.com/proveo-ca/proveo/internal/termtext"
)

var (
	openCodeEngineRE = regexp.MustCompile(`proveo-opencode-[0-9a-f]{8}\b`)
	newEngineRE      = regexp.MustCompile(`new engine (proveo-opencode-[0-9a-f]{8})\b`)
)

// removeOpenCodeEngines deletes the workspace's engines and every engine the runs named, with their receipts.
func removeOpenCodeEngines(t *testing.T, work string, outputs []string) {
	t.Helper()
	engines := []string{sbx.SandboxName("opencode", work)}
	if real, err := filepath.EvalSymlinks(work); err == nil {
		engines = append(engines, sbx.SandboxName("opencode", real))
	}
	for _, out := range outputs {
		engines = append(engines, openCodeEngineRE.FindAllString(out, -1)...)
	}
	cache, _ := os.UserCacheDir()
	seen := map[string]bool{}
	for _, engine := range engines {
		if seen[engine] {
			continue
		}
		seen[engine] = true
		_ = exec.Command(sbx.Binary, "rm", "--force", engine).Run()
		if cache != "" {
			_ = os.Remove(filepath.Join(cache, "proveo", "sbx-sandboxes", engine+".json"))
		}
	}
}

// terminalText is output without terminal control sequences, with each carriage return as a line break.
func terminalText(out string) string {
	return strings.ReplaceAll(termtext.StripANSI(out), "\r", "\n")
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

// onTerminal runs cmd on a pseudo-terminal, stops at a credential consent prompt or an sbx prompt
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
		for {
			n, readErr := terminal.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			text := terminalText(out.String())
			mu.Unlock()
			words := strings.Join(strings.Fields(text), " ")
			if services, ok := consentPrompt(text); ok {
				mu.Lock()
				refused = consentFailure(services, "opencode")
				mu.Unlock()
				_ = cmd.Process.Kill()
			} else if prompt := brokenPrompt(words); prompt != "" {
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

// treeBytes is every regular file under root with its contents.
func treeBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			files[p] = string(b)
		}
		return nil
	})
	return files
}

func TestOpenCodeEngineKeepsHistoryAndImportsOnce(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("host sbx runtime unavailable")
	}
	requireDocker(t)
	image := harnessImageName("opencode")
	if !imageExists(image) {
		t.Skipf("OpenCode image %s unavailable", image)
	}
	home, work := t.TempDir(), t.TempDir()
	share := filepath.Join(home, "opencode", "v2", "share")
	if err := os.MkdirAll(share, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := exec.Command("docker", "run", "--rm", "--network", "none",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--mount", "type=bind,source="+share+",target=/share",
		"--entrypoint", "bash", image, "-c",
		`HOME=/tmp/native-home XDG_DATA_HOME=/tmp/native-data XDG_STATE_HOME=/tmp/native-state OPENCODE_DB=/share/opencode.db OPENCODE_DISABLE_MODELS_FETCH=1 "$(cat /usr/local/lib/proveo/opencode.native)" api POST /api/session --data '{"title":"host-history"}' --standalone`)
	seeded, err := seed.CombinedOutput()
	if err != nil {
		t.Fatalf("create a host V2 database holding one session: %v\n%s", err, seeded)
	}
	hostID := sessionID(string(seeded))
	if hostID == "" {
		t.Fatalf("the host fixture printed no session id:\n%s", seeded)
	}
	hostBefore := treeBytes(t, share)

	bin := buildProveo(t)
	env := append(os.Environ(), "PROVEO_HOME="+home, "PROVEO_WIZARD=off",
		"PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_LSP_INSTALL=off",
		"PROVEO_AUTO_INSTALL_TOOLS=false", "OPENCODE_DISABLE_MODELS_FETCH=1",
		"TERM=xterm-256color", "NO_COLOR=1")
	var outputs []string
	t.Cleanup(func() { removeOpenCodeEngines(t, work, outputs) })
	run := func(img string, native ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
		defer cancel()
		args := append([]string{"run", "opencode", "--yes", "--image", img, "--input", work, "--"}, native...)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env, cmd.Dir = env, work
		out, err := onTerminal(cmd)
		outputs = append(outputs, string(out))
		if err != nil {
			t.Fatalf("sbx launch %v: %v\n%s", native, err, terminalText(string(out)))
		}
		return string(out)
	}

	first := run(image, "api", "GET", "/api/session/"+hostID)
	m := newEngineRE.FindStringSubmatch(terminalWords(first))
	if m == nil {
		t.Fatalf("the first run named no new engine:\n%s", terminalText(first))
	}
	engine := m[1]
	if !strings.Contains(terminalWords(first), "imported "+filepath.Join(share, "opencode.db")) {
		t.Errorf("the first run did not import the host V2 database:\n%s", terminalText(first))
	}
	if got := sessionID(first); got != hostID {
		t.Fatalf("the new engine read session %q, want the imported host session %q:\n%s", got, hostID, terminalText(first))
	}
	created := run(image, "api", "POST", "/api/session", "--data", `{"title":"engine-history","location":{"directory":`+strconv.Quote(work)+`}}`)
	id := sessionID(created)
	if id == "" {
		t.Fatalf("no session was created in the engine:\n%s", terminalText(created))
	}
	reattached := func(leg, out string) {
		t.Helper()
		words := terminalWords(out)
		if !strings.Contains(words, "re-attaching to "+engine+",") || strings.Contains(words, "new engine") {
			t.Fatalf("%s did not reattach to %s without importing again:\n%s", leg, engine, terminalText(out))
		}
	}
	reattached("api POST", created)
	reread := run(image, "api", "GET", "/api/session/"+id)
	reattached("api GET", reread)
	if got := sessionID(reread); got != id {
		t.Fatalf("a reattach read session %q, want %q:\n%s", got, id, terminalText(reread))
	}
	if out, err := exec.Command(sbx.Binary, "stop", engine).CombinedOutput(); err != nil {
		t.Fatalf("sbx stop %s: %v\n%s", engine, err, out)
	}
	stopped := run(image, "api", "GET", "/api/session/"+id)
	reattached("api GET after sbx stop", stopped)
	if got := sessionID(stopped); got != id {
		t.Fatalf("after sbx stop the engine read session %q, want %q:\n%s", got, id, terminalText(stopped))
	}
	if after := treeBytes(t, share); len(after) != len(hostBefore) {
		t.Errorf("the engine wrote back into the host share: %d files before, %d after", len(hostBefore), len(after))
	} else {
		for p, b := range hostBefore {
			if after[p] != b {
				t.Errorf("the engine changed host file %s", p)
			}
		}
	}

	retagged := strings.SplitN(image, ":", 2)[0] + ":carry-e2e"
	if out, err := exec.Command("docker", "tag", image, retagged).CombinedOutput(); err != nil {
		t.Fatalf("docker tag: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", retagged).Run() })
	if out, err := exec.Command(sbx.Binary, "stop", engine).CombinedOutput(); err != nil {
		t.Fatalf("sbx stop %s before the image change: %v\n%s", engine, err, out)
	}
	carried := run(retagged, "api", "GET", "/api/session/"+id)
	words := terminalWords(carried)
	if !strings.Contains(words, "carried "+engine+"'s history") || !strings.Contains(words, "carried history") {
		t.Fatalf("an image change did not carry the engine's history to its replacement:\n%s", terminalText(carried))
	}
	if got := sessionID(carried); got != id {
		t.Fatalf("the replacement engine read session %q, want the carried %q:\n%s", got, id, terminalText(carried))
	}
	if _, err := os.Stat(filepath.Join(home, "opencode", "carry", engine+".db")); err == nil {
		t.Error("the carry file stayed on the host after its import")
	}
}

func TestTerminalLegsReadPtyOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	script := `[ -t 0 ] && [ -t 1 ] && echo TTY_BOTH
printf 'Created sandbox proveo-opencode-0123abcd-sibling\n  re-attaching to proveo-opencode-0123abcd, which an earlier run left here\n'
printf '\r\033[2Kproveo: Waiting for seed — starting (1s) |' >&2; printf '\r\033[2K' >&2
printf '{"data":{"id":"ses_fixture"}}\n'`
	out, err := onTerminal(exec.CommandContext(ctx, "bash", "-c", script))
	text := terminalText(string(out))
	if err != nil || !strings.Contains(text, "TTY_BOTH") {
		t.Fatalf("a leg runs on a terminal: %v\n%s", err, text)
	}
	if got := sessionID(string(out)); got != "ses_fixture" {
		t.Fatalf("session id behind the seed wait = %q:\n%q", got, out)
	}
	words := terminalWords(string(out))
	if !strings.Contains(words, "re-attaching to proveo-opencode-0123abcd,") {
		t.Fatalf("reattach marker lost: %q", words)
	}
	if regexp.MustCompile(`Created sandbox proveo-opencode-0123abcd(?:\s|$)`).MatchString(words) {
		t.Fatalf("a longer sandbox name read as the retained engine: %q", words)
	}
	consent := "printf 'This kit wants to use these credentials:\\n  opencode  API key\\n\\033[1m[A]\\033(B\\033[mpprove all · [R]eview each · [N]o: '; IFS= read -r -n1 k; echo \"typed=$k\"; sleep 60"
	out, err = onTerminal(exec.CommandContext(ctx, "bash", "-c", consent))
	if err == nil || !strings.Contains(err.Error(), "approve opencode") || strings.Contains(string(out), "typed=") {
		t.Fatalf("a consent prompt must end the leg naming the service and typing nothing: %v\n%s", err, terminalText(string(out)))
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
