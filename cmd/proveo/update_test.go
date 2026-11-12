// SPEC: _spec/internal/cdn/distribution-update.puml
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/cdn"
)

// channel is what a test's release channel serves; a field left empty is a 404.
type channel struct {
	latest, asset, uninstall string
}

// updateHost is isolatedHost running proveo `running` from a file under the temp install root, with
// PROVEO_ASSET_BASE_URL at a local server for ch; it returns that file. updateAt replaces only it.
func updateHost(t *testing.T, running string, ch channel) (exe, base string) {
	t.Helper()
	isolatedHost(t)
	exe = filepath.Join(os.Getenv("PROVEO_INSTALL_ROOT"), "bin", "proveo")
	writeFile(t, exe, "old")
	prev := version
	t.Cleanup(func() { version = prev })
	version = running
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{"/latest.json": ch.latest, "/bin/" + cdn.CurrentAssetName(): ch.asset, "/uninstall.sh": ch.uninstall}[r.URL.Path]
		if body == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PROVEO_ASSET_BASE_URL", srv.URL)
	return exe, srv.URL
}

// release is latest.json for version, naming sum as this platform's checksum.
func release(t *testing.T, version, sum string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"version": version, "checksums": map[string]string{cdn.CurrentAssetName(): sum}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sha(body string) string {
	s := sha256.Sum256([]byte(body))
	return hex.EncodeToString(s[:])
}

func mustHold(t *testing.T, exe, want string) {
	t.Helper()
	if b, _ := os.ReadFile(exe); string(b) != want {
		t.Errorf("the binary holds %q, want %q", b, want)
	}
}

func TestUpdateCheckSaysAvailableWithTheChannelDim(t *testing.T) {
	exe, base := updateHost(t, "0.3.1", channel{latest: release(t, "0.4.0", sha("new")), asset: "new"})
	out := captureUI(t)
	if err := updateAt(exe, false, true); err != nil {
		t.Fatal(err)
	}
	if want := "proveo 0.3.1 → 0.4.0 — available\n    " + base + "\n"; out.String() != want {
		t.Errorf("update --check printed %q, want %q", out, want)
	}
	mustHold(t, exe, "old")
}

func TestUpdateOnTheLatestSaysUpToDateInOneWording(t *testing.T) {
	exe, _ := updateHost(t, "0.4.0", channel{latest: release(t, "0.4.0", sha("new")), asset: "new"})
	out := captureUI(t)
	for _, check := range []bool{true, false} {
		out.Reset()
		if err := updateAt(exe, false, check); err != nil || out.String() != "ok: proveo 0.4.0 — up to date\n" {
			t.Errorf("check=%v: %q, %v", check, out, err)
		}
	}
	mustHold(t, exe, "old")
}

func TestUpdateReplacesTheBinaryAndClosesOnUpdated(t *testing.T) {
	exe, _ := updateHost(t, "0.3.1", channel{latest: release(t, "0.4.0", sha("new")), asset: "new", uninstall: "#!/bin/sh\n"})
	out := captureUI(t)
	if err := updateAt(exe, false, false); err != nil {
		t.Fatal(err)
	}
	if want := "proveo 0.4.0 — downloading\nok: proveo 0.3.1 → 0.4.0 — updated\n    ~/.proveo/bin/proveo\n"; out.String() != want {
		t.Errorf("update printed %q, want %q", out, want)
	}
	mustHold(t, exe, "new")
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("PROVEO_INSTALL_ROOT"), "uninstall.sh")); string(b) != "#!/bin/sh\n" {
		t.Errorf("uninstall.sh = %q", b)
	}

	out.Reset()
	if err := updateAt(exe, true, false); err != nil || !strings.HasSuffix(out.String(), "ok: proveo 0.3.1 → 0.4.0 — updated\n    ~/.proveo/bin/proveo\n") {
		t.Errorf("--force printed %q, %v", out, err)
	}
	version = "0.4.0"
	out.Reset()
	if err := updateAt(exe, true, false); err != nil || !strings.HasSuffix(out.String(), "ok: proveo 0.4.0 — reinstalled\n    ~/.proveo/bin/proveo\n") {
		t.Errorf("--force on the same release printed %q, %v", out, err)
	}
}

func TestAnUnrefreshedUninstallScriptWarnsBeforeTheVerdict(t *testing.T) {
	exe, _ := updateHost(t, "0.3.1", channel{latest: release(t, "0.4.0", sha("new")), asset: "new"})
	out := captureUI(t)
	if err := updateAt(exe, false, false); err != nil {
		t.Fatal(err)
	}
	want := "proveo 0.4.0 — downloading\n" +
		"warn: ~/.proveo/uninstall.sh — not refreshed\n    HTTP 404 Not Found\n" +
		"ok: proveo 0.3.1 → 0.4.0 — updated\n    ~/.proveo/bin/proveo\n"
	if out.String() != want {
		t.Errorf("update printed %q, want %q", out, want)
	}
}

// failsWith runs updateAt and wants it Reported with exactly want on screen and the binary left as it was.
func failsWith(t *testing.T, exe string, force bool, want string) {
	t.Helper()
	out := captureUI(t)
	err := updateAt(exe, force, false)
	var rep backend.Reported
	if !errors.As(err, &rep) || out.String() != want {
		t.Errorf("update = %v, printed %q; want Reported and %q", err, out, want)
	}
	mustHold(t, exe, "old")
}

func TestUpdateRefusesALocalBuildWithTheForceFixDim(t *testing.T) {
	exe, _ := updateHost(t, "dev@f4e379c2", channel{latest: release(t, "0.4.0", sha("new")), asset: "new"})
	failsWith(t, exe, false, "error: proveo dev@f4e379c2 — a local build, not replaced\n    `proveo update --force` replaces it with 0.4.0\n")
}

func TestAChecksumMismatchShowsBothSumsDimAndLeavesTheBinary(t *testing.T) {
	want := strings.Repeat("0", 64)
	exe, _ := updateHost(t, "0.3.1", channel{latest: release(t, "0.4.0", want), asset: "new"})
	failsWith(t, exe, false, "proveo 0.4.0 — downloading\nerror: "+cdn.CurrentAssetName()+" 0.4.0 — checksum mismatch\n"+
		"    expected "+want+"\n    got      "+sha("new")+"\n")
}

func TestAReleaseThatDidNotArriveFailsOnceUnderItsName(t *testing.T) {
	exe, _ := updateHost(t, "0.3.1", channel{latest: release(t, "0.4.0", sha("new"))})
	failsWith(t, exe, false, "proveo 0.4.0 — downloading\nerror: proveo 0.4.0 — not downloaded\n    HTTP 404 Not Found\n")
}

func TestAnUnreadableManifestFailsOnceUnderItsName(t *testing.T) {
	exe, _ := updateHost(t, "0.3.1", channel{})
	failsWith(t, exe, false, "error: latest.json — not read\n    HTTP 404 Not Found\n")

	exe, _ = updateHost(t, "0.3.1", channel{latest: "<html>"})
	failsWith(t, exe, false, "error: latest.json — not valid\n    not JSON: invalid character '<' looking for beginning of value\n")

	exe, _ = updateHost(t, "0.3.1", channel{latest: `{"version":"0.4.0","checksums":{"proveo-plan9-mips":"00"}}`})
	failsWith(t, exe, false, "error: latest.json — no checksum for "+cdn.CurrentAssetName()+"\n")
}
