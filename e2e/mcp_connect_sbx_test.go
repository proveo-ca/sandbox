//go:build e2e

// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package e2e

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// probeMCP is a streamable-HTTP MCP server on host loopback that records every method it is sent.
type probeMCP struct {
	port    int
	mu      sync.Mutex
	methods []string
}

const probeTool = "proveo_ping"

func startProbeMCP(t *testing.T) *probeMCP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &probeMCP{port: ln.Addr().(*net.TCPAddr).Port}
	srv := &http.Server{Handler: http.HandlerFunc(p.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return p
}

func (p *probeMCP) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
			Name            string `json:"name"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.methods = append(p.methods, req.Method)
	p.mu.Unlock()
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	switch req.Method {
	case "initialize":
		reply["result"] = map[string]any{
			"protocolVersion": cmp.Or(req.Params.ProtocolVersion, "2025-06-18"),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "proveo-e2e-probe", "version": "1.0.0"},
		}
	case "tools/list":
		reply["result"] = map[string]any{"tools": []any{map[string]any{
			"name": probeTool, "description": "Answers pong; proves the call reached the probe server.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		}}}
	case "tools/call":
		reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong"}}}
	case "ping":
		reply["result"] = map[string]any{}
	default:
		reply["error"] = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

// waitFor reports whether method arrived within d.
func (p *probeMCP) waitFor(method string, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(250 * time.Millisecond) {
		p.mu.Lock()
		seen := slices.Contains(p.methods, method)
		p.mu.Unlock()
		if seen || time.Now().After(deadline) {
			return seen
		}
	}
}

func (p *probeMCP) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.methods...)
}

// guestURL is the probe server as a sandbox addresses the host.
func (p *probeMCP) guestURL() string {
	return fmt.Sprintf("http://host.docker.internal:%d/mcp", p.port)
}

// mcpClientPy speaks streamable HTTP: initialize, initialized, tools/list, then tools/call when argv[3] is "call".
const mcpClientPy = `import json, sys, urllib.request
url, token, want_call = sys.argv[1], sys.argv[2], sys.argv[3] == "call"
sid = None
def rpc(i, method, params=None):
    global sid
    body = {"jsonrpc": "2.0", "method": method}
    if i is not None:
        body["id"] = i
    if params is not None:
        body["params"] = params
    req = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if sid:
        req.add_header("Mcp-Session-Id", sid)
    with urllib.request.urlopen(req, timeout=60) as r:
        sid = r.headers.get("Mcp-Session-Id") or sid
        raw = r.read().decode()
    if i is None:
        return None
    if raw.lstrip().startswith("{"):
        return json.loads(raw)
    for line in raw.splitlines():
        if line.startswith("data:"):
            m = json.loads(line[5:])
            if m.get("id") == i:
                return m
    raise SystemExit("no response to %s: %r" % (method, raw[:400]))
init = rpc(1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "proveo-e2e", "version": "1"}})
print("SERVER=" + json.dumps(init.get("result", {}).get("serverInfo")))
rpc(None, "notifications/initialized")
tools = [t["name"] for t in rpc(2, "tools/list", {})["result"]["tools"]]
print("TOOLS=" + ",".join(tools))
if want_call:
    name = next((t for t in tools if t.endswith("` + probeTool + `")), None)
    if not name:
        raise SystemExit("no ` + probeTool + ` among " + ",".join(tools))
    res = rpc(3, "tools/call", {"name": name, "arguments": {}})
    print("CALL=" + "".join(c.get("text", "") for c in res["result"]["content"]))
