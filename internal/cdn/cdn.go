// SPEC: _spec/internal/cdn/distribution-update.puml
// Package cdn knows how to resolve and verify proveo CLI releases published
// to the consumer CDN (apps/cli/public/cli → https://proveo.ca/cli).
package cdn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// DefaultBaseURL is the public install/update channel.
const DefaultBaseURL = "https://proveo.ca/cli"

// Manifest is the latest.json document staged next to install.sh.
type Manifest struct {
	Version   string            `json:"version"`
	Checksums map[string]string `json:"checksums"` // basename → hex sha256
	BaseURL   string            `json:"base_url,omitempty"`
}

func AssetName(goos, goarch string) string {
	name := "proveo-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func CurrentAssetName() string {
	return AssetName(runtime.GOOS, runtime.GOARCH)
}

func BaseURL() string {
	if u := strings.TrimSpace(os.Getenv("PROVEO_ASSET_BASE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultBaseURL
}

// ManifestError is a latest.json that gave no release: Fetch when it never arrived, else its body held none.
type ManifestError struct {
	Fetch bool
	Err   error
}

func (e ManifestError) Error() string {
	if e.Fetch {
		return "fetch latest.json: " + e.Err.Error()
	}
	return "latest.json: " + e.Err.Error()
}

func (e ManifestError) Unwrap() error { return e.Err }

// DownloadError is a release asset that did not arrive.
type DownloadError struct {
	Asset string
	Err   error
}

func (e DownloadError) Error() string { return "download " + e.Asset + ": " + e.Err.Error() }

func (e DownloadError) Unwrap() error { return e.Err }

// ChecksumMismatch is a downloaded asset whose sha256 is not the one latest.json names; it is not kept.
type ChecksumMismatch struct {
	Asset, Want, Got string
}

func (e ChecksumMismatch) Error() string {
	return fmt.Sprintf("checksum mismatch for %s (expected %s, got %s)", e.Asset, e.Want, e.Got)
}

func FetchManifest(client *http.Client, baseURL string) (Manifest, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	u, err := joinURL(baseURL, "latest.json")
	if err != nil {
		return Manifest{}, ManifestError{Fetch: true, Err: err}
	}
	resp, err := client.Get(u)
	if err != nil {
		return Manifest{}, ManifestError{Fetch: true, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, ManifestError{Fetch: true, Err: fmt.Errorf("HTTP %s", resp.Status)}
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return Manifest{}, ManifestError{Err: fmt.Errorf("not JSON: %w", err)}
	}
	m.Version = strings.TrimPrefix(strings.TrimSpace(m.Version), "v")
	if m.Version == "" {
		return Manifest{}, ManifestError{Err: errors.New("no version")}
	}
	if len(m.Checksums) == 0 {
		return Manifest{}, ManifestError{Err: errors.New("no checksums")}
	}
	if m.BaseURL == "" {
		m.BaseURL = baseURL
	} else {
		m.BaseURL = strings.TrimRight(m.BaseURL, "/")
	}
	return m, nil
}

func DownloadAsset(client *http.Client, baseURL, asset, destPath, wantHex string) error {
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	u, err := joinURL(strings.TrimRight(baseURL, "/"), "bin/"+asset)
	if err != nil {
		return err
	}
	resp, err := client.Get(u)
	if err != nil {
		return DownloadError{Asset: asset, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadError{Asset: asset, Err: fmt.Errorf("HTTP %s", resp.Status)}
	}
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		_ = os.Remove(destPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(destPath)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	wantHex = strings.ToLower(strings.TrimSpace(wantHex))
	if got != wantHex {
		_ = os.Remove(destPath)
		return ChecksumMismatch{Asset: asset, Want: wantHex, Got: got}
	}
	return nil
}

// ChecksumFor returns the hex digest for asset from the manifest.
func (m Manifest) ChecksumFor(asset string) (string, error) {
	sum, ok := m.Checksums[asset]
	if !ok || strings.TrimSpace(sum) == "" {
		return "", fmt.Errorf("latest.json has no checksum for %s", asset)
	}
	return strings.ToLower(strings.TrimSpace(sum)), nil
}

func joinURL(base, rel string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, rel)
	return u.String(), nil
}

func IsDevVersion(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" || v == "dev" {
		return true
	}
	return strings.HasPrefix(v, "dev@") || strings.Contains(v, "-snapshot") || strings.HasPrefix(v, "0.0.0")
}

func Newer(remote, local string) bool {
	remote = strings.TrimPrefix(strings.TrimSpace(remote), "v")
	local = strings.TrimPrefix(strings.TrimSpace(local), "v")
	if remote == "" || remote == local {
		return false
	}
	if IsDevVersion(local) {
		return true
	}
	return compareSemver(remote, local) > 0
}

func compareSemver(a, b string) int {
	ap := semverParts(a)
	bp := semverParts(b)
	n := len(ap)
	if len(bp) > n {
		n = len(bp)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av != bv {
			return av - bv
		}
	}
	return 0
}

func semverParts(v string) []int {
	v = strings.SplitN(v, "-", 2)[0]
	v = strings.SplitN(v, "+", 2)[0]
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out = append(out, n)
	}
	return out
}
