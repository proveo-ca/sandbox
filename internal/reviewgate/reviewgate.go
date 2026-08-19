// Package reviewgate is the host-side consent gate for the review tier.
//
// SPEC: _spec/internal/reviewgate/pty-review-proxy.puml, _spec/internal/reviewgate/transport-portability.puml, _spec/internal/egress/egress-tiers.puml
package reviewgate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SocketName is the gate's unix socket basename inside the egress state dir.
const SocketName = "review.sock"

const DefaultDeadline = 20 * time.Second

// Verdict is the answer sent back over the socket.
type Verdict string

const (
	Allow Verdict = "allow"
	Deny  Verdict = "deny"
)

type Asker func(host, port string) bool

// Gate answers host:port questions, caching each answer for the session.
type Gate struct {
	Ask      Asker
	Deadline time.Duration

	mu       sync.Mutex
	decided  map[string]Verdict
	listener net.Listener
	path     string
	asking   sync.Mutex
}

// New returns a Gate. A nil Asker means nothing can grant consent, so every
// uncached host is denied — the correct posture for a headless run.
func New(ask Asker) *Gate {
	return &Gate{Ask: ask, Deadline: DefaultDeadline, decided: map[string]Verdict{}}
}

const maxSockPath = 100

func Path(dir string) string {
	full := filepath.Join(dir, SocketName)
	if len(full) <= maxSockPath {
		return full
	}
	// Portable by construction: short on Linux (/tmp) and macOS (/var/folders/...),
	// and well under sun_path on both (104 on BSD/macOS, 108 on Linux).
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(os.TempDir(), "proveo-review-"+hex.EncodeToString(sum[:6]), SocketName)
}

// Listen binds the socket in dir and serves until Close. The socket is 0600: it
// grants network reach, so only the invoking user may ask through it.
func (g *Gate) Listen(dir string) error {
	path := Path(dir)
	// 0700 on the socket's own directory: it is bind-mounted into a sidecar, so it
	// must contain nothing else and be reachable by nobody else.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("review gate: mkdir: %w", err)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("review gate: listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("review gate: chmod: %w", err)
	}
	g.mu.Lock()
	g.listener, g.path = ln, path
	g.mu.Unlock()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go g.serve(conn)
		}
	}()
	return nil
}

// Close stops serving and removes the socket.
func (g *Gate) Close() error {
	g.mu.Lock()
	ln, path := g.listener, g.path
	g.listener, g.path = nil, ""
	g.mu.Unlock()
	if ln == nil {
		return nil
	}
	err := ln.Close()
	// Unlink explicitly: Go removes the socket on a clean Close, but a killed run
	// leaves it, and every stale socket is a directory the next run will not reuse.
	if path != "" {
		_ = os.Remove(path)
		_ = os.Remove(filepath.Dir(path)) // no-op unless it is our empty fallback dir
	}
	return err
}

// serve speaks one line per request: "host port\n" in, "allow\n"/"deny\n" out.
func (g *Gate) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		host, port := splitRequest(sc.Text())
		if host == "" {
			_, _ = fmt.Fprintf(conn, "%s\n", Deny)
			continue
		}
		_, _ = fmt.Fprintf(conn, "%s\n", g.Decide(host, port))
	}
}

func splitRequest(line string) (host, port string) {
	fields := strings.Fields(strings.TrimSpace(line))
	switch len(fields) {
	case 0:
		return "", ""
	case 1:
		return fields[0], "443"
	default:
		return fields[0], fields[1]
	}
}

func (g *Gate) Decide(host, port string) Verdict {
	g.mu.Lock()
	if v, ok := g.decided[host]; ok {
		g.mu.Unlock()
		return v
	}
	g.mu.Unlock()

	// One question at a time: concurrent CONNECTs to the same new host must not
	// raise two overlays.
	g.asking.Lock()
	defer g.asking.Unlock()
	g.mu.Lock()
	if v, ok := g.decided[host]; ok {
		g.mu.Unlock()
		return v
	}
	g.mu.Unlock()

	v := g.ask(host, port)
	g.mu.Lock()
	g.decided[host] = v
	g.mu.Unlock()
	return v
}

func (g *Gate) ask(host, port string) Verdict {
	if g.Ask == nil {
		return Deny
	}
	deadline := g.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}
	res := make(chan bool, 1)
	go func() { res <- g.Ask(host, port) }()
	select {
	case ok := <-res:
		if ok {
			return Allow
		}
		return Deny
	case <-time.After(deadline):
		return Deny
	}
}

// Decisions returns a copy of the session's answers, for reporting.
func (g *Gate) Decisions() map[string]Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]Verdict, len(g.decided))
	for k, v := range g.decided {
		out[k] = v
	}
	return out
}

func AskOverSocket(socket, host, port string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprintf(conn, "%s %s\n", host, port); err != nil {
		return false
	}
	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		return false
	}
	return strings.TrimSpace(sc.Text()) == string(Allow)
}