`

// mcpClientScript is a bash line that runs mcpClientPy against url ($-expansions allowed) with token.
func mcpClientScript(url, token string, call bool) string {
	mode := "list"
	if call {
		mode = "call"
	}
	return fmt.Sprintf("python3 - %s %s %s <<'PY'\n%sPY\n", url, token, mode, mcpClientPy)
}

// agentExec runs script in sandbox name as the agent user, bounded by d.
func agentExec(t *testing.T, name, script string, d time.Duration) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, sbx.Binary, "exec", "-u", agentUID, "-w", "/", name, "--", "bash", "-lc", script)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// allowProbe lets sandbox name reach the probe server's host port, under both names the proxy may see.
func allowProbe(t *testing.T, name string, p *probeMCP) {
	t.Helper()
	port := strconv.Itoa(p.port)
	if out, err := exec.Command(sbx.Binary, "policy", "allow", "network", "--sandbox", name,
		"localhost:"+port+",host.docker.internal:"+port).CombinedOutput(); err != nil {
		t.Fatalf("policy allow: %v\n%s", err, out)
	}
}

func mcpSandboxName(layer string) string {
	return fmt.Sprintf("proveo-mcp-%s-%d", layer, time.Now().UnixNano()%1_000_000_000)
}

// TestMCPConnectionsFromASandbox climbs one layer at a time; the probe server's request log is the oracle.
func TestMCPConnectionsFromASandbox(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_MCP_TEST")) {
		t.Skip("set PROVEO_MCP_TEST=1 — starts real sandboxes; layer 4 registers a probe server in sbx's host-wide MCP store and removes it after")
	}
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}

	t.Run("1-transport", func(t *testing.T) {
		p := startProbeMCP(t)
		name := mcpSandboxName("transport")
		createWorkloadSandbox(t, sbx.RunConfig{Name: name,
			KitDir: probeWorkload(t, "proveo-mcp-probe", sbx.RecipeV3{From: harnessImage(t, "claudecode")}),
			Mounts: mountsOf(t.TempDir())})
		allowProbe(t, name, p)
		out, err := agentExec(t, name, mcpClientScript(p.guestURL(), "''", true), 2*time.Minute)
		if err != nil || !strings.Contains(out, "CALL=pong") {
			t.Fatalf("LAYER FAILED — adds sbx's network path from a sandbox to a host port: %v\nprobe saw %v\n%s", err, p.seen(), out)
		}
	})

	t.Run("2-claude-direct", func(t *testing.T) {
		p := startProbeMCP(t)
		name := mcpSandboxName("claude")
		createWorkloadSandbox(t, sbx.RunConfig{Name: name,
			KitDir: probeWorkload(t, "proveo-mcp-probe", sbx.RecipeV3{From: harnessImage(t, "claudecode")}),
			Mounts: mountsOf(t.TempDir())})
		allowProbe(t, name, p)
		out, err := agentExec(t, name, `export PATH="$HOME/.local/bin:$PATH"
claude mcp add --transport http --scope user proveo-probe `+p.guestURL()+` >/dev/null
claude mcp list`, 3*time.Minute)
		line := lineWith(out, "proveo-probe")
		if err != nil || !strings.Contains(line, "Connected") || !p.waitFor("initialize", 10*time.Second) {
			t.Fatalf("LAYER FAILED — adds claude's own MCP client: `claude mcp list` row %q (err %v); probe saw %v\n%s",
				line, err, p.seen(), out)
		}
	})

	t.Run("3-codex-direct", func(t *testing.T) {
		if !envflag.On(os.Getenv("PROVEO_MCP_TEST_CODEX")) {
			t.Skip("set PROVEO_MCP_TEST_CODEX=1 — `codex mcp list` never connects (measured on codex-cli 0.164.0), so this layer spends one model turn")
		}
		p := startProbeMCP(t)
		_, create := proveoWorkload(t, t.TempDir(), "codex", isolatedHome(t))
		name := mcpSandboxName("codex")
		t.Cleanup(func() { _, _ = removeSandbox(t, name) })
		makeTempMounts(t, create)
		if out, err := exec.Command(sbx.Binary, withName(create, name)...).CombinedOutput(); err != nil {
			t.Skipf("sbx create (codex workload): %v\n%s", err, out)
		}
		allowProbe(t, name, p)
		out, err := agentExec(t, name, `export PATH="$HOME/.local/bin:$PATH"
