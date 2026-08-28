package sbx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func TemplateLoadArgs() []string { return []string{"template", "load"} }

// TemplateListArgs builds the argv that lists the images already in that store.
func TemplateListArgs() []string { return []string{"template", "ls"} }

// templateLoadViaTar crosses the two image stores through a TAR ON DISK, because
// `sbx template load` takes a FILE — it does not read stdin, so the obvious
// `docker save | sbx template load` pipe fails with "requires at least 1 and at
// most 2 arguments" before a byte moves.
//
// The tar is the size of the image (multi-GB for the browser variants), so it
// lands in a temp dir removed whether the load succeeds or not: a failed run must
// not leave a copy of every harness image on the host.
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

// localImageID is the host engine's ID for an image, shortened to the width the
// sandbox store prints. Overridable in tests.
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

// templateReceiptDir holds one small file per loaded reference, naming the host
// image ID that was handed to the store. Overridable so tests do not touch a real
// user cache.
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

// writeTemplateReceipt records a completed load. A receipt that cannot be written
// is not an error: the next run reloads, which is correct, just slower.
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

// templateRow finds image's row in `sbx template ls` output and returns the id the
// store prints for it.
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

// storeTemplateID is the id the sandbox store prints for image, or "" when it holds
// no such image.
//
// This is trustworthy ONLY immediately after a load. `sbx create` re-bakes the
// template and rewrites that column, so comparing it at check time would reload a
// multi-GB tar on every launch forever after — see TestHasTemplateReloadsARebuiltImage.
// At load time, before any sandbox exists, the store prints exactly what it was
// handed, which is what makes the post-load verification below possible at all.
func storeTemplateID(image string) string {
	out, err := sh.TemplateList()
	if err != nil {
		return ""
	}
	id, _ := templateRow(string(out), image)
	return id
}

// confirmLoaded reports whether the store actually took the image it was handed. A
// receipt written without this outlives the load that failed to land: HasTemplate
// then reads a receipt naming the new id over a store still holding the old content,
// and skips the reload forever. Silent, self-perpetuating, and it cost an afternoon
// of chasing a stale entrypoint that no rebuild could dislodge.
func confirmLoaded(image string) error {
	want, got := sh.LocalImageID(image), storeTemplateID(image)
	if want == "" || got == "" || got == want {
		return nil // nothing to compare against: presence is all there is
	}
	return fmt.Errorf("sbx kept %s at %s after loading %s: the store did not take the image "+
		"(retry, or `sbx template rm %s` first)", image, got, want, image)
}

// templateRows splits `sbx template ls` output into whitespace-separated fields
// per row, skipping the header.
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

// splitRef splits an image reference into repository and tag, defaulting the tag
// the way docker does.
func splitRef(image string) (repo, tag string) {
	repo, tag = image, "latest"
	if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i+1:], "/") {
		repo, tag = image[:i], image[i+1:]
	}
	return trimRegistry(repo), tag
}

// trimRegistry drops a leading registry host so a locally-built "proveo/x" and
// the store's "docker.io/proveo/x" compare equal.
func trimRegistry(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		return strings.Join(parts[1:], "/")
	}
	return repo
}

// EnsureTemplate puts image into the sandbox runtime's image store, which is
// SEPARATE from the host engine's: `sbx template ls` is empty on a machine whose
// docker holds every proveo image, so a Kit naming one would resolve to nothing.
//
// The transfer is local and per-user by design — `docker save | sbx template
// load` over a pipe, no registry. Each operator has their own docker login and
// their own sbx config, so a shared registry would make a per-user concern into
// an authenticated remote one, and would publish proveo's images somewhere they
// do not need to be.
//
// Already-present is not re-loaded: the images are multi-GB and a run must not
// pay that twice.
// TemplateRemoveArgs builds the argv that drops an image from the sandbox store.
// Removing before loading is deterministic where overwriting is merely expected.
func TemplateRemoveArgs(image string) []string { return []string{"template", "rm", image} }

// ForceReload reports whether the operator has asked for the template to be handed
// over again regardless of receipts — the escape hatch for a store that has gone out
// of sync in a way proveo cannot see.
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

// ReloadTemplate hands the image over again, receipt or no receipt. It exists
// for the repair path: see StartFailure.
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

// Exists reports whether sbx currently holds a sandbox by this name. It is the
// signal that separates a sandbox which never STARTED from an agent that ran and
// exited non-zero, and only the former may be retried — retrying the latter would
// run the agent a second time.
//
// The distinction cannot be read off sbx's output: it prints "ERROR: failed to
// create sandbox: failed to run sandbox container" on STDOUT, and stdout is the
// agent's terminal. Wrapping it to sniff for that text would leave sbx writing to
// something that is not an *os.File, which is exactly what its own PTY checks
// look for. Asking what exists afterwards costs one cheap `sbx ls` and stays out
// of the terminal's way.
//
// This matters because sbx's stored template can go bad on its own: `sbx create`
// re-bakes the template it was handed, and a baked cursor template reached a
// state where EVERY create failed with "failed to run sandbox container" —
// including argv that had worked minutes earlier, with `sbx diagnose` reporting
// 12/12 healthy. Loading the image again repaired it immediately. Before the load
// was made conditional this never surfaced, because proveo re-loaded the template
// on every run and overwrote each bad bake before anything could run on it.
