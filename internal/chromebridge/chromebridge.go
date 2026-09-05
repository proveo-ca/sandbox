// SPEC: _spec/defs/claudecode/chrome-bridge.puml Package chromebridge is the
// HOST half of the Claude in Chrome bridge: a TCP listener that, per
// connection, dials the newest native-host socket at
// /tmp/claude-mcp-browser-bridge-<username>/<pid>.sock and pipes bytes both
// ways, guarded by a per-run token.
//
// SPEC: _spec/defs/claudecode/chrome-bridge.puml
package chromebridge

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	Addon = "claude-in-chrome"

	EnvAddr  = "PROVEO_CHROME_BRIDGE"
	EnvToken = "PROVEO_CHROME_BRIDGE_TOKEN"

	handshakePrefix  = "PROVEO-CHROME-BRIDGE "
	handshakeMaxLine = 256
	handshakeTimeout = 5 * time.Second

	SocketDirPrefix = "/tmp/claude-mcp-browser-bridge-"

	ContainerHost = "host.docker.internal"
)

func TierSupported(mode, credentials string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "open") &&
		strings.EqualFold(strings.TrimSpace(credentials), "forward")
}

// TierWhy is the one sentence the picker and the run both print when
// TierSupported says no.
const TierWhy = "needs egress open + credentials forward"

func Username(getenv func(string) string) string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, k := range []string{"USER", "USERNAME"} {
		if v := getenv(k); v != "" {
			return v
		}
	}
	return "default"
}

func SocketDir(username string) string { return SocketDirPrefix + username }

func HostSocketDir() string { return SocketDir(Username(os.Getenv)) }

const (
	EnvOAuthToken   = "CLAUDE_CODE_OAUTH_TOKEN"
	EnvOAuthScopes  = "CLAUDE_CODE_OAUTH_SCOPES"
	EnvOAuthTokenFD = "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"
	EnvAPIKey       = "ANTHROPIC_API_KEY"
)

// BrowserScopes is the accepted set.
var BrowserScopes = []string{"user:profile", "user:office", "user:ccr_inference"}

var envTokenFallbackScopes = []string{"user:inference"}

func ScopeGate(lookup func(string) string, hasPersistedLogin bool) string {
	if lookup == nil {
		return ""
	}
	get := func(k string) string { return strings.TrimSpace(lookup(k)) }
	switch {
	case get(EnvOAuthToken) != "":
		scopes := strings.Fields(get(EnvOAuthScopes))
		if len(scopes) == 0 {
			scopes = envTokenFallbackScopes
		}
		if hasBrowserScope(scopes) {
			return ""
		}
		if len(strings.Fields(get(EnvOAuthScopes))) > 0 {
			return EnvOAuthScopes + " names none of " + scopeList() + " — add one"
		}
		return EnvOAuthToken + " has no " + EnvOAuthScopes +
			" — set it to " + strings.Join(BrowserScopes, "/") +
			", or /login INSIDE the run"
	case get(EnvOAuthTokenFD) != "":
		return "" // scope default carries user:ccr_inference
	case hasPersistedLogin:
		return "" // real /login scopes include user:profile
	case get(EnvAPIKey) != "":
		return EnvAPIKey + " is not an OAuth account — /login INSIDE the run"
	}
	return ""
}

func hasBrowserScope(scopes []string) bool {
	for _, s := range scopes {
		for _, want := range BrowserScopes {
			if s == want {
				return true
			}
		}
	}
	return false
}

func scopeList() string { return strings.Join(BrowserScopes, ", ") }

func NewestSocket(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var socks []cand
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := os.Stat(p)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		socks = append(socks, cand{p, fi.ModTime()})
	}
	if len(socks) == 0 {
		return "", fmt.Errorf("no native host socket in %s", dir)
	}
	sort.Slice(socks, func(i, j int) bool { return socks[i].mod.After(socks[j].mod) })
	return socks[0].path, nil
}

func Available(dir string) (ok bool, why string) {
	if _, err := NewestSocket(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "no native host socket — open Chrome with the Claude extension, then run `claude --chrome` once on the host"
		}
		return false, "Chrome is not running with the Claude extension — start it"
	}
	return true, ""
}

// Relay is the host-side TCP listener.
type Relay struct {
	ln    net.Listener
	dir   string
	token string
	wg    sync.WaitGroup
	once  sync.Once
	errf  func(string, ...any)
}

func BindAddr() string {
	if runtime.GOOS == "linux" {
		return "0.0.0.0:0"
	}
	return "127.0.0.1:0"
}

func Start(bind, dir string, errf func(string, ...any)) (*Relay, error) {
	if errf == nil {
		errf = func(string, ...any) {}
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, fmt.Errorf("chrome bridge: listen %s: %w", bind, err)
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chrome bridge: token: %w", err)
	}
	r := &Relay{ln: ln, dir: dir, token: hex.EncodeToString(tok), errf: errf}
	r.wg.Add(1)
	go r.serve()
	return r, nil
}

// Addr is the listening address, e.g. 127.0.0.1:54321.
func (r *Relay) Addr() string { return r.ln.Addr().String() }

// Port is the listening port.
func (r *Relay) Port() int { return r.ln.Addr().(*net.TCPAddr).Port }

// Token is the handshake secret this relay accepts.
func (r *Relay) Token() string { return r.token }

// ContainerAddr is what the container relay must dial.
func (r *Relay) ContainerAddr() string { return ContainerHost + ":" + strconv.Itoa(r.Port()) }

// Env are the two variables the agent container needs, in runner.Config.Env
// form: the address as KEY=VALUE, the token as a bare name (the caller sets it
// in its own environment, see SetTokenEnv).
func (r *Relay) Env() []string { return []string{EnvAddr + "=" + r.ContainerAddr(), EnvToken} }

// SetTokenEnv exports the token into this process so a bare `-e
// PROVEO_CHROME_BRIDGE_TOKEN` forwards it without the value appearing on the
// docker command line.
func (r *Relay) SetTokenEnv() error { return os.Setenv(EnvToken, r.token) }

// Close stops accepting and waits for in-flight connections to unwind.
func (r *Relay) Close() error {
	var err error
	r.once.Do(func() {
		err = r.ln.Close()
		_ = os.Unsetenv(EnvToken)
	})
	r.wg.Wait()
	return err
}

func (r *Relay) serve() {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(c)
		}()
	}
}

func Handshake(token string) string { return handshakePrefix + token + "\n" }

func (r *Relay) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReaderSize(c, handshakeMaxLine)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, handshakePrefix) {
		r.errf("chrome bridge: connection from %s sent no handshake; closed", c.RemoteAddr())
		return
	}
	got := strings.TrimSuffix(strings.TrimPrefix(line, handshakePrefix), "\n")
	if subtle.ConstantTimeCompare([]byte(got), []byte(r.token)) != 1 {
		r.errf("chrome bridge: connection from %s presented a wrong token; closed", c.RemoteAddr())
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	sock, err := NewestSocket(r.dir)
	if err != nil {
		r.errf("chrome bridge: %v — is Chrome open with the Claude extension?", err)
		return
	}
	up, err := net.DialTimeout("unix", sock, handshakeTimeout)
	if err != nil {
		r.errf("chrome bridge: dial %s: %v", sock, err)
		return
	}
	defer func() { _ = up.Close() }()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseRead()
	}
	if uc, ok := up.(*net.UnixConn); ok {
		_ = uc.CloseRead()
	}
}
