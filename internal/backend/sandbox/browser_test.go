// SPEC: _spec/_plans/isolated-browser-profiles.puml
package sandbox

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/guestbrowser"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestIsolatedBrowserUsesAnEphemeralSBXPublication(t *testing.T) {
	in := Input{Target: "claudecode", Image: "example", Lookup: func(string) string { return "" }}
	for _, on := range []bool{false, true} {
		in.Browser = on
		cfg, kit, _ := Spec(in)
		want := guestbrowser.Off
		if on {
			want = guestbrowser.Isolated
		}
		if got := envValue(cfg.Env, guestbrowser.ModeEnv); got != want {
			t.Errorf("browser mode = %q, want %q", got, want)
		}
		found := slices.ContainsFunc(kit.Descriptor.Capabilities, func(c sbx.CapabilityV3) bool {
			return c.Type == sbx.CapPort && c.Config == (sbx.PortV3{Name: "browser-viewport", Container: guestbrowser.ViewportPort})
		})
		if found != on || len(cfg.Publish) != 0 {
			t.Errorf("browser %t: port capability found %t, pinned ports %v", on, found, cfg.Publish)
		}
	}
}

func TestBrowserProfileIdentityAndLease(t *testing.T) {
	dir := t.TempDir()
	old := receiptDir
	t.Cleanup(func() { receiptDir = old })
	receiptDir = func() string { return dir }
	in := Input{Target: "claudecode", Browser: true, BrowserProfile: "work", Sid: "one"}
	name := browserSandboxName(in, "/repo")
	in.Sid = "two"
	if browserSandboxName(in, "/repo") != name || browserSandboxName(in, "/other") == name {
		t.Fatal("named profile identity must survive the run and stay in its workspace")
	}
	release, err := profileLease(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateRecord(filepath.Join(dir, name+".lease.url"), []byte("http://127.0.0.1:2000/view/")); err != nil {
		t.Fatal(err)
	}
	if _, err := profileLease(name); err == nil || !strings.Contains(err.Error(), "http://127.0.0.1:2000/view/") {
		t.Fatalf("second lease: %v", err)
	}
	if err := writeReceipt(name, launchReceipt{BrowserProfile: "work", BrowserRun: "one"}); err != nil {
		t.Fatal(err)
	}
	release()
	if !BrowserProfileRuns()["one"] {
		t.Fatal("the stopped profile lost its retained run")
	}
	release, err = profileLease(name)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	in.BrowserProfile = ""
	fresh := browserSandboxName(in, "/repo")
	in.Sid = "three"
	if browserSandboxName(in, "/repo") == fresh || fresh == name {
		t.Fatal("fresh runs must use independent sandboxes")
	}
}

func TestViewportProxyRequiresRunPathAndLocalOrigin(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tabs" || r.Header.Get(guestbrowser.TokenHeader) != "guest-token" || r.Header.Get("Cookie") != "" {
			t.Errorf("upstream request: %s, %v", r.URL, r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	target, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	const host = "127.0.0.1:8080"
	proxy := viewportProxy(host, "/run-token/", "guest-token", target)
	for _, test := range []struct {
		host, origin, path string
		want               int
	}{
		{host, "http://" + host, "/run-token/tabs", 204},
		{host, "https://external.example", "/run-token/tabs", 404},
		{"external.example", "", "/run-token/tabs", 404},
		{host, "", "/wrong-token/tabs", 404},
	} {
		r := httptest.NewRequest("GET", "http://"+test.host+test.path, nil)
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Cookie", "host=private")
		r.Header.Set(guestbrowser.TokenHeader, "untrusted")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Errorf("viewport %s %s from %q: HTTP %d, want %d", test.host, test.path, test.origin, w.Code, test.want)
		}
	}
}

func TestNamedProfileChecksTheRecordedSandboxID(t *testing.T) {
	const name = "profile-sandbox"
	was := &launchReceipt{BrowserSandboxID: "owning-id"}
	listing(t, true, sbx.Listed{Name: name, ID: "replacement-id", Status: "stopped"})
	if _, err := namedProfileSandbox(name, was); err == nil || !strings.Contains(err.Error(), "different sandbox ID") {
		t.Fatalf("profile resumed in a replacement sandbox: %v", err)
	}
	listing(t, true, sbx.Listed{Name: name, ID: "owning-id", Status: "stopped"})
	if row, err := namedProfileSandbox(name, was); err != nil || row.ID != was.BrowserSandboxID {
		t.Fatalf("the owning sandbox could not resume: %v, %v", row, err)
	}
}

func TestBrowserPrintShowsTheProfileBeforeAnyViewportExists(t *testing.T) {
	screen, _ := captured(t)
	PrintInterfacePlan(Input{Browser: true, BrowserProfile: "work"})
	got := screen.String()
	if !strings.Contains(got, "profile work") || !strings.Contains(got, "viewport available in a live proveo run") ||
		strings.Contains(got, "127.0.0.1") {
		t.Fatalf("unexpected --print output:\n%s", got)
	}
}
