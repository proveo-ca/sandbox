// SPEC: _spec/_plans/config-seeding-and-persistence.puml, _spec/defs/cecli/cecli-paradigm.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeServer writes a stdio "MCP server" onto a temp PATH dir.
func fakeServer(t *testing.T, bin, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

const (
	// Answers the handshake the way a real server does.
	srvAnswers = `read -r _
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"fake"}}}'
`
	// Starts, says nothing, exits 0 — the shape that shipped dead for weeks
	// behind a `command -v` gate.
	srvSilent = `read -r _
exit 0
`
	// Answers, but with an error rather than a result.
	srvErrors = `read -r _
printf '%s\n' '{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom"}}'
`
	// Never answers. A boot must not wait on it.
	srvHangs = `read -r _
sleep 60
`
)

func runProbe(t *testing.T, bash, bin, server string) (ok bool, out string) {
	t.Helper()
	script := `export PATH="` + bin + `:/usr/bin:/bin"
source "$1/packages/lib/entrypoint-lib.sh"
if _proveo_mcp_probe ` + server + `; then echo VERDICT=ok; else echo VERDICT=no; fi`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_MCP_PROBE_TIMEOUT=3")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe run failed: %v\n%s", err, b)
	}
	return strings.Contains(string(b), "VERDICT=ok"), string(b)
}

// The probe is the step the MCP class exists to add. `command -v` proved a
// launcher existed; only an answered handshake proves a server serves.
func TestMcpProbeSeparatesAServerThatAnswersFromOneThatMerelyStarts(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	bin := t.TempDir()
	fakeServer(t, bin, "answers", srvAnswers)
	fakeServer(t, bin, "silent", srvSilent)
	fakeServer(t, bin, "errors", srvErrors)

	for _, tc := range []struct {
		server string
		want   bool
		why    string
	}{
		{"answers", true, "a result naming protocolVersion and serverInfo is a working server"},
		{"silent", false, "starting and saying nothing is exactly the failure that shipped for weeks"},
		{"errors", false, "a JSON-RPC error is an answer, but not a usable server"},
		{"absent", false, "a server not on PATH cannot be wired"},
	} {
		t.Run(tc.server, func(t *testing.T) {
			t.Parallel()
			got, out := runProbe(t, bash, bin, tc.server)
			if got != tc.want {
				t.Errorf("_proveo_mcp_probe %s = %v, want %v — %s\n%s", tc.server, got, tc.want, tc.why, out)
			}
		})
	}
}

// A server that never answers must not hold the boot: the whole point is to
// reach a verdict without the agent waiting on one.
func TestMcpProbeIsBounded(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	bin := t.TempDir()
	fakeServer(t, bin, "hangs", srvHangs)

	done := make(chan bool, 1)
	go func() { ok, _ := runProbe(t, bash, bin, "hangs"); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Error("a server that never answered was reported as working")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the probe did not return — PROVEO_MCP_PROBE_TIMEOUT is not bounding it")
	}
}