codex mcp add proveo-probe --url `+p.guestURL()+` >/dev/null
codex exec --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox \
  "Call the `+probeTool+` MCP tool exactly once, then reply with its exact output and nothing else."`, 6*time.Minute)
		if !p.waitFor("tools/call", 10*time.Second) {
			t.Fatalf("LAYER FAILED — adds codex's MCP client inside a real codex session: the probe never got tools/call "+
				"(err %v); probe saw %v\n%s", err, p.seen(), lastLines(out, 30))
		}
	})

	t.Run("4-sbx-gateway", func(t *testing.T) {
		p := startProbeMCP(t)
		reg := fmt.Sprintf("proveo-e2e-probe-%d", os.Getpid())
		if out, err := exec.Command(sbx.Binary, "mcp", "add", reg, "--url",
			fmt.Sprintf("http://localhost:%d/mcp", p.port), "--skip-ssrf-check", "--skip-auth").CombinedOutput(); err != nil {
			t.Skipf("sbx mcp add: %v\n%s", err, out)
		}
		t.Cleanup(func() {
			if out, err := exec.Command(sbx.Binary, "mcp", "rm", "--force", reg).CombinedOutput(); err != nil {
				t.Logf("probe registration %s not removed: %v\n%s", reg, err, out)
			}
		})
		_, create := proveoWorkload(t, t.TempDir(), "claudecode", append(isolatedHome(t), "PROVEO_SBX_MCP=on"))
		if slices.Contains(create, sbx.MCPGatewayEnv+"=") {
			t.Fatalf("PROVEO_SBX_MCP=on still blanks %s on create: %v", sbx.MCPGatewayEnv, create)
		}
		name := mcpSandboxName("gateway")
		t.Cleanup(func() { _, _ = removeSandbox(t, name) })
		makeTempMounts(t, create)
		if out, err := exec.Command(sbx.Binary, withName(create, name)...).CombinedOutput(); err != nil {
			t.Skipf("sbx create (claudecode workload): %v\n%s", err, out)
		}
		if out, err := exec.Command(sbx.Binary, "mcp", "load", reg, "--sandbox", name).CombinedOutput(); err != nil {
			t.Fatalf("sbx mcp load: %v\n%s", err, out)
		}

		list, err := agentExec(t, name, `export PATH="$HOME/.local/bin:$PATH"; claude mcp list`, 3*time.Minute)
		if row := lineWith(list, "mcp-gateway"); err != nil || !strings.Contains(row, "Connected") {
			t.Errorf("the published kit's hook did not leave claude connected to sbx's gateway: row %q (err %v)\n%s", row, err, list)
		}
		out, err := agentExec(t, name, mcpClientScript(`"$`+sbx.MCPGatewayEnv+`"`, `"$MCP_SENTINEL_TOKEN_NAME"`, true), 2*time.Minute)
		if err != nil || !strings.Contains(out, "CALL=pong") || !p.waitFor("tools/call", 10*time.Second) {
			t.Fatalf("LAYER FAILED — adds sbx's MCP gateway: a tools/call through %s never reached the host probe "+
				"registered as %s (http://localhost:%d); probe saw %v (err %v)\n%s",
				sbx.MCPGatewayEnv, reg, p.port, p.seen(), err, out)
		}
	})
}

// TestHermesTakesSbxsMCPGateway is layer 5: hermes always takes sbx's gateway (mcpGateway: always).
func TestHermesTakesSbxsMCPGateway(t *testing.T) {
	if !envflag.On(os.Getenv("PROVEO_MCP_TEST")) {
		t.Skip("set PROVEO_MCP_TEST=1 — starts a real sandbox and registers a probe server in sbx's host-wide MCP store")
	}
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	p := startProbeMCP(t)
	reg := fmt.Sprintf("proveo-e2e-probe-h%d", os.Getpid())
	if out, err := exec.Command(sbx.Binary, "mcp", "add", reg, "--url",
		fmt.Sprintf("http://localhost:%d/mcp", p.port), "--skip-ssrf-check", "--skip-auth").CombinedOutput(); err != nil {
		t.Skipf("sbx mcp add: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(sbx.Binary, "mcp", "rm", "--force", reg).Run() })
	_, create := proveoWorkload(t, t.TempDir(), "hermes", isolatedHome(t))
	if slices.Contains(create, sbx.MCPGatewayEnv+"=") {
		t.Fatalf("hermes declares mcpGateway: always, yet create blanks %s: %v", sbx.MCPGatewayEnv, create)
	}
	name := mcpSandboxName("hermes")
	t.Cleanup(func() { _, _ = removeSandbox(t, name) })
	makeTempMounts(t, create)
	if out, err := exec.Command(sbx.Binary, withName(create, name)...).CombinedOutput(); err != nil {
		t.Skipf("sbx create (hermes workload): %v\n%s", err, out)
	}
	if out, err := exec.Command(sbx.Binary, "mcp", "load", reg, "--sandbox", name).CombinedOutput(); err != nil {
		t.Fatalf("sbx mcp load: %v\n%s", err, out)
	}
	lib := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	if out, err := exec.Command(sbx.Binary, "cp", lib, name+":/tmp/entrypoint-lib.sh").CombinedOutput(); err != nil {
		t.Fatalf("sbx cp: %v\n%s", err, out)
	}
	out, err := agentExec(t, name, `source /tmp/entrypoint-lib.sh && proveo_wire_hermes_mcp_gateway hermes
