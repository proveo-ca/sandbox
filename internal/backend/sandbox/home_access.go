// SPEC: _spec/internal/sbx/ide-attach.puml
package sandbox

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
)

// HomeAccess is the narrow host-state view an sbx run receives. The whole
// proveo home is deliberately absent: an attached editor must not inherit
// every prior run log merely because the agent needs its own config and tools.
type HomeAccess struct {
	Root      string
	FilesRoot string
	Mounts    []runner.Mount
	files     []string
}

func PrepareHomeAccess(root, runDir string, h manifest.Home) (HomeAccess, error) {
	a := HomeAccess{Root: root}
	if strings.TrimSpace(root) == "" || !h.Active() {
		return a, nil
	}
	seen := map[string]bool{}
	addDir := func(path string) error {
		path = filepath.Clean(path)
		if seen[path] {
			return nil
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		seen[path] = true
		// sbx mounts workspace directories at their host path; Container is
		// intentionally empty so WorkspaceBinds does not treat this as the
		// docker-only /proveo-home nesting.
		a.Mounts = append(a.Mounts, runner.Mount{Host: path})
		return nil
	}
	for _, m := range h.Mounts {
		rel := strings.Trim(strings.TrimSpace(m.Host), "/")
		if rel == "" {
			continue
		}
		if err := addDir(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return HomeAccess{}, fmt.Errorf("sandbox home access: %w", err)
		}
	}
	declared := len(a.Mounts)
	if err := addDir(filepath.Join(root, "toolchains")); err != nil {
		return HomeAccess{}, fmt.Errorf("sandbox home access: %w", err)
	}
	for _, m := range a.Mounts[:declared] {
		for _, l := range linkedDirs(root, m.Host) {
			if !seen[l.Host] {
				seen[l.Host] = true
				a.Mounts = append(a.Mounts, l)
			}
		}
	}
	if len(h.Files) == 0 {
		return a, nil
	}
	a.FilesRoot = filepath.Join(runDir, "sbx", "home-files")
	if err := addDir(a.FilesRoot); err != nil {
		return HomeAccess{}, fmt.Errorf("sandbox home files: %w", err)
	}
	for _, name := range h.Files {
		name = strings.TrimSpace(name)
		if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
			continue
		}
		a.files = append(a.files, name)
		if err := copyNewerFile(filepath.Join(root, name), filepath.Join(a.FilesRoot, name)); err != nil {
			return HomeAccess{}, fmt.Errorf("sandbox home file %s: %w", name, err)
		}
	}
	return a, nil
}

// linkedDirs is dir's first-level symlinks to directories outside the proveo
// home, as read-only mounts of their resolved targets.
// SPEC: _spec/internal/sbx/ide-attach.puml (LINKED DIRS FOLLOW, READ-ONLY)
func linkedDirs(root, dir string) []runner.Mount {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	home := realPath(root)
	var out []runner.Mount
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
			continue
		}
		if within(target, home) || within(home, target) || len(strings.Split(strings.Trim(filepath.ToSlash(target), "/"), "/")) < 2 {
			continue
		}
		out = append(out, runner.Mount{Host: target, ReadOnly: true})
	}
	return out
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// Commit copies home-root config files back after the in-VM config sync. The
// newest side wins, so a host edit made during the run is never overwritten by
// the copy staged when the run started.
func (a HomeAccess) Commit() error {
	if a.FilesRoot == "" {
		return nil
	}
	for _, name := range a.files {
		if err := copyNewerFile(filepath.Join(a.FilesRoot, name), filepath.Join(a.Root, name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (a HomeAccess) Cleanup() {
	if a.FilesRoot != "" {
		_ = os.RemoveAll(a.FilesRoot)
	}
}

func copyNewerFile(src, dst string) error {
	s, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if d, err := os.Stat(dst); err == nil && !s.ModTime().After(d.ModTime()) {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".proveo-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
		}
	}()
	if err := tmp.Chmod(s.Mode().Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(tmpName, time.Now(), s.ModTime()); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	ok = true
	return nil
}
