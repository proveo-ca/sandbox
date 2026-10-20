//go:build e2e

// SPEC: _spec/_paradigms/credential-boundary.puml, _spec/defs/opencode/opencode-paradigm.puml

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const opencodeKeyVar = "OPENCODE_API_KEY"

// TestOpenCodeAuth asserts the CREDENTIAL BOUNDARY for OpenCode's own
// gateway: that OPENCODE_API_KEY still authenticates through sbx's proxy.
func TestOpenCodeAuth(t *testing.T) {
	requireHarness(t, "opencode")

	proveoBin := buildProveo(t)
	declared := declaredSecrets(t, "opencode")

	for _, c := range opencodeAuth {
		t.Run(c.name, func(t *testing.T) {
			requireValidOpenCodeCredential(t, c)
			for _, creds := range run.CredentialModes() {
				t.Run(creds, func(t *testing.T) {
					if creds == "forward" && !declared[opencodeKeyVar] {
						t.Skipf("opencode does not declare %s in its manifest env, so "+
							"--credentials forward has nothing to forward — it is reachable "+
							"only through the broker", opencodeKeyVar)
					}
					probeOpenCodeBoundary(t, c, proveoBin, creds)
				})
			}
		})
	}
}

type opencodeAuthCase struct {
	name  string
	base  string
	model string
}

var opencodeAuth = []opencodeAuthCase{
	{name: "zen", base: "https://opencode.ai/zen/v1", model: "deepseek-v4-flash"},
	{name: "go", base: "https://opencode.ai/zen/go/v1", model: "glm-5.3-flash"},
}

const opencodeSessionHeader = "x-opencode-session"

func (c opencodeAuthCase) session() string {
	return fmt.Sprintf("proveo-e2e-%s-%d", c.name, os.Getpid())
}

func (c opencodeAuthCase) body() string {
	return fmt.Sprintf(`{"model":"%s","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, c.model)
}

// probeOpenCodeBoundary runs the real harness in its sandbox and asks the
// gateway, from inside the sandbox, whether the credential arrived.
func probeOpenCodeBoundary(t *testing.T, c opencodeAuthCase, proveoBin, creds string) {
	t.Helper()

	work := t.TempDir()
	before, _ := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-ocauth-%s-%s-%d", c.name, creds, os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgsFor(t, opencodeKeyVar)...)
	cmd = append(cmd, "PROVEO_HOME="+t.TempDir())
	cmd = append(cmd, proveoBin, "run", "opencode",
		"--credentials", creds, "--input", work)

	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}
	k := awaitKit(t, sess, before, "opencode", work)
	out, _ := k.exec(opencodeProbeLine(c, creds), 90*time.Second)

	if got := probeStatus(out); got != "200" {
		t.Fatalf("%s reached %s as HTTP %s under --credentials %s, want 200 — "+
			"the host accepted this same credential on this same endpoint, so sbx's proxy is what changed the answer\n%s\n%s",
			opencodeKeyVar, c.base, got, creds, out, k.pane(40))
	}
	screen, _ := sess.CaptureAll()
	assertCleanBoot(t, screen)
	t.Logf("%s authenticated through --credentials %s (POST %s/chat/completions → 200)",
		opencodeKeyVar, creds, c.base)
}

// opencodeProbeLine is the request the sandbox makes: an auth header carrying
// sbx's placeholder under `broker`, the real key under `forward`.
func opencodeProbeLine(c opencodeAuthCase, creds string) string {
	var b strings.Builder
	// -o /dev/null so neither the credential nor a completion is echoed onto the
	// pane; --max-time keeps a blocked request from hanging the shell.
	b.WriteString(`curl -sS --max-time 60 -o /dev/null -w 'PROBE=%{http_code}\n' -X POST`)
	b.WriteString(` -H 'content-type: application/json'`)
	fmt.Fprintf(&b, ` -H '%s: %s'`, opencodeSessionHeader, c.session())
	// Sent in both credential modes: sbx substitutes the placeholder the agent
	// sends, so a missing header leaves nothing to substitute. Double-quoted so
	// the shell expands it and the secret never rides this test's argv.
	fmt.Fprintf(&b, ` -H "authorization: Bearer $%s"`, opencodeKeyVar)
	fmt.Fprintf(&b, ` -d '%s' %s/chat/completions`, c.body(), c.base)
	return b.String()
}

// requireValidOpenCodeCredential skips unless the key exists AND this plan's
// endpoint accepts it on the very request the container probe will make.
func requireValidOpenCodeCredential(t *testing.T, c opencodeAuthCase) string {
	t.Helper()
	token := hostEnvValue(t, opencodeKeyVar)
	if token == "" {
		t.Skipf("%s not present in the environment or the repo .env", opencodeKeyVar)
	}
	if status := opencodeCompletionStatus(c, token); status != http.StatusOK {
		t.Skipf("%s is present but %s does not accept it on %s (HTTP %d) — a Zen key is not a Go key; "+
			"supply a credential for this plan to exercise it", opencodeKeyVar, c.name, c.model, status)
	}
	return token
}

// opencodeCompletionStatus is the host-side half of the same question the
// container asks.
func opencodeCompletionStatus(c opencodeAuthCase, token string) int {
	req, err := http.NewRequest(http.MethodPost, c.base+"/chat/completions", strings.NewReader(c.body()))
	if err != nil {
		return 0
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set(opencodeSessionHeader, c.session())
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