H="$(command -v hermes || echo /opt/hermes/.venv/bin/hermes)"
HERMES_HOME="${HERMES_HOME:-/opt/data}" "$H" mcp test mcp-gateway`, 3*time.Minute)
	if err != nil || !strings.Contains(out, probeTool) {
		t.Fatalf("LAYER FAILED — adds Hermes's MCP client on sbx's gateway: `hermes mcp test mcp-gateway` does not "+
			"list %s (err %v); probe saw %v\n%s", probeTool, err, p.seen(), lastLines(out, 30))
	}
}

// isolatedHome points a rendered workload's proveo home and state at temp dirs, so kit hooks
// that write user config never reach the operator's ~/.proveo.
func isolatedHome(t *testing.T) []string {
	return []string{"PROVEO_HOME=" + t.TempDir(), "PROVEO_STATE_ROOT=" + t.TempDir()}
}

// makeTempMounts creates the positional workspace dirs a --print plan names under the temp root;
// print mode names them, and only a real run creates them.
func makeTempMounts(t *testing.T, create []string) {
	t.Helper()
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	for i, a := range create {
		if i > 0 && strings.HasPrefix(create[i-1], "-") || !filepath.IsAbs(a) {
			continue
		}
		real := a
		if r, err := filepath.EvalSymlinks(filepath.Dir(a)); err == nil {
			real = filepath.Join(r, filepath.Base(a))
		}
		if strings.HasPrefix(real, tmp) || strings.HasPrefix(a, os.TempDir()) {
			if err := os.MkdirAll(a, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestTheProbeMCPServerAnswersTheSandboxClient runs the in-sandbox client against the probe on the host.
func TestTheProbeMCPServerAnswersTheSandboxClient(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	p := startProbeMCP(t)
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", p.port)
	cmd := exec.Command("bash", "-c", mcpClientScript(url, "tok", true))
	cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1", "no_proxy=127.0.0.1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("client: %v\n%s", err, out)
	}
	for _, want := range []string{`SERVER={"name": "proveo-e2e-probe"`, "TOOLS=" + probeTool, "CALL=pong"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("client output lacks %q:\n%s", want, out)
		}
	}
	if got, want := p.seen(), []string{"initialize", "notifications/initialized", "tools/list", "tools/call"}; !slices.Equal(got, want) {
		t.Errorf("probe saw %v, want %v", got, want)
	}
}

func TestARenderedProbeWorkloadNeverMountsTheOperatorsHome(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sbx not available on this host")
	}
	home, _ := os.UserHomeDir()
	_, create := proveoWorkload(t, t.TempDir(), "claudecode", isolatedHome(t))
	for _, a := range create {
		if strings.HasPrefix(a, filepath.Join(home, ".proveo")) || strings.Contains(a, "="+filepath.Join(home, ".proveo")) {
			t.Errorf("the probe workload reaches the operator's proveo home through %q:\n%v", a, create)
		}
	}
}
