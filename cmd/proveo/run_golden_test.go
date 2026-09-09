package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/ui"
)

// _spec/internal/run/run-spec.puml (MOVE 6, PROOF).
func TestRunResolveGolden(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		image  string
		mode   string
		creds  string
		sbx    string
	}{
		{"docker-allowlist-broker", "claudecode", "proveo/claudecode:test", "allowlist", "broker", "off"},
		{"docker-open-forward", "claudecode", "proveo/claudecode:test", "open", "forward", "off"},
		{"docker-opencode-broker", "opencode", "proveo/opencode:test", "allowlist", "broker", "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderRun(t, tc.target, tc.image, tc.mode, tc.creds, tc.sbx)
			golden := filepath.Join("testdata", "run", tc.name+".golden")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (re-run with UPDATE_GOLDEN=1 to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("resolve path changed.\n--- want ---\n%s\n--- got ---\n%s", want, got)
			}
		})
	}
}

// renderRun drives doRun in --print mode under a fully pinned environment and
// returns its combined output with every host-specific value scrubbed.
func renderRun(t *testing.T, target, image, mode, creds, sbx string) string {
	t.Helper()
	work := t.TempDir()
	home := t.TempDir()
	mustWrite(t, filepath.Join(work, "package.json"), `{"name":"golden"}`)
	envFile := filepath.Join(t.TempDir(), "empty.env")
	mustWrite(t, envFile, "")

	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	for _, k := range entrypoint.ConfigVars {
		t.Setenv(k, "")
	}
	// The role names are no longer read from the environment, but a golden run
	// must still be blind to an operator's own: a stray ARCHITECT_MODEL must not
	// change what proveo prints, and the day it does, this clears it first.
	// SPEC: _spec/_plans/retire-model-bridging.puml
	for _, k := range provider.RoleNames {
		t.Setenv(k, "")
	}
	ghDir := filepath.Join(t.TempDir(), "gh")
	if err := os.MkdirAll(ghDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_CONFIG_DIR", ghDir)
	for k, v := range map[string]string{
		"PROVEO_HOME": home, "PROVEO_WIZARD": "off", "PROVEO_SBX": sbx,
		// PROVEO_DIND is retired and warns when set; pinned off so an operator's own
		// exported value cannot add a line to these goldens.
		"PROVEO_EGRESS_ENV_FILE": envFile, "PROVEO_DIND": "off",
		"PROVEO_AGENT_EVIDENCE": "", "PROVEO_SBX_MCP": "", "PROVEO_PIDS_LIMIT": "",
		"PROVEO_SQUID_PROXY_IMAGE": "", "PROVEO_EGRESS_PROXY_IMAGE": "", "PROVEO_OLLAMA_IMAGE": "",
		"PROVEO_EGRESS_PROVIDER_DOMAINS": "", "GIT_AUTHOR_NAME": "Golden", "GIT_AUTHOR_EMAIL": "g@example.com",
		"GIT_COMMITTER_NAME": "Golden", "GIT_COMMITTER_EMAIL": "g@example.com", "GH_TOKEN": "", "GITHUB_TOKEN": "",
	} {
		t.Setenv(k, v)
	}

	out := capture(t, func() {
		_ = run.Do(run.Params{
			Target: target, Image: image, Mode: mode, Credentials: creds,
			Input: work, Output: filepath.Join(work, "reports"), PrintOnly: true,
		}, runDeps())
	})
	return scrubRun(out, work, home)
}

func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	so, se, ud := os.Stdout, os.Stderr, ui.Default
	os.Stdout, os.Stderr = w, w
	ui.Default = ui.New(struct{ io.Writer }{w})
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout, os.Stderr, ui.Default = so, se, ud
	return <-done
}

var (
	reSid  = regexp.MustCompile(`proveo-\d+-\d+`)
	reTmp  = regexp.MustCompile(`/(?:private/)?(?:var|tmp)/[^\s"',:]*`)
	rePort = regexp.MustCompile(`127\.0\.0\.1:\d+`)
	reUser = regexp.MustCompile(`--user \d+:\d+`)
)

// scrub removes everything that legitimately differs between two runs on two
// hosts, so a diff means the resolve path changed and nothing else.
func scrubRun(s, work, home string) string {
	s = strings.ReplaceAll(s, work, "<WORK>")
	s = strings.ReplaceAll(s, home, "<HOME>")
	if wd, err := os.Getwd(); err == nil {
		s = strings.ReplaceAll(s, wd, "<REPO>")
	}
	if h, err := os.UserHomeDir(); err == nil {
		s = strings.ReplaceAll(s, h, "<USERHOME>")
	}
	s = reSid.ReplaceAllString(s, "proveo-<SID>")
	s = rePort.ReplaceAllString(s, "127.0.0.1:<PORT>")
	s = reUser.ReplaceAllString(s, "--user <UID>:<GID>")
	s = reTmp.ReplaceAllString(s, "<TMP>")
	return s
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
