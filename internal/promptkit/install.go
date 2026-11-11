// SPEC: _spec/internal/promptkit/prompt-kit.puml
package promptkit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InstallStarship downloads the pinned starship for goos/goarch, checks it against its
// pinned sha256, and puts the binary in binDir. It returns the binary's path.
func InstallStarship(goos, goarch, binDir string, fetch Fetch) (string, error) {
	asset, sum, url, ok := StarshipAsset(goos, goarch)
	if !ok {
		return "", fmt.Errorf("starship publishes no build for %s/%s", goos, goarch)
	}
	archive, err := verified(fetch, url, sum)
	if err != nil {
		return "", err
	}
	name := "starship"
	var bin []byte
	if strings.HasSuffix(asset, ".zip") {
		name = "starship.exe"
		files, err := fromZip(archive, func(base string) bool { return base == name })
		if err != nil {
			return "", err
		}
		bin = files[name]
	} else if bin, err = fromTarGz(archive, name); err != nil {
		return "", err
	}
	if len(bin) == 0 {
		return "", fmt.Errorf("%s holds no %s", asset, name)
	}
	dest := filepath.Join(binDir, name)
	return dest, writeFile(dest, bin, 0o755)
}

// FontDirs lists where a host keeps fonts: every directory to look in, and the
// per-user one proveo installs into.
func FontDirs(goos, home string, getenv func(string) string) (scan []string, install string) {
	switch goos {
	case "darwin":
		install = filepath.Join(home, "Library", "Fonts")
		return []string{install, "/Library/Fonts", "/System/Library/Fonts"}, install
	case "windows":
		install = filepath.Join(getenv("LOCALAPPDATA"), "Microsoft", "Windows", "Fonts")
		return []string{install, filepath.Join(getenv("WINDIR"), "Fonts")}, install
	}
	data := getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	install = filepath.Join(data, "fonts")
	return []string{install, filepath.Join(home, ".fonts"), "/usr/local/share/fonts", "/usr/share/fonts"}, install
}

// isNerdFont reports whether a font file's name marks it as a Nerd Font.
func isNerdFont(base string) bool {
	ext := strings.ToLower(filepath.Ext(base))
	if ext != ".ttf" && ext != ".otf" && ext != ".ttc" {
		return false
	}
	name := strings.ToLower(base)
	return strings.Contains(name, "nerdfont") || strings.Contains(name, "nerd font")
}

// HasNerdFont reports whether any of dirs holds a Nerd Font, at any depth.
func HasNerdFont(dirs []string) bool {
	found := false
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // a directory that cannot be read holds no font this user can use
			}
			if !d.IsDir() && isNerdFont(d.Name()) {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if found {
			return true
		}
	}
	return false
}

// fontSHA256 is the digest InstallFont holds the archive to; tests replace it.
var fontSHA256 = FontSHA256

// InstallFont downloads the pinned Nerd Font, checks it against its pinned sha256, and
// unpacks its font files into dir. It returns the files it wrote, sorted.
func InstallFont(dir string, fetch Fetch) ([]string, error) {
	archive, err := verified(fetch, FontURL(), fontSHA256)
	if err != nil {
		return nil, err
	}
	fonts, err := fromZip(archive, func(base string) bool {
		ext := strings.ToLower(filepath.Ext(base))
		return ext == ".ttf" || ext == ".otf"
	})
	if err != nil {
		return nil, err
	}
	if len(fonts) == 0 {
		return nil, fmt.Errorf("%s holds no font file", FontAsset)
	}
	var written []string
	for base, body := range fonts {
		dest := filepath.Join(dir, base)
		if err := writeFile(dest, body, 0o644); err != nil {
			return written, err
		}
		written = append(written, dest)
	}
	sort.Strings(written)
	return written, nil
}
