// SPEC: _spec/internal/choiceui/topology-strip.puml, _spec/internal/sbx/policy-baseline.puml
package run

import (
	"os"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/proveo-ca/proveo/internal/choiceui"
)

func topologyOf(target string, credsDefault string) func(*choiceui.Form, int) *choiceui.Frame {
	return func(f *choiceui.Form, cursor int) *choiceui.Frame {
		tier := egressTier(f, cursor, "")
		creds := rowChoice(f, cursor, "credentials", credsDefault)
		lane, open, refused := lanesOf(tier)
		option := f.Selection(rowModel)
		model := modelShort(option)
		host, hostRight, hostModel := hostAccount(), "", ""
		hostOS := hostOSArch()
		square := squareOf(target)
		hostLit := false
		if name, ok := localBesideHost(option); ok {
			hostRight = "Ollama"
			hostModel = name
			hostLit = cursor >= 0 && cursor < len(f.Rows) && f.Rows[cursor].Label == rowModel
		} else {
			square = squareWith(square, model)
		}
		key := choiceui.KeyAtHop
		if creds == "forward" {
			key = choiceui.KeyInSquare
		}
		if model != "" {
			key = choiceui.KeyNone
		}
		fr := choiceui.Frame{
			Host:      host,
			HostOS:    hostOS,
			HostRight: hostRight,
			HostModel: hostModel,
			HostLit:   hostLit,
			Square:    square,
			Hop:       "sbx proxy",
			Interface: interfaceOf(f),
			Key:       key,
			Lane:      lane,
			Open:      open,
			Refused:   refused,
			Speaking:  f.Selection(evidenceLabel) == EvidenceVerbose,
			Focus:     focusOf(f, cursor),
		}
		fr.Caption = captionOf(fr, tier, creds)
		return &fr
	}
}

func egressTier(f *choiceui.Form, cursor int, fallback string) string {
	return rowChoice(f, cursor, "egress", fallback)
}

func rowChoice(f *choiceui.Form, cursor int, label, fallback string) string {
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != label {
			continue
		}
		idx := r.Selected
		if r.Locked && i == cursor {
			idx = r.Hover
		}
		if idx >= 0 && idx < len(r.Options) {
			return r.Options[idx]
		}
	}
	return fallback
}

// lanesOf draws sbx's baseline: allow-all watches, balanced screens some, anything else screens most.
func lanesOf(baseline string) (choiceui.LaneKind, int, int) {
	switch baseline {
	case "allow-all":
		return choiceui.LaneWatched, 3, 0
	case "balanced":
		return choiceui.LaneScreened, 2, 1
	default:
		return choiceui.LaneScreened, 1, 2
	}
}

func hostAccount() string {
	for _, k := range []string{"USER", "LOGNAME", "USERNAME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return "host"
}

func archShort() string {
	switch runtime.GOARCH {
	case "amd64", "386":
		return "x86"
	case "arm64", "arm":
		return "arm"
	default:
		return runtime.GOARCH
	}
}

func hostOSArch() string {
	name := hostPlatform()
	arch := archShort()
	cell := name + "/" + arch
	if utf8.RuneCountInString(cell) <= choiceui.HostSide {
		return cell
	}
	switch name {
	case "linux":
		name = "lin"
	case "windows":
		name = "win"
	case "macOS":
		name = "mac"
	}
	cell = name + "/" + arch
	if r := []rune(cell); len(r) > choiceui.HostSide {
		return string(r[:choiceui.HostSide-1]) + "…"
	}
	return cell
}

func hostPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	}
	return runtime.GOOS
}

// squareWith appends " + model" to the square, trimming model to what choiceui.SquareMax leaves.
func squareWith(square, model string) string {
	room := choiceui.SquareMax - utf8.RuneCountInString(square+" + ")
	if model == "" || room < 1 {
		return square
	}
	if r := []rune(model); len(r) > room {
		model = string(r[:room-1]) + "…"
	}
	return square + " + " + model
}

func vmPlatform() string {
	return "linux/" + archShort()
}

func squareOf(target string) string {
	return "sbx (" + vmPlatform() + ") · " + target
}

func interfaceOf(f *choiceui.Form) string {
	driven := []string{"tui"}
	if rowTicked(f, rowInterface, addonBrowser) {
		driven = append(driven, "browser")
	}
	if rowTicked(f, rowInterface, addonChrome) || rowTicked(f, rowInterface, addonHostCDP) {
		driven = append(driven, "chrome")
	}
	if rowTicked(f, rowInterface, addonAndroid) {
		driven = append(driven, "android")
	}
	return strings.Join(driven, " + ")
}

func focusOf(f *choiceui.Form, cursor int) choiceui.Focus {
	if cursor < 0 || cursor >= len(f.Rows) {
		return choiceui.FocusNone
	}
	switch f.Rows[cursor].Label {
	case "egress":
		return choiceui.FocusHop
	case "credentials", "auth", rowModel:
		return choiceui.FocusKey
	case rowExecution, rowPorts, rowApps:
		return choiceui.FocusSquare
	case rowInterface:
		return choiceui.FocusReturn
	case evidenceLabel:
		return choiceui.FocusSay
	}
	return choiceui.FocusNone
}

func captionOf(fr choiceui.Frame, tier, creds string) string {
	var what string
	switch tier {
	case "allow-all":
		what = "the host allows every destination"
	case "balanced":
		what = "the host's own allows decide what gets out"
	case "deny-all":
		what = "only the host's explicit allows get out"
	default:
		what = "proveo could not read the host baseline, so this is drawn at its tightest"
	}
	where := "the key never leaves your machine"
	switch fr.Key {
	case choiceui.KeyInSquare:
		where = "the real key rides inside the container"
	case choiceui.KeyAtHop:
		where = "the keys are only seen by the host and the cloud recipients, never the agent"
	case choiceui.KeyNone:
		where = "no API key is in play"
	}
	name := tier
	if name == "" {
		name = "unreadable"
	}
	return name + " · " + creds + " — " + what + "; " + where
}