// configure_cecli_mcp writes the declaration only when the handshake answers,
// and never a second mcp-servers block (cecli reads the first).
func TestConfigureCecliMcpOnlyDeclaresAServerThatAnswered(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)

	wire := func(t *testing.T, body string, existing string) (conf string, out string) {
		t.Helper()
		home, bin, ws := t.TempDir(), t.TempDir(), t.TempDir()
		fakeServer(t, bin, "serena", body)
		if existing != "" {
			if err := os.WriteFile(filepath.Join(home, ".cecli.conf.yml"), []byte(existing), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		script := `export PATH="` + bin + `:/usr/bin:/bin"
source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
_proveo_agent_home() { printf '%s' "$agent_home"; }
configure_cecli_mcp "$3"`
		cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, ws)
		cmd.Env = append(os.Environ(), "PROVEO_MCP_PROBE_TIMEOUT=3", "PROVEO_CECLI_MCP=", "HOME="+home)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("configure_cecli_mcp failed: %v\n%s", err, b)
		}
		got, _ := os.ReadFile(filepath.Join(home, ".cecli.conf.yml"))
		return string(got), string(b)
	}

	t.Run("answers", func(t *testing.T) {
		t.Parallel()
		conf, out := wire(t, srvAnswers, "")
		if !strings.Contains(conf, "mcp-servers:") || !strings.Contains(conf, "serena:") {
			t.Errorf("no serena declaration written:\n%s\n%s", conf, out)
		}
		// The project must be the scan root: /app does not exist on the sbx backend.
		if strings.Contains(conf, "--project, /app") || !strings.Contains(conf, "/tmp/") {
			t.Errorf("--project is not the scan root:\n%s", conf)
		}
	})

	t.Run("silent server is not declared", func(t *testing.T) {
		t.Parallel()
		conf, out := wire(t, srvSilent, "")
		if strings.Contains(conf, "serena:") {
			t.Errorf("declared a server that never answered:\n%s", conf)
		}
		if !strings.Contains(out, "initialize handshake") {
			t.Errorf("the refusal must say why:\n%s", out)
		}
	})

	t.Run("an existing declaration wins", func(t *testing.T) {
		t.Parallel()
		mine := "mcp-servers:\n  mcpServers:\n    mine: {}\n"
		conf, _ := wire(t, srvAnswers, mine)
		if strings.Count(conf, "mcp-servers:") != 1 || strings.Contains(conf, "serena:") {
			t.Errorf("overwrote or duplicated the operator's declaration:\n%s", conf)
		}
	})

	t.Run("opt-out", func(t *testing.T) {
		t.Parallel()
		home, bin, ws := t.TempDir(), t.TempDir(), t.TempDir()
		fakeServer(t, bin, "serena", srvAnswers)
		script := `export PATH="` + bin + `:/usr/bin:/bin"
source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
_proveo_agent_home() { printf '%s' "$agent_home"; }
configure_cecli_mcp "$3"`
		cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, ws)
		cmd.Env = append(os.Environ(), "PROVEO_CECLI_MCP=off", "HOME="+home)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed: %v\n%s", err, b)
		}
		if _, err := os.Stat(filepath.Join(home, ".cecli.conf.yml")); err == nil {
			t.Error("wrote a declaration with PROVEO_CECLI_MCP=off")
		}
	})
}

// The image half. Serena is pinned for the reason the layer was deleted the
// first time: an unpinned install shipped a version skew that left the server
// dead on every boot. Same shape as agentPins.
func TestCecliImagePinsSerena(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	df, err := os.ReadFile(filepath.Join(root, "defs", "cecli", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(df)
	for _, want := range []string{
		"ARG SERENA_VERSION",
		`"serena-agent==${SERENA_VERSION}"`,
		`pip show serena-agent | grep -qx "Version: ${SERENA_VERSION}"`,
		"ln -sf /opt/serena/bin/serena /usr/local/bin/serena",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("defs/cecli/Dockerfile is missing %q", want)
		}
	}
	// A bare `docker build` must fail rather than bake whatever is newest.
	if strings.Contains(dockerfile, "ARG SERENA_VERSION=") {
		t.Error("SERENA_VERSION has a default — that is `@latest` one step removed")
	}
	if strings.Contains(dockerfile, "serena-agent\n") || strings.Contains(dockerfile, "install serena-agent ") {
		t.Error("an unpinned serena-agent install is in the Dockerfile")
	}

	bs, err := os.ReadFile(filepath.Join(root, "defs", "cecli", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	build := string(bs)
	if !strings.Contains(build, "proveo_agent_version SERENA_VERSION pypi serena-agent") {
		t.Error("defs/cecli/build.sh must resolve SERENA_VERSION through proveo_agent_version")
	}
	if !strings.Contains(build, `--build-arg SERENA_VERSION="$SERENA_VERSION"`) {
		t.Error("defs/cecli/build.sh must pass SERENA_VERSION to the build")
	}
}
