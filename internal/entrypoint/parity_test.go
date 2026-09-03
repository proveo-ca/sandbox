package entrypoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/chromebridge"
)

var bashBridge = regexp.MustCompile(`(?m)^\s*_apply_env_bridge\s+(\S+)\s+(\S+)\s+(\S+|"")\s+('[^']*'|"[^"]*")\s+("[^"]*"|\S*)\s*$`)

var goBridge = regexp.MustCompile(`\{from: "([^"]*)", to: "([^"]*)"(?:, fallback: "([^"]*)")?(?:, def: "([^"]*)")?(?:, transform: "([^"]*)")?\}`)

func TestGoBashBridgeParity(t *testing.T) {
	t.Parallel()

	shPath := filepath.Join("..", "..", "packages", "lib", "entrypoint-lib.sh")
	sh, err := os.ReadFile(shPath)
	if err != nil {
		t.Fatalf("read %s: %v", shPath, err)
	}
	goPath := filepath.Join("entrypoint.go")
	gosrc, err := os.ReadFile(goPath)
	if err != nil {
		t.Fatalf("read %s: %v", goPath, err)
	}

	unquote := func(s string) string { return strings.Trim(s, `"'`) }

	bash := map[string]string{}
	for _, m := range bashBridge.FindAllStringSubmatch(string(sh), -1) {
		from, to := m[1], m[2]
		bash[from+"→"+to] = unquote(m[3]) + "|" + unquote(m[4]) + "|" + unquote(m[5])
	}
	if len(bash) == 0 {
		t.Fatal("parsed zero bridges from entrypoint-lib.sh — the parser has drifted from the script")
	}

	gomap := map[string]string{}
	for _, m := range goBridge.FindAllStringSubmatch(string(gosrc), -1) {
		gomap[m[1]+"→"+m[2]] = m[3] + "|" + m[4] + "|" + m[5]
	}
	if len(gomap) == 0 {
		t.Fatal("parsed zero bridges from entrypoint.go — the parser has drifted from the source")
	}

	for k, want := range bash {
		got, ok := gomap[k]
		if !ok {
			t.Errorf("bridge %s exists in entrypoint-lib.sh but not in internal/entrypoint", k)
			continue
		}
		if got != want {
			t.Errorf("bridge %s differs\n  bash: %s\n  go:   %s", k, want, got)
		}
	}
	for k := range gomap {
		if _, ok := bash[k]; !ok {
			t.Errorf("bridge %s exists in internal/entrypoint but not in entrypoint-lib.sh", k)
		}
	}
}

// The Claude in Chrome scope gate exists twice, host-side and in the
// entrypoint, so one table is run through BOTH. Also pins bash 3.2.
// SPEC: _spec/defs/claudecode/chrome-bridge.puml
func TestChromeScopeGateParityWithBash(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	lib, err := filepath.Abs(filepath.Join("..", "..", "packages", "lib", "entrypoint-lib.sh"))
	if err != nil {
		t.Fatal(err)
	}

	// login writes the credential shape a persisted /login leaves in the home the
	// container mounts; blankLogin is the macOS-host shape, tokens removed.
	const login = `{"claudeAiOauth":{"accessToken":"real","expiresAt":9999999999999}}`
	const blankLogin = `{"claudeAiOauth":{"accessToken":"","refreshToken":""}}`

	for _, tc := range []struct {
		name  string
		env   map[string]string
		creds string // .claude/.credentials.json in the agent home; "" writes none
	}{
		{name: "bare env token", env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "x"}},
		{name: "env token, cloud-launcher scopes", env: map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN":  "x",
			"CLAUDE_CODE_OAUTH_SCOPES": "user:inference user:ccr_inference user:file_upload",
		}},
		{name: "env token, profile scope", env: map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN":  "x",
			"CLAUDE_CODE_OAUTH_SCOPES": "user:profile user:inference",
		}},
		{name: "env token, scopes that accept nothing", env: map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN":  "x",
			"CLAUDE_CODE_OAUTH_SCOPES": "user:inference user:file_upload",
		}},
		{name: "env token shadows the login", env: map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "x",
		}, creds: login},
		{name: "file-descriptor delivery", env: map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR": "3",
		}},
		{name: "persisted login", creds: login},
		{name: "api key alone", env: map[string]string{"ANTHROPIC_API_KEY": "x"}},
		{name: "api key beside a login", env: map[string]string{"ANTHROPIC_API_KEY": "x"}, creds: login},
		{name: "api key beside a BLANKED login", env: map[string]string{"ANTHROPIC_API_KEY": "x"}, creds: blankLogin},
		{name: "nothing to classify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if tc.creds != "" {
				if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(tc.creds), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			hasLogin := tc.creds == login

			cmd := exec.Command(sh, "-c", "source \""+lib+"\" >/dev/null 2>&1; _proveo_chrome_scope_ok")
			cmd.Env = append(os.Environ(),
				"HOME="+home, "PROVEO_HOME="+home,
				"CLAUDE_CODE_OAUTH_TOKEN=", "CLAUDE_CODE_OAUTH_SCOPES=",
				"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR=", "ANTHROPIC_API_KEY=")
			for k, v := range tc.env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			bashOK := cmd.Run() == nil

			goOK := chromebridge.ScopeGate(func(k string) string { return tc.env[k] }, hasLogin) == ""
			if bashOK != goOK {
				t.Errorf("bash says wired=%v, chromebridge.ScopeGate says wired=%v — the two copies of the rule have drifted", bashOK, goOK)
			}
		})
	}
}
