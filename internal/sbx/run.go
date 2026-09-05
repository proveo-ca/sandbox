package sbx

import (
	"fmt"
	"strconv"
	"strings"
)

func Exists(name string) bool {
	if name == "" {
		return false
	}
	out, err := sh.SandboxList()
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range strings.Fields(line) {
			if f == name {
				return true
			}
		}
	}
	return false
}

func Running(name string) bool {
	if name == "" {
		return false
	}
	out, err := sh.SandboxList()
	if err != nil {
		return false
	}
	return statusOf(string(out), name) == "running"
}

func statusOf(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == name {
			return strings.ToLower(f[2])
		}
	}
	return ""
}

// NamePrefix is what proveo calls its own sandboxes (run.go builds the sid as
// "proveo-<unix>-<pid>").
const NamePrefix = "proveo-"

// SPEC: _spec/_plans/config-seeding-and-persistence.puml
func RunningNames() (names []string, ok bool) {
	if _, err := lookPath(Binary); err != nil {
		return nil, true // sbx absent: there are no sandboxes, and that is a fact
	}
	out, err := sh.SandboxList()
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[0], NamePrefix) {
			continue
		}
		if strings.ToLower(f[2]) == "running" {
			names = append(names, f[0])
		}
	}
	return names, true
}

func StoredSecretNames() []string {
	out, err := sh.SecretList()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[2] == "NAME" {
			continue
		}
		names = append(names, f[2])
	}
	return names
}

func ImageEntrypoint(image string) []string { return sh.ImageEntrypoint(image) }

// Mount is a workspace bind into the sandbox VM.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

func AgentName(target string) string { return "proveo-" + target }

// RunConfig describes one agent run on the sbx backend.
type RunConfig struct {
	Name    string // sandbox/session name; empty lets sbx assign one
	Agent   string
	KitDir  string // directory holding the rendered Kit spec.yaml
	Image   string // template image, passed as -t
	Memory  string // -m limit; empty leaves sbx's host-derived default in place
	CPUs    int    // --cpus; zero leaves sbx's default (every host CPU) in place
	Clone   bool
	Mounts  []Mount // workspace binds, passed POSITIONALLY
	Publish []string
	Env     []string // non-secret KEY=VALUE (or bare NAME) passthrough
	Command []string // trailing agent command (after "--")
}

func CreateArgs(cfg RunConfig) []string {
	cfg.Command = nil
	return append([]string{"create"}, RunArgs(cfg)[1:]...)
}

func RunArgs(cfg RunConfig) []string {
	args := []string{"run"}
	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}
	if cfg.KitDir != "" {
		args = append(args, "--kit", cfg.KitDir)
	}
	if cfg.Image != "" {
		args = append(args, "-t", cfg.Image)
	}
	if cfg.Memory != "" {
		args = append(args, "-m", cfg.Memory)
	}
	if cfg.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(cfg.CPUs))
	}
	if cfg.Clone {
		args = append(args, "--clone")
	}
	for _, p := range cfg.Publish {
		args = append(args, "-p", p)
	}
	for _, e := range cfg.Env {
		args = append(args, "-e", e)
	}
	if cfg.Agent != "" {
		args = append(args, cfg.Agent)
	}
	for _, m := range cfg.Mounts {
		spec := m.Host
		if m.ReadOnly {
			spec += ":ro"
		}
		args = append(args, spec)
	}
	if len(cfg.Command) > 0 {
		args = append(args, "--")
		args = append(args, cfg.Command...)
	}
	return args
}

func CloneRemote(name string) string { return "sandbox-" + name }

func CloneRefs(name string) string { return "refs/proveo/" + name }

// SPEC: _spec/internal/sbx/clone-workspace.puml
func CloneSnapshotArgs(name, workdir string) []string {
	return []string{"exec", "-w", workdir, name, "--", "bash", "-c",
		"git add -A && (git diff --cached --quiet || git -c user.name=proveo -c user.email=proveo@sandbox " +
			"commit -q -m 'proveo: uncommitted work at teardown (left in the clone by the agent)')"}
}

func CloneFetchArgs(repoRoot, name string) []string {
	return []string{"-C", repoRoot, "fetch", "--no-tags", "--quiet", CloneRemote(name),
		"+refs/heads/*:" + CloneRefs(name) + "/*"}
}

const (
	CDPRelayPort   = 9222 // the relay listens here, on every interface; this is what is published
	CDPBrowserPort = 9223 // Chromium's own DevTools port, loopback only
)

func BrowserCDPArgs(existing string) string {
	flag := fmt.Sprintf("--remote-debugging-port=%d", CDPBrowserPort)
	existing = strings.TrimSpace(existing)
	switch {
	case existing == "":
		return "--no-sandbox," + flag
	case strings.Contains(existing, "--remote-debugging-port="):
		return existing // the operator pinned their own; theirs wins
	}
	return existing + "," + flag
}

const cdpRelay = `
import socket,sys,threading
L,T=int(sys.argv[1]),int(sys.argv[2])
def pipe(a,b):
    try:
        while True:
            d=a.recv(65536)
            if not d: break
            b.sendall(d)
    except Exception: pass
    finally:
        for s in (a,b):
            try: s.close()
            except Exception: pass
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(("0.0.0.0",L)); s.listen(64)
while True:
    c,_=s.accept()
    try: u=socket.create_connection(("127.0.0.1",T))
    except Exception:
        c.close(); continue
    for a,b in ((c,u),(u,c)):
        threading.Thread(target=pipe,args=(a,b),daemon=True).start()
`

func CDPRelayArgs(name string) []string {
	return []string{"exec", "-w", "/", name, "--", "python3", "-c", cdpRelay,
		strconv.Itoa(CDPRelayPort), strconv.Itoa(CDPBrowserPort)}
}

// CloneLiftNothing is the exit status CloneLiftArgs reserves for "the agent
// wrote nothing there": the directory does not exist in the clone, so there is
// nothing to unpack and nothing went wrong.
const CloneLiftNothing = 3

// SPEC: _spec/internal/sbx/clone-workspace.puml
func CloneLiftArgs(name, workdir, rel string) []string {
	return []string{"exec", "-w", "/", name, "--", "bash", "-c",
		"cd " + bashQuote(workdir) + " && { [ -d " + bashQuote(rel) + " ] || exit " +
			strconv.Itoa(CloneLiftNothing) + "; } && tar -cf - " + bashQuote(rel)}
}

func bashQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SPEC: _spec/internal/sbx/state-sync.puml, _spec/_plans/config-seeding-and-persistence.puml
func SaveStateArgs(name string) []string {
	return []string{"exec", "-w", "/", name, "--", "bash", "-c",
		". /entrypoint-lib.sh" +
			"; { proveo_sync_config save || true; }" +
			"; { proveo_sync_tools save || true; }" +
			"; proveo_sync_state save"}
}

func RemoveArgs(name string) []string {
	return []string{"rm", "--force", name}
}

func NotFound(out string) bool {
	return strings.Contains(strings.ToLower(out), "not found")
}

// SPEC: _spec/internal/sbx/oauth-provisioning.puml
var authLoginArgs = map[string][]string{
	"claude": {"auth", "login"},
}

func AuthLoginArgs(agent, workspace string) []string {
	sub, ok := authLoginArgs[agent]
	if !ok || agent == "" || workspace == "" {
		return nil
	}
	args := []string{"run", agent, workspace, "--"}
	return append(args, sub...)
}
