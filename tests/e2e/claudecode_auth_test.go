//go:build e2e

// SPEC: _spec/tests/42-hello-world-e2e.puml, _spec/_paradigms/credential-boundary.puml

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

func TestClaudeCodeAuth(t *testing.T) {
	if os.Getenv("PROVEO_LLM_TEST") != "1" {
		t.Skip("set PROVEO_LLM_TEST=1 to run the claudecode auth matrix")
	}
	if !tmux.Available() {
		t.Skip("tmux not installed (brew install tmux)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	image := env("PROVEO_TEST_IMAGE_CLAUDECODE", "proveo/claudecode:latest")
	if !dockerImagePresent(t, image) {
		t.Skipf("harness image %s not built (mise run build claudecode)", image)
	}

	proveoBin := buildProveo(t)
	mode := env("PROVEO_TEST_EGRESS_MODE", "firewall")

	for _, c := range claudecodeAuth {
		t.Run(c.name, func(t *testing.T) {
			token := hostEnvValue(t, c.envVar)
			if token == "" {
				t.Skipf("%s not present in the environment or the repo .env", c.envVar)
			}
			if status := c.probe(token); status != http.StatusOK {
				t.Skipf("%s is present but the provider rejects it (HTTP %d) — supply a working credential to exercise this path",
					c.envVar, status)
			}
			runClaudecodeAuth(t, c, proveoBin, mode)
		})
	}
}

type claudecodeAuthCase struct {
	name   string
	envVar string
	probe  func(token string) int
}

var claudecodeAuth = []claudecodeAuthCase{
	{name: "api-key", envVar: "ANTHROPIC_API_KEY", probe: probeAPIKey},
	{name: "subscription", envVar: "CLAUDE_CODE_OAUTH_TOKEN", probe: probeOAuthToken},
}

func runClaudecodeAuth(t *testing.T, c claudecodeAuthCase, proveoBin, mode string) {
	t.Helper()

	work := copySampleWorkspace(t)
	replaceSampleEnvWith(t, work, c.envVar)
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "config", "user.email", "e2e@proveo.test")
	mustRun(t, work, "git", "config", "user.name", "proveo e2e")
	if err := os.MkdirAll(filepath.Join(work, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	marker := fmt.Sprintf("PROVEO-AUTH-%s-%d", strings.ToUpper(c.name), os.Getpid())
	prompt := fmt.Sprintf(
		"Create a new file at /workspace/output/AUTH_OK.txt whose entire contents are this one line: %s\n"+
			"Do not create, edit or delete any other file. When the file exists, stop.", marker)

	sess := tmux.New(fmt.Sprintf("proveo-auth-%s-%d", c.name, os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgsFor(t, c.envVar)...)
	cmd = append(cmd, proveoBin, "run", "claudecode", "--egress-mode", mode, "--input", work, "--", "-p", prompt)

	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}

	deadline := time.Now().Add(durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute))
	var lastScreen, found string
	for {
		screen, captureErr := sess.CaptureAll()
		alive := captureErr == nil
		if alive {
			lastScreen = screen
		}
		if found = firstExisting(work, []string{"reports/AUTH_OK.txt", "AUTH_OK.txt"}); found != "" {
			break
		}
		if !alive || time.Now().After(deadline) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if s, ok := waitSessionExit(sess, 45*time.Second); ok && s != "" {
		lastScreen = s
	}

	for _, sig := range authFailures {
		if strings.Contains(lastScreen, sig) {
			t.Errorf("%s auth did not reach the model — pane contains %q\n--- offending lines ---\n%s",
				c.envVar, sig, linesMatching(lastScreen, sig, 3))
		}
	}
	assertCleanBoot(t, lastScreen)

	if found == "" {
		t.Fatalf("claudecode produced no file on the host using %s\n--- screen (tail) ---\n%s",
			c.envVar, tail(lastScreen, 60))
	}
	if body := readIn(work, found); !strings.Contains(body, marker) {
		t.Fatalf("%s exists but lacks this run's marker %q\n--- file ---\n%s", found, marker, body)
	}
	t.Logf("claudecode authenticated via %s and wrote %s", c.envVar, filepath.Join(work, found))
}

var authFailures = []string{
	"Please run /login",
	"Invalid API key",
	"authentication_error",
	"API Error: 403",
	"API Error: 401",
	"credential broker OFF",
}

func probeAPIKey(token string) int {
	req, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("x-api-key", token)
	req.Header.Set("anthropic-version", "2023-06-01")
	return statusOf(req)
}

func probeOAuthToken(token string) int {
	req, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	return statusOf(req)
}

func statusOf(req *http.Request) int {
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func childEnvArgsFor(t *testing.T, keep string) []string {
	t.Helper()
	args := childEnvArgs(t)
	for i, a := range args {
		if strings.HasPrefix(a, "PROVEO_EGRESS_ENV_FILE=") {
			args[i] = "PROVEO_EGRESS_ENV_FILE=" + writeSingleCredentialEnv(t, keep)
		}
	}
	return args
}

func writeSingleCredentialEnv(t *testing.T, keep string) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "%s=%s\n", keep, hostEnvValue(t, keep))
	for _, k := range []string{"ARCHITECT_MODEL", "EDITOR_MODEL", "SMALL_MODEL"} {
		if v := hostEnvValue(t, k); v != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func replaceSampleEnvWith(t *testing.T, work, keep string) {
	t.Helper()
	path := filepath.Join(work, ".env")
	if _, err := os.Lstat(path); err != nil {
		return
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(writeSingleCredentialEnv(t, keep))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
