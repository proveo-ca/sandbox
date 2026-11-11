// SPEC: _spec/internal/promptkit/prompt-kit.puml
package promptkit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	Report      func(format string, a ...any)
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

func (o Options) say(format string, a ...any) {
	if o.Report != nil {
		o.Report(format, a...)
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
		o.say("starship %s installed at %s", StarshipVersion, starship)
	} else {
		o.say("starship is already on PATH: %s", starship)
	}

	scan, fontDir := FontDirs(o.GOOS, o.Home, o.getenv)
	if HasNerdFont(scan) {
		o.say("a Nerd Font is already installed")
	} else {
		fonts, err := InstallFont(fontDir, o.Fetch)
		if err != nil {
			return rec, err
		}
		rec.Fonts = fonts
		o.say("%s installed: %d files in %s", FontFamily, len(fonts), fontDir)
	}

	if line := o.Shell.StarshipLine(); line == "" {
		o.say("no rc line known for this shell — start starship from its startup file yourself")
	} else {
		rc := o.Shell.RCFile(o.GOOS, o.Home)
		body, _ := os.ReadFile(rc)
		if shell.StartsStarship(string(body)) {
			o.say("%s already starts starship", rc)
		} else {
			if err := writeFile(rc, append(body, o.Shell.PromptBlock()...), modeOf(rc, 0o644)); err != nil {
				return rec, err
			}
			rec.RC = rc
			o.say("added `%s` to %s — it replaces this shell's prompt", line, rc)
		}
	}

	config := ConfigPath(o.Home, o.getenv)
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
				o.say("%s created from starship's %s preset", config, preset)
			}
		}
		if preset == presetPlain {
			o.say("to see icons, set your terminal's font to %s, then `starship preset %s -o %s`", FontFamily, presetIcons, config)
		}
		rec.ConfigCreated = true
	case err != nil:
		return rec, err
	}
	if err := writeFile(config, []byte(WithSegment(string(body))), modeOf(config, 0o644)); err != nil {
		return rec, err
	}
	rec.Config = config
	o.say("the proveo segment is in %s", config)
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

// Undo removes proveo's block from the rc file and from starship's config, and the
// binary and font files only when the record says proveo installed them.
func Undo(o Options) error {
	rec := ReadRecord(o.StateDir)
	var errs []error
	if rec.RC != "" {
		if body, err := os.ReadFile(rec.RC); err == nil {
			if stripped := shell.WithoutPromptBlock(string(body)); stripped != string(body) {
				errs = append(errs, writeFile(rec.RC, []byte(stripped), modeOf(rec.RC, 0o644)))
				o.say("removed the starship line from %s", rec.RC)
			}
		}
	}
	config := rec.Config
	if config == "" {
		config = ConfigPath(o.Home, o.getenv)
	}
	if body, err := os.ReadFile(config); err == nil && strings.Contains(string(body), segmentOpen) {
		stripped := WithoutSegment(string(body))
		if rec.ConfigCreated && strings.TrimSpace(stripped) == "" {
			errs = append(errs, os.Remove(config))
		} else {
			errs = append(errs, writeFile(config, []byte(stripped), modeOf(config, 0o644)))
		}
		o.say("removed the proveo segment from %s", config)
	}
	if rec.Starship != "" {
		if err := os.Remove(rec.Starship); err == nil {
			o.say("removed %s", rec.Starship)
		}
	}
	for _, f := range rec.Fonts {
		_ = os.Remove(f)
	}
	if len(rec.Fonts) > 0 {
		o.say("removed %d %s files", len(rec.Fonts), FontFamily)
	}
	if err := os.Remove(recordPath(o.StateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
