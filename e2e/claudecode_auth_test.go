//go:build e2e

// SPEC: _spec/_paradigms/credential-boundary.puml

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestClaudeCodeAuth asserts the CREDENTIAL BOUNDARY rather than the model:
// that a credential the operator holds still authenticates after crossing the
// egress layer, under both ways of handling it.
func TestClaudeCodeAuth(t *testing.T) {
	requireHarness(t, "claudecode")

	proveoBin := buildProveo(t)
	// The intercepting tier is where a boundary exists at all: `open` + `forward`
	// is a plain bridge with no MITM, so it could not fail the way this guards.
	mode := env("PROVEO_TEST_EGRESS_MODE", "allowlist")

	declared := declaredSecrets(t, "claudecode")

	for _, c := range claudecodeAuth {
		t.Run(c.name, func(t *testing.T) {
			requireValidAnthropicCredential(t, c)
			for _, creds := range egress.CredentialModes() {
				t.Run(creds, func(t *testing.T) {
					if creds == "forward" && !declared[c.envVar] {
						t.Skipf("claudecode does not declare %s in its manifest env, so "+
							"--credentials forward has nothing to forward — it is reachable "+
							"only through the broker", c.envVar)
					}
					probeCredentialBoundary(t, c, proveoBin, mode, creds)
				})
			}
		})
	}
}

func declaredSecrets(t *testing.T, target string) map[string]bool {
	t.Helper()
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	for _, m := range ms {
		if _, ok := m.Images[target]; !ok {
			continue
		}
		out := map[string]bool{}
		for _, e := range m.Env {
			if e.Secret {
				out[e.Name] = true
			}
		}
		return out
	}
	t.Fatalf("no manifest declares an image for target %q", target)
	return nil
}

// claudecodeAuthCase is one credential the anthropic provider accepts, and how.
type claudecodeAuthCase struct {
	name   string
	envVar string
	header string
	bearer bool
	// beta is the anthropic-beta this credential requires, if any.
	beta string
}

var claudecodeAuth = []claudecodeAuthCase{
	{name: "api-key", envVar: "ANTHROPIC_API_KEY", header: "x-api-key"},
	{
		name: "subscription", envVar: "CLAUDE_CODE_OAUTH_TOKEN",
		header: "authorization", bearer: true, beta: "oauth-2025-04-20",
	},
}

func probeCredentialBoundary(t *testing.T, c claudecodeAuthCase, proveoBin, mode, creds string) {
	t.Helper()

	work := t.TempDir()
	// claudecode's output mount — create it host-side so the bind lands on a dir
	// this user owns.
	if err := os.MkdirAll(filepath.Join(work, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	sess := tmux.New(fmt.Sprintf("proveo-auth-%s-%s-%d", c.name, creds, os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgsFor(t, c.envVar)...)
	cmd = append(cmd,
		"PROVEO_SBX=off",
		"PROVEO_HOME="+t.TempDir(),
	)
	cmd = append(cmd, proveoBin, "run", "claudecode",
		"--egress-mode", mode, "--credentials", creds, "--input", work, "--shell")

	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}
	w := newWatcher(t, sess)
	waitForContainerShell(t, w, durationEnv(t, "PROVEO_TEST_TIMEOUT", 4*time.Minute))

	if err := sess.SendText(credentialProbeLine(c, creds)); err != nil {
		t.Fatalf("send credential probe: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("send credential probe newline: %v", err)
	}
	w.until("the credential probe to answer", 90*time.Second, func() bool {
		return probeStatus(w.Screen()) != ""
	})

	if got := probeStatus(w.Screen()); got != "200" {
		w.Fatalf("%s reached the provider as HTTP %s under --egress-mode %s --credentials %s, want 200 — "+
			"the host accepted this same credential on this same endpoint, so the egress layer is what changed the answer",
			c.envVar, got, mode, creds)
	}
	assertCleanBoot(t, w.Screen())
	t.Logf("%s authenticated through --egress-mode %s --credentials %s (GET /v1/models → 200)",
		c.envVar, mode, creds)
}

func credentialProbeLine(c claudecodeAuthCase, creds string) string {
	var b strings.Builder
	// --max-time keeps a blocked request from hanging the shell; -o /dev/null so a
	// credential can never be echoed back onto the pane by the response body.
	b.WriteString(`curl -sS --max-time 30 -o /dev/null -w 'PROBE=%{http_code}\n'`)
	b.WriteString(` -H 'anthropic-version: 2023-06-01'`)
	if c.beta != "" {
		fmt.Fprintf(&b, " -H 'anthropic-beta: %s'", c.beta)
	}
	if creds == "forward" {
		value := "$" + c.envVar
		if c.bearer {
			value = "Bearer $" + c.envVar
		}
		// Double-quoted so the shell expands the variable: the secret is read from
		// the container's own env and never travels on this test's argv.
		fmt.Fprintf(&b, ` -H "%s: %s"`, c.header, value)
	}
	b.WriteString(" https://api.anthropic.com/v1/models")
	return b.String()
}

var probeStatusRE = regexp.MustCompile(`^PROBE=(\d{3})$`)

func probeStatus(screen string) string {
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "%{") || strings.Contains(line, "curl") {
			continue
		}
		if m := probeStatusRE.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

func requireValidAnthropicCredential(t *testing.T, c claudecodeAuthCase) string {
	t.Helper()
	token := hostEnvValue(t, c.envVar)
	if token == "" {
		t.Skipf("%s not present in the environment or the repo .env", c.envVar)
	}
	if status := anthropicModelsStatus(c, token); status != http.StatusOK {
		t.Skipf("%s is present but the provider does not accept it (HTTP %d) — "+
			"supply a working credential to exercise this path", c.envVar, status)
	}
	return token
}

// anthropicModelsStatus is the host-side half of the same question the container
// asks: authenticate against GET /v1/models and report the status.
func anthropicModelsStatus(c claudecodeAuthCase, token string) int {
	req, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	if c.beta != "" {
		req.Header.Set("anthropic-beta", c.beta)
	}
	if c.bearer {
		req.Header.Set(c.header, "Bearer "+token)
	} else {
		req.Header.Set(c.header, token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
