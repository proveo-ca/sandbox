package sbx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func TemplateLoadArgs() []string { return []string{"template", "load"} }

func TemplateListArgs() []string { return []string{"template", "ls"} }

func templateLoadViaTar(image string) error {
	dir, err := os.MkdirTemp("", "proveo-sbx-template-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tar := filepath.Join(dir, "image.tar")

	save := exec.Command("docker", "save", "-o", tar, image)
	save.Stdout, save.Stderr = os.Stderr, os.Stderr
	if err := save.Run(); err != nil {
		return fmt.Errorf("docker save %s: %w", image, err)
	}
	load := exec.Command(Binary, append(TemplateLoadArgs(), tar)...)
	load.Stdout, load.Stderr = os.Stderr, os.Stderr
	return load.Run()
}

func HasTemplate(image string) bool {
	out, err := sh.TemplateList()
	if err != nil {
		return false // unreadable store: load again rather than run on nothing
	}
	if _, ok := templateRow(string(out), image); !ok {
		return false
	}
	wantID := sh.LocalImageID(image)
	if wantID == "" {
		return true // cannot compare identity; presence is all there is
	}
	return readTemplateReceipt(image) == wantID
}

func receiptFile(image string) string {
	dir := templateReceiptDir()
	if dir == "" {
		return ""
	}
	name := strings.NewReplacer("/", "_", ":", "_").Replace(image)
	return filepath.Join(dir, name)
}

func readTemplateReceipt(image string) string {
	f := receiptFile(image)
	if f == "" {
		return ""
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "" // no receipt: treat as not-current and load
	}
	return strings.TrimSpace(string(b))
}

func writeTemplateReceipt(image string) {
	f := receiptFile(image)
	id := sh.LocalImageID(image)
	if f == "" || id == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(f), 0o755) != nil {
		return
	}
	_ = os.WriteFile(f, []byte(id), 0o644)
}

func templateRow(out, image string) (id string, ok bool) {
	wantRepo, wantTag := splitRef(image)
	for _, f := range templateRows(out) {
		if trimRegistry(f[0]) == wantRepo && f[1] == wantTag {
			if len(f) > 2 {
				return f[2], true
			}
			return "", true
		}
	}
	return "", false
}

func storeTemplateID(image string) string {
	out, err := sh.TemplateList()
	if err != nil {
		return ""
	}
	id, _ := templateRow(string(out), image)
	return id
}

func confirmLoaded(image string) error {
	want, got := sh.LocalImageID(image), storeTemplateID(image)
	if want == "" || got == "" || got == want {
		return nil // nothing to compare against: presence is all there is
	}
	return fmt.Errorf("sbx kept %s at %s after loading %s: the store did not take the image "+
		"(retry, or `sbx template rm %s` first)", image, got, want, image)
}

func templateRows(out string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "REPOSITORY" {
			continue
		}
		rows = append(rows, f)
	}
	return rows
}

func splitRef(image string) (repo, tag string) {
	repo, tag = image, "latest"
	if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i+1:], "/") {
		repo, tag = image[:i], image[i+1:]
	}
	return trimRegistry(repo), tag
}

func trimRegistry(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		return strings.Join(parts[1:], "/")
	}
	return repo
}

func TemplateRemoveArgs(image string) []string { return []string{"template", "rm", image} }

func ForceReload(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_SBX_RELOAD"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func EnsureTemplate(image string, report func(string, ...any)) error {
	if image == "" {
		return nil
	}
	if ForceReload(os.Getenv) {
		if report != nil {
			report("PROVEO_SBX_RELOAD=1 — dropping %s from the sandbox image store first", image)
		}
		_ = sh.TemplateRemove(image)
	} else if HasTemplate(image) {
		return nil
	}
	if report != nil {
		report("loading %s into the sandbox image store (local, per-user)", image)
	}
	if err := sh.TemplateLoad(image); err != nil {
		return fmt.Errorf("sbx template load %s: %w", image, err)
	}
	if err := confirmLoaded(image); err != nil {
		return err // no receipt: the next run retries rather than skipping forever
	}
	writeTemplateReceipt(image)
	return nil
}

func ReloadTemplate(image string, report func(string, ...any)) error {
	if image == "" {
		return nil
	}
	if report != nil {
		report("reloading %s into the sandbox image store", image)
	}
	if err := sh.TemplateLoad(image); err != nil {
		return fmt.Errorf("sbx template load %s: %w", image, err)
	}
	if err := confirmLoaded(image); err != nil {
		return err
	}
	writeTemplateReceipt(image)
	return nil
}
