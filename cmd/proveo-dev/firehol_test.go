// SPEC: _spec/_plans/retire-docker-egress.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFireholConfigDefaults(t *testing.T) {
	c := fireholConfigFrom(env(nil), "/w", nil)
	want := fireholConfig{
		ipset: "firehol_level1", ref: "master",
		url:    "https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_level1.netset",
		outDir: "/w/config",
	}
	if c != want {
		t.Errorf("defaults = %+v, want %+v", c, want)
	}
}

func TestFireholConfigPrecedence(t *testing.T) {
	c := fireholConfigFrom(env(map[string]string{
		"FIREHOL_IPSET": "firehol_level2", "FIREHOL_REF": "abc", "FIREHOL_OUTPUT_DIR": "/env",
	}), "/w", nil)
	if c.url != "https://raw.githubusercontent.com/firehol/blocklist-ipsets/abc/firehol_level2.netset" || c.outDir != "/env" {
		t.Errorf("env config = %+v", c)
	}
	if c = fireholConfigFrom(env(map[string]string{"FIREHOL_OUTPUT_DIR": "/env", "FIREHOL_SOURCE_URL": "u"}), "/w", []string{"/arg"}); c.outDir != "/arg" || c.url != "u" {
		t.Errorf("arg/url override = %+v", c)
	}
	if c = fireholConfigFrom(env(nil), "/w", []string{""}); c.outDir != "/w/config" {
		t.Errorf("empty arg must fall back like ${1:-}: %+v", c)
	}
}

func TestSquidACLMatchesTheBashConversion(t *testing.T) {
	c := fireholConfig{ipset: "s", url: "u"}
	netset := "# header\n1.2.3.0/24\n  5.6.7.8 # trailing\n\r\n\t9.9.9.9\r\n10.0.0.0/8"
	got := string(squidACL(c, []byte(netset), time.Date(2026, 9, 22, 1, 2, 3, 0, time.UTC)))
	want := `# Generated from u
# FireHOL ipset: s
# Generated at: 2026-09-22T01:02:03Z
# Review false-positive risk before enabling broad feeds.
acl firehol_ipset dst 1.2.3.0/24
acl firehol_ipset dst 5.6.7.8
acl firehol_ipset dst 9.9.9.9
acl firehol_ipset dst 10.0.0.0/8
http_access deny firehol_ipset
`
	if got != want {
		t.Errorf("squidACL:\n%s\nwant:\n%s", got, want)
	}
}

func TestFireholUpdateVerifiesChecksum(t *testing.T) {
	body := "1.1.1.1\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	sum := sha256.Sum256([]byte(body))
	dir := t.TempDir()
	now := func() time.Time { return time.Unix(0, 0) }

	c := fireholConfig{ipset: "s", ref: "abc", url: srv.URL, sha: strings.Repeat("0", 64), outDir: dir}
	if err := fireholUpdate(c, srv.Client(), now); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "firehol-ipset.conf")); err == nil {
		t.Fatal("a mismatched netset was written")
	}
	c.sha = hex.EncodeToString(sum[:])
	if err := fireholUpdate(c, srv.Client(), now); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "firehol-ipset.conf"))
	if !strings.Contains(string(got), "acl firehol_ipset dst 1.1.1.1\n") {
		t.Errorf("output:\n%s", got)
	}
}

func TestFireholUpdateFailsOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := fireholConfig{ref: "abc", url: srv.URL, outDir: t.TempDir()}
	if err := fireholUpdate(c, srv.Client(), time.Now); err == nil {
		t.Error("a 404 must fail like curl -f")
	}
}
