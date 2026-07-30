package cdn_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/proveo-ca/proveo/internal/cdn"
)

func TestAssetName(t *testing.T) {
	t.Parallel()
	if got := cdn.AssetName("linux", "amd64"); got != "proveo-linux-amd64" {
		t.Errorf("linux = %q", got)
	}
	if got := cdn.AssetName("windows", "arm64"); got != "proveo-windows-arm64.exe" {
		t.Errorf("windows = %q", got)
	}
}

func TestNewerAndDev(t *testing.T) {
	t.Parallel()
	if !cdn.IsDevVersion("dev@abc") || !cdn.IsDevVersion("dev") {
		t.Error("expected dev versions")
	}
	if !cdn.Newer("1.2.0", "1.1.9") {
		t.Error("1.2.0 should be newer than 1.1.9")
	}
	if cdn.Newer("1.0.0", "1.0.0") {
		t.Error("same version is not newer")
	}
	if !cdn.Newer("1.0.0", "dev@deadbeef") {
		t.Error("release should beat dev")
	}
}

func TestFetchManifestAndDownload(t *testing.T) {
	payload := []byte("proveo-binary-bytes")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])
	man := cdn.Manifest{
		Version: "1.2.3",
		Checksums: map[string]string{
			"proveo-linux-amd64": hexSum,
		},
	}
	body, _ := json.Marshal(man)

	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/bin/proveo-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := cdn.FetchManifest(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.2.3" {
		t.Fatalf("version = %q", got.Version)
	}

	dest := filepath.Join(t.TempDir(), "proveo")
	if err := cdn.DownloadAsset(srv.Client(), srv.URL, "proveo-linux-amd64", dest, hexSum); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != string(payload) {
		t.Fatalf("downloaded content mismatch: %v %q", err, b)
	}

	if err := cdn.DownloadAsset(srv.Client(), srv.URL, "proveo-linux-amd64", dest, "00"); err == nil {
		t.Fatal("expected checksum failure")
	}
}
