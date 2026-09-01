// SPEC: _spec/defs/claudecode/chrome-bridge.puml
package chromebridge

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortTempDir stays under the 104-byte Unix socket path limit that t.TempDir()
// on macOS (/var/folders/…) walks straight into.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "cb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// fakeNativeHost stands in for Claude Code's `claude --chrome-native-host`: a
// Unix socket server that echoes whatever it is sent.
func fakeNativeHost(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
}

func startRelay(t *testing.T, dir string) *Relay {
	t.Helper()
	r, err := Start("127.0.0.1:0", dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func dial(t *testing.T, r *Relay) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", r.Addr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}

func TestRelayCarriesBytesBothWaysAfterTheHandshake(t *testing.T) {
	dir := shortTempDir(t)
	fakeNativeHost(t, filepath.Join(dir, "4242.sock"))
	r := startRelay(t, dir)

	c := dial(t, r)
	if _, err := io.WriteString(c, Handshake(r.Token())+"hello native host\n"); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if got != "hello native host\n" {
		t.Fatalf("echo = %q, want the payload back without the handshake line", got)
	}
}

func TestRelayClosesOnAWrongToken(t *testing.T) {
	dir := shortTempDir(t)
	fakeNativeHost(t, filepath.Join(dir, "1.sock"))
	r := startRelay(t, dir)

	c := dial(t, r)
	_, _ = io.WriteString(c, Handshake("not-the-token")+"hello\n")
	if n, err := c.Read(make([]byte, 16)); err == nil || n != 0 {
		t.Fatalf("a wrong token must be closed before any byte flows; read %d bytes, err=%v", n, err)
	}
}

func TestRelayClosesWithoutAHandshakeLine(t *testing.T) {
	dir := shortTempDir(t)
	fakeNativeHost(t, filepath.Join(dir, "1.sock"))
	r := startRelay(t, dir)

	c := dial(t, r)
	_, _ = io.WriteString(c, "GET / HTTP/1.0\r\n\r\n")
	if n, err := c.Read(make([]byte, 16)); err == nil || n != 0 {
		t.Fatalf("a non-handshake preamble must be closed; read %d bytes, err=%v", n, err)
	}
}

func TestRelayClosesWhenNoNativeHostIsListening(t *testing.T) {
	dir := shortTempDir(t) // no .sock inside
	r := startRelay(t, dir)

	c := dial(t, r)
	_, _ = io.WriteString(c, Handshake(r.Token())+"hello\n")
	if n, err := c.Read(make([]byte, 16)); err == nil || n != 0 {
		t.Fatalf("with no native host the relay must close rather than hang; read %d bytes, err=%v", n, err)
	}
}

func TestNewestSocketPicksTheLatestAndIgnoresNonSockets(t *testing.T) {
	dir := shortTempDir(t)
	old := filepath.Join(dir, "100.sock")
	newer := filepath.Join(dir, "200.sock")
	for _, p := range []string{old, newer} {
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
	}
	if err := os.WriteFile(filepath.Join(dir, "300.sock"), []byte("a regular file"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got, err := NewestSocket(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Fatalf("NewestSocket = %s, want %s", got, newer)
	}
}

func TestAvailableNamesWhatTheOperatorMustDo(t *testing.T) {
	if ok, why := Available("/tmp/definitely-not-a-claude-bridge-dir"); ok || !strings.Contains(why, "claude --chrome") {
		t.Fatalf("missing dir: ok=%v why=%q", ok, why)
	}
	dir := shortTempDir(t)
	if ok, why := Available(dir); ok || !strings.Contains(why, "not running") {
		t.Fatalf("empty dir: ok=%v why=%q", ok, why)
	}
	fakeNativeHost(t, filepath.Join(dir, "7.sock"))
	if ok, why := Available(dir); !ok || why != "" {
		t.Fatalf("live socket: ok=%v why=%q", ok, why)
	}
}

func TestSocketDirAndEnvMatchWhatTheContainerRelayExpects(t *testing.T) {
	if got := SocketDir("pluvo"); got != "/tmp/claude-mcp-browser-bridge-pluvo" {
		t.Fatalf("SocketDir = %q", got)
	}
	if u := Username(func(string) string { return "" }); u == "" {
		t.Fatal("Username must never be empty")
	}
	if u := Username(func(k string) string {
		if k == "USER" {
			return "envuser"
		}
		return ""
	}); u == "" {
		t.Fatal("Username with env fallback must never be empty")
	}
	dir := shortTempDir(t)
	r := startRelay(t, dir)
	env := r.Env()
	if len(env) != 2 || env[0] != EnvAddr+"="+r.ContainerAddr() || env[1] != EnvToken {
		t.Fatalf("Env = %v: the address is KEY=VALUE, the token a bare name", env)
	}
	if !strings.HasPrefix(r.ContainerAddr(), ContainerHost+":") {
		t.Fatalf("ContainerAddr = %q", r.ContainerAddr())
	}
	if err := r.SetTokenEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(EnvToken) != r.Token() {
		t.Fatal("SetTokenEnv must export the token for the bare -e forward")
	}
	_ = r.Close()
	if os.Getenv(EnvToken) != "" {
		t.Fatal("Close must scrub the token from the environment")
	}
}
