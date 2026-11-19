// SPEC: _spec/_plans/isolated-browser-profiles.puml
package guestbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ModeEnv      = "PROVEO_BROWSER_MODE"
	Isolated     = "isolated"
	Off          = "none"
	HostChrome   = "host-chrome"
	AgentPort    = 9224
	AgentAddress = "127.0.0.1:9224"
	AgentURL     = "http://" + AgentAddress
	ProfileRoot  = "/var/lib/proveo/browser"
	ViewportPort = 9222
	TokenHeader  = "X-Proveo-Viewport"
)

type Options struct {
	Executable string
	Root       string
	Persistent bool
	Token      string
}

func Executable() string {
	if path := os.Getenv("AGENT_BROWSER_EXECUTABLE_PATH"); path != "" {
		return path
	}
	path, _ := os.ReadFile("/etc/hermes/chromium-executable-path")
	return strings.TrimSpace(string(path))
}

type browser struct {
	mu         sync.Mutex
	executable string
	dir        string
	proc       *exec.Cmd
	done       chan struct{}
	ws         string
	closed     bool
}

func (b *browser) endpoint(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return "", errors.New("browser run ended")
	}
	if b.proc != nil {
		select {
		case <-b.done:
			b.proc, b.ws = nil, ""
		default:
		}
	}
	if b.ws != "" {
		return b.ws, nil
	}
	activePort := filepath.Join(b.dir, "DevToolsActivePort")
	if err := os.Remove(activePort); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	b.proc = exec.Command(b.executable,
		"--headless", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking",
		"--no-first-run", "--no-default-browser-check", "--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=0", "--user-data-dir="+b.dir, "--window-size=1280,800", "about:blank")
	b.proc.Stderr = os.Stderr
	groupChromium(b.proc)
	if err := b.proc.Start(); err != nil {
		b.proc = nil
		return "", err
	}
	proc, done := b.proc, make(chan struct{})
	b.done = done
	go func() {
		_ = proc.Wait()
		close(done)
	}()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(activePort); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 && strings.HasPrefix(fields[1], "/devtools/browser/") {
				b.ws = "ws://127.0.0.1:" + fields[0] + fields[1]
				return b.ws, nil
			}
		}
		select {
		case <-done:
			return "", errors.New("browser exited before CDP became ready")
		case <-ctx.Done():
			b.stopProcess()
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *browser) stopProcess() {
	if b.proc == nil {
		return
	}
	select {
	case <-b.done:
	default:
		_ = b.proc.Process.Signal(syscall.SIGTERM)
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			killChromium(b.proc)
			<-b.done
		}
	}
	b.proc, b.ws = nil, ""
}

func CheckExecutable(path string) error {
	if info, err := os.Stat(path); path == "" || err != nil || info.Mode()&0o111 == 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("no executable Chromium at %q", path)
	}
	return nil
}

func (b *browser) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if b.ws != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = command(ctx, b.ws, "Browser.close", nil, nil)
		cancel()
		select {
		case <-b.done:
		case <-time.After(2 * time.Second):
		}
	}
	b.stopProcess()
}

// Serve holds one guest browser until ctx ends.
func Serve(ctx context.Context, agent, viewport net.Listener, opts Options) (serveErr error) {
	defer func() { _ = agent.Close() }()
	defer func() { _ = viewport.Close() }()
	if opts.Token == "" {
		return errors.New("isolated browser requires a run token")
	}
	if err := CheckExecutable(opts.Executable); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Root, 0o700); err != nil {
		return err
	}
	var dir string
	var err error
	if opts.Persistent {
		dir = filepath.Join(opts.Root, "named")
		err = os.MkdirAll(dir, 0o700)
	} else {
		if err := ClearFresh(opts.Root); err != nil {
			return err
		}
		dir, err = os.MkdirTemp(opts.Root, "fresh-")
		defer func() {
			if dir != "" {
				serveErr = errors.Join(serveErr, os.RemoveAll(dir))
			}
		}()
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &service{
		ctx: ctx, token: opts.Token, agentHost: agent.Addr().String(),
		browser: browser{executable: opts.Executable, dir: dir},
	}
	defer s.browser.close()
	agentServer := &http.Server{Handler: http.HandlerFunc(s.agent), ReadHeaderTimeout: 5 * time.Second}
	viewServer := &http.Server{Handler: http.HandlerFunc(s.viewport), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second}
	defer func() { _ = agentServer.Close() }()
	defer func() { _ = viewServer.Close() }()
	errs := make(chan error, 2)
	go func() { errs <- agentServer.Serve(agent) }()
	go func() { errs <- viewServer.Serve(viewport) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		cancel()
		return err
	}
}

func ClearFresh(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "fresh-") {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
