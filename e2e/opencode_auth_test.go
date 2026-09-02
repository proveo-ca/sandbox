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

	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// opencodeKeyVar is the one credential both OpenCode plans read: Zen
// (pay-as-you-go) and Go (the subscription). opencode takes it from the
// environment ahead of auth.json, which is what lets the login happen on the
// host before launch and never inside the sandbox.
const opencodeKeyVar = "OPENCODE_API_KEY"

// TestOpenCodeAuth asserts the CREDENTIAL BOUNDARY for OpenCode's own gateway:
// that OPENCODE_API_KEY still authenticates after crossing the egress layer.
//
// Unlike anthropic's, this gateway's GET /v1/models is PUBLIC — 200 with no
// header and 200 with a bad one (probed 2026-09-01) — so it cannot tell a
// delivered credential from a dropped one. The cheapest request that can is a
// one-token chat completion on the plan's least expensive paid model: 401 with
// no valid key, 200 with one. On Zen that spends a fraction of a cent; on a Go
// subscription nothing extra. A FREE model would not do: opencode.ai serves free
// models to an unauthenticated caller, so the probe would pass with the boundary
// broken.
//
// Each plan is a case, and each skips unless the host itself gets 200 on that
// plan's endpoint — a Zen key is not a Go key, and asking host-side first is what
// lets a container failure be attributed to the egress layer instead of the key.
//
// `forward` is skipped throughout: opencode declares no secret in its manifest
// env, so nothing is forwarded — the key reaches a forward run only through a
// workspace .env the entrypoint autoloads, which this probe's empty workspace does
// not have. The broker is the boundary under test.
func TestOpenCodeAuth(t *testing.T) {
	requireHarness(t, "opencode")

	proveoBin := buildProveo(t)
	mode := env("PROVEO_TEST_EGRESS_MODE", "allowlist")
	declared := declaredSecrets(t, "opencode")

	for _, c := range opencodeAuth {
		t.Run(c.name, func(t *testing.T) {
			requireValidOpenCodeCredential(t, c)
			for _, creds := range egress.CredentialModes() {
				t.Run(creds, func(t *testing.T) {
					if creds == "forward" && !declared[opencodeKeyVar] {
						t.Skipf("opencode does not declare %s in its manifest env, so "+
							"--credentials forward has nothing to forward — it is reachable "+
							"only through the broker", opencodeKeyVar)
					}
					probeOpenCodeBoundary(t, c, proveoBin, mode, creds)
				})
			}
		})
	}
}

// opencodeAuthCase is one OpenCode plan: its base URL and the cheapest PAID model
// it served when this was written (models.dev, 2026-09-01). Paid on purpose — see
// TestOpenCodeAuth. If a model is retired the host-side check skips with the
// status rather than failing the boundary.
type opencodeAuthCase struct {
	name  string
	base  string
	model string
}

var opencodeAuth = []opencodeAuthCase{
	{name: "zen", base: "https://opencode.ai/zen/v1", model: "deepseek-v4-flash"},
	{name: "go", base: "https://opencode.ai/zen/go/v1", model: "glm-5.3-flash"},
}

func (c opencodeAuthCase) body() string {
	return fmt.Sprintf(`{"model":"%s","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, c.model)
}

// probeOpenCodeBoundary runs the real harness through the real egress topology and
// asks the gateway, from inside the container, whether the credential arrived.
func probeOpenCodeBoundary(t *testing.T, c opencodeAuthCase, proveoBin, mode, creds string) {
	t.Helper()

	work := t.TempDir()
	sess := tmux.New(fmt.Sprintf("proveo-ocauth-%s-%s-%d", c.name, creds, os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	// --shell puts a shell on the PTY instead of the agent: the topology is
	// identical, and the credential question is answerable with curl. The
	// narrowed env file holds ONLY this credential, so the broker pins opencode
	// rather than refusing over a multi-key host. PROVEO_HOME is isolated so a
	// developer's own mounted home cannot supply or suppress anything.
	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgsFor(t, opencodeKeyVar)...)
	cmd = append(cmd, "PROVEO_HOME="+t.TempDir())
	cmd = append(cmd, proveoBin, "run", "opencode",
		"--egress-mode", mode, "--credentials", creds, "--input", work, "--shell")

	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}
	w := newWatcher(t, sess)
	waitForContainerShell(t, w, durationEnv(t, "PROVEO_TEST_TIMEOUT", 4*time.Minute))

	if err := sess.SendText(opencodeProbeLine(c, creds)); err != nil {
		t.Fatalf("send credential probe: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("send credential probe newline: %v", err)
	}
	w.until("the credential probe to answer", 90*time.Second, func() bool {
		return probeStatus(w.Screen()) != ""
	})

	if got := probeStatus(w.Screen()); got != "200" {
		w.Fatalf("%s reached %s as HTTP %s under --egress-mode %s --credentials %s, want 200 — "+
			"the host accepted this same credential on this same endpoint, so the egress layer is what changed the answer",
			opencodeKeyVar, c.base, got, mode, creds)
	}
	assertCleanBoot(t, w.Screen())
	t.Logf("%s authenticated through --egress-mode %s --credentials %s (POST %s/chat/completions → 200)",
		opencodeKeyVar, mode, creds, c.base)
}

// opencodeProbeLine is the request the container makes.
//
// Under `broker` it sends NO auth header: the container holds only the sentinel,
// and the proxy is supposed to attach the real key on-route — an unauthenticated
// request answering 200 IS the brokering assertion, and on this endpoint it
// cannot pass by accident (401 without a key, probed). Under `forward` the
// container holds the real key and presents it the way the agent does.
func opencodeProbeLine(c opencodeAuthCase, creds string) string {
	var b strings.Builder
	// -o /dev/null so neither the credential nor a completion is echoed onto the
	// pane; --max-time keeps a blocked request from hanging the shell.
	b.WriteString(`curl -sS --max-time 60 -o /dev/null -w 'PROBE=%{http_code}\n' -X POST`)
	b.WriteString(` -H 'content-type: application/json'`)
	if creds == "forward" {
		// Double-quoted so the shell expands it: the secret is read from the
		// container's own env and never travels on this test's argv.
		fmt.Fprintf(&b, ` -H "authorization: Bearer $%s"`, opencodeKeyVar)
	}
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
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
