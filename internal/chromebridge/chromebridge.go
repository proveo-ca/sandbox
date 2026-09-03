// SPEC: _spec/defs/claudecode/chrome-bridge.puml
//
// Package chromebridge is the HOST half of the Claude in Chrome bridge: a TCP
// listener that, per connection, dials the newest native-host socket at
// /tmp/claude-mcp-browser-bridge-<username>/<pid>.sock and pipes bytes both
// ways, guarded by a per-run token. Its counterpart in the image is
// defs/claudecode/mcp/proveo-lib/chrome-bridge.js.
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
	// Addon is the picker label. Kept beside docker's "(sandbox)" / "(dind)"
	// spelling so the row reads as one family of things the run can hand the agent.
	Addon = "chrome (host browser)"

	// EnvAddr is what the container relay dials: host:port, from the container's
	// point of view (host.docker.internal:<port> on the docker backend).
	EnvAddr = "PROVEO_CHROME_BRIDGE"
	// EnvToken carries the per-run handshake token. Forwarded as a BARE -e so the
	// value rides the client environment rather than the docker argv.
	EnvToken = "PROVEO_CHROME_BRIDGE_TOKEN"

	// handshakePrefix opens every relayed connection; chrome-bridge.js writes the
	// same bytes. The line is bounded so a client cannot stall the reader with an
	// unterminated preamble.
	handshakePrefix  = "PROVEO-CHROME-BRIDGE "
	handshakeMaxLine = 256
	handshakeTimeout = 5 * time.Second

	// SocketDirPrefix is Claude Code's own: `/tmp/claude-mcp-browser-bridge-${username}`.
	SocketDirPrefix = "/tmp/claude-mcp-browser-bridge-"

	// ContainerHost is how the docker backend names the host from inside the
	// agent container; the egress plan maps it to the host gateway when a bridge
	// is on (egress.Options.HostBridge).
	ContainerHost = "host.docker.internal"
)

// Username mirrors Claude Code's rule for the socket directory suffix:
// os.userInfo().username, else $USER, else $USERNAME, else "default".
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

// SocketDir is the directory Claude Code's native host listens in for username.
func SocketDir(username string) string { return SocketDirPrefix + username }

// HostSocketDir is SocketDir for the operator running proveo.
func HostSocketDir() string { return SocketDir(Username(os.Getenv)) }

// The credential gate, mirrored from Claude Code 2.1.258: the scopes are
// SYNTHESISED on the client from where the credential arrived, and
// /api/oauth/validate is named in its log line but never called.
const (
	// EnvOAuthToken shadows the credential store outright: when it is set, Claude
	// Code stops looking and a /login sitting in the same home is never consulted.
	EnvOAuthToken = "CLAUDE_CODE_OAUTH_TOKEN"
	// EnvOAuthScopes is the only thing that decides an env-var session's scopes.
	// Space-separated; unset means the hardcoded fallback below.
	EnvOAuthScopes = "CLAUDE_CODE_OAUTH_SCOPES"
	// EnvOAuthTokenFD is the desktop app's delivery path. Its scope default
	// carries user:ccr_inference, so that shape passes without saying anything.
	EnvOAuthTokenFD = "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"
	// EnvAPIKey buys inference and no OAuth account, so there are no scopes to
	// accept — unless a login is persisted, which the key does not displace.
	EnvAPIKey = "ANTHROPIC_API_KEY"
)

// BrowserScopes is the accepted set. Any ONE of them is enough.
var BrowserScopes = []string{"user:profile", "user:office", "user:ccr_inference"}

// envTokenFallbackScopes is what Claude Code assumes when EnvOAuthToken arrives
// with no EnvOAuthScopes: inference only, which accepts nothing.
var envTokenFallbackScopes = []string{"user:inference"}

// ScopeGate reports why Claude Code would refuse to wire the browser integration
// for the session this run is about to start, or "" when it would wire it — a
// shape it cannot classify included.
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
			" — set it to " + strings.Join(BrowserScopes, "/") + ", or unset it and use /login"
	case get(EnvOAuthTokenFD) != "":
		return "" // scope default carries user:ccr_inference
	case hasPersistedLogin:
		return "" // real /login scopes include user:profile
	case get(EnvAPIKey) != "":
		return EnvAPIKey + " is not an OAuth account — sign in with /login"
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

// NewestSocket returns the most recently modified *.sock in dir. Claude Code
// lists the directory the same way; when Chrome has restarted the native host,
// the newest socket is the live one and the older files are leftovers.
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

// Available reports whether a Claude in Chrome native host is reachable right
// now, and if not, what the operator has to do about it.
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

// BindAddr picks where the relay listens: loopback where host.docker.internal
// routes there, the bridge address on Linux.
func BindAddr() string {
	if runtime.GOOS == "linux" {
		return "0.0.0.0:0"
	}
	return "127.0.0.1:0"
}

// Start listens on bind and relays each authenticated connection to the newest
// native host socket in dir at the time the connection arrives.
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
// form: the address as KEY=VALUE, the token as a bare name (the caller sets it in
// its own environment, see SetTokenEnv).
func (r *Relay) Env() []string { return []string{EnvAddr + "=" + r.ContainerAddr(), EnvToken} }

// SetTokenEnv exports the token into this process so a bare `-e PROVEO_CHROME_BRIDGE_TOKEN`
// forwards it without the value appearing on the docker command line.
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

// Handshake is the first line the container relay sends for token.
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
	// Half-close is not something the native messaging stream uses; the first
	// side to finish ends the conversation.
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseRead()
	}
	if uc, ok := up.(*net.UnixConn); ok {
		_ = uc.CloseRead()
	}
}
