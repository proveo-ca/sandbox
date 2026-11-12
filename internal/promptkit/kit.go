// SPEC: _spec/internal/promptkit/prompt-kit.puml
package promptkit

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/shell"
)

// Options says where one host's prompt kit goes and how its steps reach the outside.
type Options struct {
	GOOS, GOARCH string
	Home         string
	BinDir       string // where a starship proveo installs lands; it must be on PATH
	StateDir     string // where the record of what proveo installed is kept
	Shell        shell.Shell
	Getenv       func(string) string
	Fetch        Fetch
	LookPath     func(string) (string, error)
	// Preset returns starship's named preset as a config file; nil or an error seeds none.
	Preset func(starship, name string) ([]byte, error)
	// DrawsGlyphs asks the operator whether the terminal drew a Nerd glyph; nil does not ask.
	DrawsGlyphs func() (icon, asked bool)
	// Fact and Warn say what Apply found, or what Undo removed, as a key, its value and dim detail, or as a
	// warning; nil says nothing.
	Fact func(key, value string, detail ...string)
	Warn func(text string, detail ...string)
}

// Record is what proveo itself installed, so an uninstall removes that and no more.
type Record struct {
	Starship      string   `json:"starship,omitempty"` // a binary proveo downloaded
	Fonts         []string `json:"fonts,omitempty"`    // font files proveo unpacked
	RC            string   `json:"rc,omitempty"`       // the rc file proveo appended its block to
	Config        string   `json:"config,omitempty"`   // starship's config, holding proveo's block
	ConfigCreated bool     `json:"config_created,omitempty"`
}

func recordPath(state string) string { return filepath.Join(state, "prompt-kit.json") }

// ReadRecord loads what an earlier Apply installed; a missing record is an empty one.
func ReadRecord(state string) Record {
	var r Record
	if b, err := os.ReadFile(recordPath(state)); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

func (o Options) getenv(k string) string {
	if o.Getenv == nil {
		return ""
	}
	return o.Getenv(k)
}

func (o Options) fact(key, value string, detail ...string) {
	if o.Fact != nil {
		o.Fact(key, value, detail...)
	}
}

func (o Options) warn(text string, detail ...string) {
	if o.Warn != nil {
		o.Warn(text, detail...)
	}
}

// Presets proveo seeds when it creates starship's config.
const (
	presetIcons = "nerd-font-symbols"
	presetPlain = "no-nerd-font"
)

// Apply installs what is missing and records what it installed: starship when none is
// on PATH, a Nerd Font when the host has none, the shell's init line unless the rc
// already starts starship, and proveo's block in starship's config.
func Apply(o Options) (Record, error) {
	rec := ReadRecord(o.StateDir)
	starship, err := o.LookPath("starship")
	if err != nil {
		if starship, err = InstallStarship(o.GOOS, o.GOARCH, o.BinDir, o.Fetch); err != nil {
			return rec, err
		}
		rec.Starship = starship
		o.fact("starship", StarshipVersion, "installed at "+starship)
	} else {
		o.fact("starship", starship, "already on PATH")
	}

	scan, fontDir := FontDirs(o.GOOS, o.Home, o.getenv)
	if HasNerdFont(scan) {
		o.fact("font", "a Nerd Font", "already installed")
	} else {
		fonts, err := InstallFont(fontDir, o.Fetch)
		if err != nil {
			return rec, err
		}
		rec.Fonts = fonts
		o.fact("font", FontFamily, fmt.Sprintf("%d files in %s", len(fonts), fontDir))
	}

	if line := o.Shell.StarshipLine(); line == "" {
		o.warn(cmp.Or(o.Shell.Name, "this shell")+" — no starship line known", "start starship from its startup file yourself")
	} else {
		rc := o.Shell.RCFile(o.GOOS, o.Home)
		body, _ := os.ReadFile(rc)
		if shell.StartsStarship(string(body)) {
			o.fact("rc", rc, "already starts starship")
		} else {
			if err := writeFile(rc, append(body, o.Shell.PromptBlock()...), modeOf(rc, 0o644)); err != nil {
				return rec, err
			}
			rec.RC = rc
			o.fact("rc", rc, "added `"+line+"`")
		}
	}

	config := ConfigPath(o.Home, o.getenv)
	var configDetail []string
	body, err := os.ReadFile(config)
	switch {
	case errors.Is(err, os.ErrNotExist):
		preset := presetPlain
		if o.DrawsGlyphs != nil {
			if icon, asked := o.DrawsGlyphs(); asked && icon {
				preset = presetIcons
			}
		}
		if o.Preset != nil {
			if seeded, err := o.Preset(starship, preset); err == nil {
				body = seeded
				configDetail = append(configDetail, "created from starship's "+preset+" preset")
			}
		}
		rec.ConfigCreated = true
	case err != nil:
		return rec, err
	}
	if err := writeFile(config, []byte(WithSegment(string(body))), modeOf(config, 0o644)); err != nil {
		return rec, err
	}
	rec.Config = config
	o.fact("config", config, append(configDetail, "holds the proveo segment")...)
	return rec, saveRecord(o.StateDir, rec)
}

// modeOf is path's mode, or def when it does not exist.
func modeOf(path string, def os.FileMode) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return def
}

