// SPEC: _spec/defs/opencode/engine-local-history.puml
package sandbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// opencodeTarget reports whether target runs OpenCode in a retained engine.
func opencodeTarget(target string) bool {
	return target == "opencode" || target == "opencode-browser"
}

// legacyOwnerRecord is the owner record a pre-engine-local OpenCode keeper left beside the receipts.
func legacyOwnerRecord(name string) string {
	return filepath.Join(receiptDir(), name+".opencode-cache.json")
}

// openCodeCarry is where a replaced engine's database waits for its successor's import.
func openCodeCarry(home, name string) string {
	return filepath.Join(home, "opencode", "carry", name+".db")
}

// openCodeImportSource is the host database a new engine imports: its carried database, else
// the first regular file among the proveo home's OpenCode stores; "" when there is none.
func openCodeImportSource(home, name string) string {
	if home == "" {
		return ""
	}
	for _, p := range []string{
		openCodeCarry(home, name),
		filepath.Join(home, "opencode", "v2", "share", "opencode.db"),
		filepath.Join(home, "opencode", "share", "opencode.db"),
		filepath.Join(home, ".local", "share", "opencode", "opencode.db"),
	} {
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

var createOpenCodeEngine = func(cfg sbx.RunConfig) error {
	cmd := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// ensureOpenCodeEngine reattaches to engine cfg.Name; a new engine is created and imports one
// host database first. created reports a new engine.
func ensureOpenCodeEngine(in Input, cfg sbx.RunConfig) (launch sbx.RunConfig, created bool, err error) {
	if sbx.Exists(cfg.Name) {
		return sbx.Reattach(cfg), false, nil
	}
	if err := createOpenCodeEngine(cfg); err != nil {
		return cfg, false, fmt.Errorf("create OpenCode engine %s: %w", cfg.Name, err)
	}
	src := openCodeImportSource(in.HomeRoot, cfg.Name)
	if src == "" {
		ui.Appf("OpenCode: new engine %s starts with empty history", cfg.Name)
		return sbx.Reattach(cfg), true, nil
	}
	size, err := importOpenCodeHistory(cfg.Name, src)
	if err != nil {
		_, _ = SbxRun(sbx.RemoveArgs(cfg.Name)...)
		_ = os.Remove(receiptPath(cfg.Name))
		return cfg, false, fmt.Errorf("OpenCode history import into %s failed: %w — the new engine was removed and %s stays; the next run retries", cfg.Name, err, src)
	}
	if carried := openCodeCarry(in.HomeRoot, cfg.Name); src == carried {
		_ = os.Remove(carried)
		ui.Appf("OpenCode: carried history (%s) into new engine %s", byteSize(size), cfg.Name)
	} else {
		ui.Appf("OpenCode: imported %s (%s) into new engine %s once; the host copy stays unchanged", src, byteSize(size), cfg.Name)
	}
	return sbx.Reattach(cfg), true, nil
}

// importOpenCodeHistory streams src and its -wal/-journal into engine name over `sbx exec -i` and
// installs them as its OpenCode database; it fails when src changes while it streams.
func importOpenCodeHistory(name, src string) (int64, error) {
	changed := fmt.Errorf("%s changed while it was copied; close the OpenCode that writes it", src)
	files := sbx.OpenCodeImportFiles(src)
	db, ok := files[""]
	if !ok {
		return 0, changed
	}
	cmd := exec.Command(sbx.Binary, sbx.OpenCodeImportArgs(name)...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("import: %w", err)
	}
	sendErr := sbx.WriteOpenCodeImport(stdin, src, files)
	_ = stdin.Close()
	runErr := cmd.Wait()
	switch code := exitCodeOf(runErr); {
	case runErr == nil, code == sbx.OpenCodeImportExisting:
		return db.Size(), nil
	case errors.Is(sendErr, sbx.ErrOpenCodeImportChanged):
		return 0, changed
	case code == sbx.OpenCodeImportCorrupt:
		return 0, fmt.Errorf("%s fails SQLite's quick_check", src)
	case code == sbx.OpenCodeImportIncomplete && sendErr != nil:
		return 0, fmt.Errorf("stream %s: %w", src, sendErr)
	}
	return 0, fmt.Errorf("import: %w: %s", runErr, strings.TrimSpace(out.String()))
}

// carryOpenCodeHistory streams engine name's database into its carry file under home; an
// engine without a database carries nothing.
func carryOpenCodeHistory(home, name string) error {
	if home == "" {
		return fmt.Errorf("no proveo home to carry it into")
	}
	dest := openCodeCarry(home, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	part := dest + ".partial"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(part) }()
	cmd := exec.Command(sbx.Binary, sbx.OpenCodeExportArgs(name)...)
	var errb strings.Builder
	cmd.Stdout, cmd.Stderr = f, &errb
	runErr := cmd.Run()
	closeErr := f.Close()
	switch {
	case runErr != nil && exitCodeOf(runErr) == sbx.OpenCodeExportNone:
		return nil
	case runErr != nil:
		return fmt.Errorf("export: %w: %s", runErr, strings.TrimSpace(errb.String()))
	case closeErr != nil:
		return closeErr
	}
	header := make([]byte, len(sbx.OpenCodeDatabaseHeader))
	r, err := os.Open(part)
	if err != nil {
		return err
	}
	_, readErr := io.ReadFull(r, header)
	info, statErr := r.Stat()
	_ = r.Close()
	if readErr != nil || string(header) != sbx.OpenCodeDatabaseHeader || statErr != nil {
		return fmt.Errorf("export produced no SQLite database")
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	ui.Storef("OpenCode: carried %s's history (%s) to %s for the engine that replaces it", name, byteSize(info.Size()), dest)
	return nil
}

// byteSize renders n bytes for an operator line.
func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
