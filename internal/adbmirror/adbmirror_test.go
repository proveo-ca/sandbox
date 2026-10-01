// SPEC: _spec/internal/sbx/host-android-adb.puml
package adbmirror

import (
	"context"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer speaks the adb smart-socket framing: a forward list, transports, and an echo target.
type fakeServer struct {
	addr string
	mu   sync.Mutex
	list string
	seen []string
}

func (f *fakeServer) setList(s string) { f.mu.Lock(); f.list = s; f.mu.Unlock() }

func (f *fakeServer) services() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func startFake(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &fakeServer{addr: ln.Addr().String()}
	read := func(c net.Conn) (string, error) {
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(c, hdr); err != nil {
			return "", err
		}
		n, _ := strconv.ParseUint(string(hdr), 16, 32)
		b := make([]byte, n)
		_, err := io.ReadFull(c, b)
		return string(b), err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				for {
					svc, err := read(c)
					if err != nil {
						return
					}
					f.mu.Lock()
					f.seen = append(f.seen, svc)
					list := f.list
					f.mu.Unlock()
					switch {
					case svc == "host:list-forward":
						fmt.Fprintf(c, "OKAY%04x%s", len(list), list)
						return
					case strings.HasPrefix(svc, "host:transport:"):
						_, _ = c.Write([]byte("OKAY"))
					case svc == "localabstract:echo":
						_, _ = c.Write([]byte("OKAY"))
						_, _ = io.Copy(c, c)
						return
					default:
						msg := "closed"
						fmt.Fprintf(c, "FAIL%04x%s", len(msg), msg)
						return
					}
				}
			}()
		}
	}()
	return f
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

func runMirror(t *testing.T, addr string) func() string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var mu sync.Mutex
	var log strings.Builder
	go func() {
		Run(ctx, addr, 20*time.Millisecond, func(f string, a ...any) {
			mu.Lock()
			fmt.Fprintf(&log, f+"\n", a...)
			mu.Unlock()
		})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return func() string { mu.Lock(); defer mu.Unlock(); return log.String() }
}

func dialEventually(t *testing.T, port int) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond); err == nil {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing listens on 127.0.0.1:%d", port)
	return nil
}

func TestParseKeepsOnlyTCPForwards(t *testing.T) {
	got := parse("emulator-5554 tcp:43577 localabstract:mobilecli-server\n" +
		"emulator-5554 localabstract:x tcp:1\n" +
		"emulator-5554 tcp:nope localabstract:y\n" +
		"garbage\n")
	want := []Forward{{Serial: "emulator-5554", Port: 43577, Target: "localabstract:mobilecli-server"}}
	if !slices.Equal(got, want) {
		t.Errorf("parse = %v; want %v", got, want)
	}
}

func TestListReadsTheServersForwards(t *testing.T) {
	f := startFake(t)
	f.setList("emulator-5554 tcp:5000 localabstract:echo\n")
	got, err := List(f.addr)
	if err != nil || len(got) != 1 || got[0].Port != 5000 {
		t.Errorf("List = %v, %v", got, err)
	}
}

func TestAMirroredPortCarriesBytesThroughTheServer(t *testing.T) {
	f := startFake(t)
	port := freePort(t)
	f.setList(fmt.Sprintf("emulator-5554 tcp:%d localabstract:echo\n", port))
	runMirror(t, f.addr)

	c := dialEventually(t, port)
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	svcs := f.services()
	if !slices.Contains(svcs, "host:transport:emulator-5554") || !slices.Contains(svcs, "localabstract:echo") {
		t.Errorf("server saw %v; want the transport then the forward's own target", svcs)
	}
	if slices.ContainsFunc(svcs, func(s string) bool { return strings.HasPrefix(s, "host:forward") }) {
		t.Errorf("the mirror must not create forwards of its own: %v", svcs)
	}
}

func TestARemovedForwardStopsListening(t *testing.T) {
	f := startFake(t)
	port := freePort(t)
	f.setList(fmt.Sprintf("emulator-5554 tcp:%d localabstract:echo\n", port))
	runMirror(t, f.addr)
	_ = dialEventually(t, port).Close()

	f.setList("")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("127.0.0.1:%d still listens after its forward was removed", port)
}

func TestATakenPortIsRetriedOnceFree(t *testing.T) {
	f := startFake(t)
	squat, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := squat.Addr().(*net.TCPAddr).Port
	f.setList(fmt.Sprintf("emulator-5554 tcp:%d localabstract:echo\n", port))
	runMirror(t, f.addr)
	time.Sleep(100 * time.Millisecond)
	_ = squat.Close()

	c := dialEventually(t, port)
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = c.Write([]byte("ok"))
	got := make([]byte, 2)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ok" {
		t.Fatalf("after the squatter left, echo = %q, %v", got, err)
	}
}

func TestADeadTargetClosesTheClientAndSaysWhy(t *testing.T) {
	f := startFake(t)
	port := freePort(t)
	f.setList(fmt.Sprintf("emulator-5554 tcp:%d localabstract:gone\n", port))
	log := runMirror(t, f.addr)

	c := dialEventually(t, port)
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if n, err := c.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Errorf("read = %d, %v; want the connection closed", n, err)
	}
	_ = c.Close()
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(log(), "localabstract:gone: closed") {
		t.Errorf("log = %q; want the server's refusal", log())
	}
}