func saveRecord(state string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(recordPath(state), append(b, '\n'), 0o600)
}

// Installed reports whether Undo finds anything of the kit to remove: proveo's block in the rc file or in
// starship's config, or a binary or font file the record says proveo installed.
func Installed(o Options) bool {
	rec := ReadRecord(o.StateDir)
	if body, err := os.ReadFile(rec.RC); rec.RC != "" && err == nil && shell.WithoutPromptBlock(string(body)) != string(body) {
		return true
	}
	if body, err := os.ReadFile(cmp.Or(rec.Config, ConfigPath(o.Home, o.getenv))); err == nil && strings.Contains(string(body), segmentOpen) {
		return true
	}
	return slices.ContainsFunc(append([]string{rec.Starship}, rec.Fonts...), func(p string) bool {
		_, err := os.Lstat(p)
		return p != "" && err == nil
	})
}

// Undo removes proveo's block from the rc file and from starship's config, and the binary and font files
// only when the record says proveo installed them. Each removal that succeeded is a Fact ("rc", "config",
// "starship", "font"), each that failed a Warn; the error joins the failures.
func Undo(o Options) error {
	rec := ReadRecord(o.StateDir)
	var errs []error
	failed := func(text string, err error) {
		errs = append(errs, err)
		o.warn(text, err.Error())
	}
	if rec.RC != "" {
		if body, err := os.ReadFile(rec.RC); err == nil {
			if stripped := shell.WithoutPromptBlock(string(body)); stripped != string(body) {
				if err := writeFile(rec.RC, []byte(stripped), modeOf(rec.RC, 0o644)); err != nil {
					failed(rec.RC+" — starship line not removed", err)
				} else {
					o.fact("rc", rec.RC)
				}
			}
		}
	}
	config := cmp.Or(rec.Config, ConfigPath(o.Home, o.getenv))
	if body, err := os.ReadFile(config); err == nil && strings.Contains(string(body), segmentOpen) {
		stripped := WithoutSegment(string(body))
		if rec.ConfigCreated && strings.TrimSpace(stripped) == "" {
			err = os.Remove(config)
		} else {
			err = writeFile(config, []byte(stripped), modeOf(config, 0o644))
		}
		if err != nil {
			failed(config+" — proveo segment not removed", err)
		} else {
			o.fact("config", config)
		}
	}
	if rec.Starship != "" {
		switch err := os.Remove(rec.Starship); {
		case err == nil:
			o.fact("starship", rec.Starship)
		case !errors.Is(err, os.ErrNotExist):
			failed(rec.Starship+" — not removed", err)
		}
	}
	removed := 0
	var stayed []error
	for _, f := range rec.Fonts {
		switch err := os.Remove(f); {
		case err == nil:
			removed++
		case !errors.Is(err, os.ErrNotExist):
			stayed = append(stayed, err)
		}
	}
	if removed > 0 {
		o.fact("font", FontFamily, fmt.Sprintf("%d files in %s", removed, filepath.Dir(rec.Fonts[0])))
	}
	if len(stayed) > 0 {
		failed(fmt.Sprintf("%s — %d files not removed", FontFamily, len(stayed)), stayed[0])
	}
	if err := os.Remove(recordPath(o.StateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		failed(recordPath(o.StateDir)+" — not removed", err)
	}
	return errors.Join(errs...)
}
