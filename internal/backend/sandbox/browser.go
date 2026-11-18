// SPEC: _spec/_plans/isolated-browser-profiles.puml
package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/guestbrowser"
	"github.com/proveo-ca/proveo/internal/sbx"
)

var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,47}$`)
var profileSandbox = regexp.MustCompile(`-browser-profile-[a-f0-9]{24}$`)

func randomBrowserToken() string {
	var token [24]byte
	_, _ = rand.Read(token[:])
	return hex.EncodeToString(token[:])
}

func ValidateBrowserProfile(name string) error {
	if !profileName.MatchString(name) {
		return fmt.Errorf("browser profile %q must have 1–48 letters, digits, dots, dashes or underscores and start with a letter or digit", name)
	}
	return nil
}

func browserSandboxName(in Input, key string) string {
	name := sbx.SandboxName(in.Target, key)
	if !in.Browser {
		return name
	}
	if in.BrowserProfile != "" {
		hash := sha256.Sum256([]byte(in.Target + "\x00" + key + "\x00" + in.BrowserProfile))
		return sbx.SandboxName(Harness(in), "") + fmt.Sprintf("-browser-profile-%x", hash[:12])
	}
	hash := sha256.Sum256([]byte(in.Sid))
	return name + fmt.Sprintf("-browser-%x", hash[:8])
}

func profileDeletion(name string) string {
	return "proveo rm --delete-browser-profile " + name
}

func profileLease(name string) (release func(), err error) {
	dir := receiptDir()
	if dir == "" {
		return nil, errors.New("cannot locate the browser profile lease directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".lease")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	busy, err := tryProfileLock(f)
	switch {
	case busy:
		_ = f.Close()
		view, _ := os.ReadFile(path + ".url")
		if len(view) > 0 {
			return nil, fmt.Errorf("browser profile is in use by %s. Open %s to join its viewport, or choose another profile", name, view)
		}
		return nil, fmt.Errorf("browser profile is in use by %s. Reattach to its terminal or choose another profile", name)
	case err != nil:
		_ = f.Close()
		return nil, err
	}
	_ = os.Remove(path + ".url")
	return func() {
		_ = os.Remove(path + ".url")
		_ = f.Close()
	}, nil
}

func writePrivateRecord(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".browser-record-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(body)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func namedProfileSandbox(name string, was *launchReceipt) (*sbx.Listed, error) {
	rows, ok := listSandboxes()
	if !ok || (len(rows) == 0 && was != nil && sbx.Exists(name)) {
		return nil, errNoListing
	}
	i := slices.IndexFunc(rows, func(row sbx.Listed) bool { return row.Name == name })
	if i < 0 {
		return nil, nil
	}
	row := &rows[i]
	if was != nil && was.BrowserSandboxID != "" && row.ID != was.BrowserSandboxID {
		return nil, fmt.Errorf("%s has a different sandbox ID from its recorded browser profile. Inspect sbx ls before using %s", name, profileDeletion(name))
	}
	switch strings.ToLower(row.Status) {
	case "created", "exited", "stopped":
		return row, nil
	default:
		return nil, fmt.Errorf("profile sandbox %s is %s. Reattach to its terminal, or stop it before starting another run", name, row.Status)
	}
}

func refreshProfileCloneEnv(in Input, receipt *launchReceipt) error {
	if in.CloneEnv == "" {
		return nil
	}
	if receipt == nil || !filepath.IsAbs(receipt.KitDir) {
		return fmt.Errorf("missing the browser profile sandbox's project environment record")
	}
	oldRun := filepath.Dir(filepath.Dir(filepath.Dir(receipt.KitDir)))
	rel, err := filepath.Rel(in.RunDir, in.CloneEnv)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("locate the browser profile sandbox's project environment")
	}
	data, err := os.ReadFile(in.CloneEnv)
	if err != nil {
		return err
	}
	return replaceFile(filepath.Join(oldRun, rel), data)
}

// BrowserProfileRuns reports state directories used by retained profile sandboxes.
func BrowserProfileRuns() map[string]bool {
	runs := map[string]bool{}
	dir := receiptDir()
	if dir == "" {
		return runs
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		name, isReceipt := strings.CutSuffix(f.Name(), ".json")
		if !isReceipt || !profileSandbox.MatchString(name) {
			continue
		}
		if r := readReceipt(name); r != nil && r.BrowserRun != "" {
			runs[r.BrowserRun] = true
		}
	}
	return runs
}

type viewportProcess struct {
	stop      func() error
	url       string
	sandboxID string
}

func startBrowserViewport(ctx context.Context, in Input, cfg sbx.RunConfig) (viewportProcess, error) {
	if !in.Browser {
		return viewportProcess{stop: func() error { return nil }}, nil
	}
	token := randomBrowserToken()
	kind := "fresh"
	if in.BrowserProfile != "" {
		kind = "named"
	}
	cmd := exec.Command(sbx.Binary, "exec", "-i", "-u", "agent", "-w", "/", cfg.Name, "--",
		"/usr/local/bin/proveo-entrypoint", "browser", kind)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return viewportProcess{}, err
	}
	logs := agentsettings.NewTail(12)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return viewportProcess{}, err
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	stopGuest := func() error {
		_ = stdin.Close()
		select {
		case <-done:
			return waitErr
		case <-time.After(9 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			return errors.New("isolated browser did not confirm profile cleanup")
		}
	}
	fail := func(err error) (viewportProcess, error) {
		_ = stopGuest()
		return viewportProcess{}, fmt.Errorf("start isolated browser in %s: %w. Rebuild the selected image if it lacks the browser launcher. %s",
			cfg.Name, err, strings.Join(logs.Lines(), "\n"))
	}
	if _, err := io.WriteString(stdin, token+"\n"); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var upstream *url.URL
	var sandboxID string
	probe := time.NewTicker(250 * time.Millisecond)
	defer probe.Stop()
	for upstream == nil {
		select {
		case <-done:
			err := waitErr
			if err == nil {
				err = errors.New("browser launcher ended")
			}
			return fail(err)
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-probe.C:
			row := listedSandbox(cfg.Name)
			if row != nil && row.Running() {
				port := keptPorts(row, nil)[guestbrowser.ViewportPort]
				if port > 0 {
					upstream = probeBrowser(ctx, port, token)
					sandboxID = row.ID
				}
			}
		}
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fail(err)
	}
	path := "/" + randomBrowserToken() + "/"
	address := "http://" + l.Addr().String() + path
	if in.BrowserProfile != "" {
		if err := writePrivateRecord(filepath.Join(receiptDir(), cfg.Name+".lease.url"), []byte(address)); err != nil {
			_ = l.Close()
			return fail(err)
		}
	}
	server := &http.Server{
		Handler:           viewportProxy(l.Addr().String(), path, token, upstream),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 35 * time.Second, IdleTimeout: time.Minute,
	}
	go func() { _ = server.Serve(l) }()
	return viewportProcess{url: address, sandboxID: sandboxID, stop: func() error {
		if in.BrowserProfile != "" {
			_ = os.Remove(filepath.Join(receiptDir(), cfg.Name+".lease.url"))
		}
		_ = server.Close()
		_ = l.Close()
		err := stopGuest()
		if err == nil || in.BrowserProfile != "" || sbx.Gone(cfg.Name) {
			return err
		}
		out, cleanup := guestRun("exec", "-u", "agent", "-w", "/", cfg.Name, "--", "/usr/local/bin/proveo-entrypoint", "browser", "clear-fresh")
		if cleanup != nil && !sbx.Gone(cfg.Name) {
			return errors.Join(err, fmt.Errorf("clear fresh profile: %s", failure(cleanup, out)))
		}
		return nil
	}}, nil
}

var browserTransport = &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	ResponseHeaderTimeout: 25 * time.Second,
}

func probeBrowser(ctx context.Context, port int, token string) *url.URL {
	u := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/health", nil)
	if err != nil {
		return nil
	}
	req.Header.Set(guestbrowser.TokenHeader, token)
	resp, err := browserTransport.RoundTrip(req)
	if err != nil {
		return nil
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return nil
	}
	return u
}

func viewportProxy(host, prefix, token string, upstream *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Transport: browserTransport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.URL.Path = "/" + strings.TrimPrefix(r.In.URL.Path, prefix)
			r.Out.URL.RawPath = ""
			r.Out.Header.Set(guestbrowser.TokenHeader, token)
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Authorization")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if r.Host != host || !strings.HasPrefix(r.URL.Path, prefix) ||
			(origin != "" && origin != "http://"+host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" ||
			r.Header.Get("Upgrade") != "" {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		proxy.ServeHTTP(w, r)
	})
}
