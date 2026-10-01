// SPEC: _spec/internal/sbx/host-android-adb.puml
package adbmirror

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Forward is one row of the adb server's forward list.
type Forward struct {
	Serial string
	Port   int
	Target string
}

var dialTimeout = 5 * time.Second

func request(c net.Conn, service string) error {
	if _, err := fmt.Fprintf(c, "%04x%s", len(service), service); err != nil {
		return err
	}
	status := make([]byte, 4)
	if _, err := io.ReadFull(c, status); err != nil {
		return err
	}
	if string(status) == "OKAY" {
		return nil
	}
	size := make([]byte, 4)
	if _, err := io.ReadFull(c, size); err != nil {
		return fmt.Errorf("%s: %s", service, status)
	}
	n, _ := strconv.ParseUint(string(size), 16, 32)
	msg := make([]byte, n)
	_, _ = io.ReadFull(c, msg)
	return fmt.Errorf("%s: %s", service, msg)
}

// List reads the forwards the adb server at addr holds.
func List(addr string) ([]Forward, error) {
	c, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(dialTimeout))
	if err := request(c, "host:list-forward"); err != nil {
		return nil, err
	}
	size := make([]byte, 4)
	if _, err := io.ReadFull(c, size); err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(string(size), 16, 32)
	if err != nil {
		return nil, fmt.Errorf("adb: bad length %q", size)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c, body); err != nil {
		return nil, err
	}
	return parse(string(body)), nil
}

func parse(body string) []Forward {
	var out []Forward
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || !strings.HasPrefix(f[1], "tcp:") {
			continue
		}
		port, err := strconv.Atoi(strings.TrimPrefix(f[1], "tcp:"))
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		out = append(out, Forward{Serial: f[0], Port: port, Target: f[2]})
	}
	return out
}

// Open reaches target on serial through the adb server at addr, as `adb forward` would.
func Open(addr string, fw Forward) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(dialTimeout))
	if err := request(c, "host:transport:"+fw.Serial); err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := request(c, fw.Target); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

type mirror struct {
	fw Forward
	ln net.Listener
}

func serve(addr string, m mirror, logf func(string, ...any)) {
	for {
		c, err := m.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			up, err := Open(addr, m.fw)
			if err != nil {
				logf("adb-mirror: 127.0.0.1:%d -> %s %s: %v", m.fw.Port, m.fw.Serial, m.fw.Target, err)
				_ = c.Close()
				return
			}
			pipe(c, up)
		}()
	}
}

// Run mirrors every tcp forward on the adb server at addr onto 127.0.0.1 here,
// carried through the server itself, until ctx ends.
func Run(ctx context.Context, addr string, poll time.Duration, logf func(string, ...any)) {
	live := map[Forward]mirror{}
	defer func() {
		for _, m := range live {
			_ = m.ln.Close()
		}
	}()
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		reconcile(addr, live, logf)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func reconcile(addr string, live map[Forward]mirror, logf func(string, ...any)) {
	fws, err := List(addr)
	if err != nil {
		return
	}
	want := map[Forward]bool{}
	for _, fw := range fws {
		want[fw] = true
		if _, ok := live[fw]; ok {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fw.Port)))
		if err != nil {
			continue
		}
		m := mirror{fw: fw, ln: ln}
		live[fw] = m
		logf("adb-mirror: 127.0.0.1:%d -> %s %s", fw.Port, fw.Serial, fw.Target)
		go serve(addr, m, logf)
	}
	for fw, m := range live {
		if !want[fw] {
			_ = m.ln.Close()
			delete(live, fw)
		}
	}
}
