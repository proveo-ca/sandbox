// Package cdn knows how to resolve and verify proveo CLI releases published to
// the consumer CDN (apps/cli/public/cli → https://proveo.ca/cli).
package cdn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// AssetName returns the CDN binary basename for goos/goarch
// (e.g. proveo-linux-amd64, proveo-windows-amd64.exe).
func AssetName(goos, goarch string) string {
	name := "proveo-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// CurrentAssetName is AssetName for the running process.
func CurrentAssetName() string {
	return AssetName(runtime.GOOS, runtime.GOARCH)
}

// BaseURL resolves the CDN root: PROVEO_ASSET_BASE_URL, else DefaultBaseURL.
func BaseURL() string {
	if u := strings.TrimSpace(os.Getenv("PROVEO_ASSET_BASE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultBaseURL
}

// FetchManifest downloads and parses latest.json from baseURL.
func FetchManifest(client *http.Client, baseURL string) (Manifest, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	u, err := joinURL(baseURL, "latest.json")
	if err != nil {
		return Manifest{}, err
	}
	resp, err := client.Get(u)
	if err != nil {
		return Manifest{}, fmt.Errorf("fetch latest.json: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("fetch latest.json: HTTP %s", resp.Status)
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse latest.json: %w", err)
	}
	m.Version = strings.TrimPrefix(strings.TrimSpace(m.Version), "v")
	if m.Version == "" {
		return Manifest{}, fmt.Errorf("latest.json: missing version")
	}
	if len(m.Checksums) == 0 {
		return Manifest{}, fmt.Errorf("latest.json: missing checksums")
	}
	if m.BaseURL == "" {
		m.BaseURL = baseURL
	} else {
		m.BaseURL = strings.TrimRight(m.BaseURL, "/")
	}
	return m, nil
}

// DownloadAsset fetches bin/<asset> into destPath and verifies sha256 against wantHex.
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
		return fmt.Errorf("download %s: %w", asset, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", asset, resp.Status)
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
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", asset, wantHex, got)
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

// IsDevVersion reports builds that should not self-update against the release channel
// without --force (dev, empty, or goreleaser snapshot-looking stamps).
func IsDevVersion(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" || v == "dev" {
		return true
	}
	return strings.HasPrefix(v, "dev@") || strings.Contains(v, "-snapshot") || strings.HasPrefix(v, "0.0.0")
}

// Newer reports whether remote is a higher semver-ish version than local.
// Non-semver locals (dev@…) always lose to a remote release unless equal.
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

// compareSemver compares dotted numeric versions (major.minor.patch[+extra ignored]).
// Returns >0 if a>b, <0 if a<b, 0 if equal / incomparable.
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
