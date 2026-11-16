// SPEC: _spec/tests/stale-local-image.puml
package imagebuild

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// InputsLabel names the image label holding the InputsDigest a build ran against.
const InputsLabel = "proveo.inputs"

// InputsFormat is the `docker image inspect --format` that prints InputsLabel.
const InputsFormat = `{{index .Config.Labels "` + InputsLabel + `"}}`

// InputPaths are the repo-relative trees whose content an image bakes.
var InputPaths = []string{"defs", "packages"}

// InputsDigest hashes the working-tree content of every git-visible file under InputPaths in root.
func InputsDigest(root string) (string, error) {
	if root == "" {
		return "", errors.New("no repo root")
	}
	args := append([]string{"-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--"}, InputPaths...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	paths := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	slices.Sort(paths)
	paths = slices.Compact(paths)
	h := sha256.New()
	for _, rel := range paths {
		if rel == "" {
			continue
		}
		body, err := readInput(filepath.Join(root, rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(body)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readInput(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		return []byte("symlink:" + target), err
	case fi.IsDir():
		return []byte("dir"), nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&0o111 != 0 {
		body = bytes.Join([][]byte{[]byte("exec:"), body}, nil)
	}
	return body, nil
}

// InputsArgs is the --label pair stamping root's InputsDigest, or nil when root is no git checkout.
func InputsArgs(root string) []string {
	d, err := InputsDigest(root)
	if err != nil {
		return nil
	}
	return []string{"--label", InputsLabel + "=" + d}
}
