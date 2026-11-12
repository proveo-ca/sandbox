// SPEC: _spec/internal/promptkit/prompt-kit.puml
package promptkit

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxDownload bounds one release asset; the font archive is 27 MB.
const maxDownload = 96 << 20

// Fetch returns the body at url.
type Fetch func(url string) ([]byte, error)

// HTTPFetch downloads over HTTPS with a deadline and a size cap.
func HTTPFetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDownload {
		return nil, fmt.Errorf("GET %s: larger than %d MB", url, maxDownload>>20)
	}
	return body, nil
}

// verified fetches url and refuses a body whose sha256 is not want.
func verified(fetch Fetch, url, want string) ([]byte, error) {
	body, err := fetch(url)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		return nil, fmt.Errorf("%s has sha256 %s, want the pinned %s — nothing was installed", filepath.Base(url), got, want)
	}
	return body, nil
}

// writeFile puts body at path through a rename, with mode.
func writeFile(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".proveo-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// fromTarGz returns the regular file named name at any depth of a .tar.gz.
func fromTarGz(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive holds no %s", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

// fromZip returns the files of a .zip whose base name keep accepts, by base name.
func fromZip(archive []byte, keep func(base string) bool) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if f.FileInfo().IsDir() || !keep(base) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxDownload))
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		out[base] = body
	}
	return out, nil
}
