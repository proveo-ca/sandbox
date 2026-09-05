// Package-local projection of the choice form onto the topology strip.
//
// SPEC: _spec/internal/choiceui/topology-strip.puml, _spec/internal/sbx/policy-baseline.puml
package run

import (
	"os"
	"runtime"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
)

func topologyOf(man manifest.Manifest, target string, sbxBackend bool, tierDefault, credsDefault string) func(*choiceui.Form, int) *choiceui.Frame {
	return func(f *choiceui.Form, cursor int) *choiceui.Frame {
		tier := f.Selection("egress")
		if tier == "" {
			tier = tierDefault
		}
		creds := f.Selection("credentials")
		if creds == "" {
			creds = credsDefault
		}
		lane, open, refused := lanesOf(tier, sbxBackend)
		fr := choiceui.Frame{
			Host:      hostAccount(),
			HostOS:    "(" + hostPlatform() + ")",
			Square:    squareOf(man, target),
			Hop:       hopOf(tier, creds, sbxBackend),
			Interface: interfaceOf(f),
			Key:       keyHomeOf(creds),
			Lane:      lane,
			Open:      open,
			Refused:   refused,
			Speaking:  f.Selection(evidenceLabel) == EvidenceVerbose,
			Focus:     focusOf(f, cursor),
		}
		fr.Caption = captionOf(fr, tier, creds, sbxBackend)
		return &fr
	}
}

func lanesOf(tier string, sbx bool) (choiceui.LaneKind, int, int) {
	if sbx {
		switch tier {
		case "allow-all":
			return choiceui.LaneWatched, 3, 0
		case "balanced":
			return choiceui.LaneScreened, 2, 1
		default:
			return choiceui.LaneScreened, 1, 2
		}
	}
	switch tier {
	case "allowlist":
		return choiceui.LaneScreened, 2, 1
	case "review":
		return choiceui.LaneAsked, 0, 0
	default: // open
		return choiceui.LaneWatched, 3, 0
	}
}

func hopOf(tier, creds string, sbx bool) string {
	if sbx {
		return "sbx proxy"
	}
	if tier == "open" {
		if creds == "forward" {
			return ""
		}
		return "mitm"
	}
	return "mitm + squid"
}

func keyHomeOf(creds string) choiceui.KeyHome {
	switch creds {
	case "forward":
		return choiceui.KeyInSquare
	case "broker":
		return choiceui.KeyAtHop
	}
	return choiceui.KeyAtHost
}

func hostAccount() string {
	for _, k := range []string{"USER", "LOGNAME", "USERNAME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return "host"
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

func squareOf(man manifest.Manifest, target string) string {
	if man.Docker == manifest.DockerSbx {
		return "sbx · " + target
	}
	return target
}

func interfaceOf(f *choiceui.Form) string {
	driven := []string{"tui"}
	if rowTicked(f, rowInterface, addonBrowser) {
		driven = append(driven, "browser")
	}
	if rowTicked(f, rowInterface, addonChrome) {
		driven = append(driven, "chrome")
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
	case "credentials", "auth":
		return choiceui.FocusKey
	case rowExecution:
		return choiceui.FocusSquare
	case rowInterface:
		return choiceui.FocusReturn
	case evidenceLabel:
		return choiceui.FocusSay
	}
	return choiceui.FocusNone
}

func captionOf(fr choiceui.Frame, tier, creds string, sbx bool) string {
	var what string
	switch {
	case sbx && tier != "allow-all" && tier != "balanced" && tier != "deny-all":
		what = "proveo could not read the host baseline, so this is drawn at its tightest"
	case sbx && tier == "deny-all":
		what = "only proveo's allowlist gets out — your provider, and nothing else"
	case sbx && tier == "balanced":
		what = "the host's own allows, plus proveo's"
	case sbx:
		what = "the host allows every destination, so proveo's list adds reach"
	case tier == "review":
		what = "nothing crosses until you answer, over your suspended agent TUI"
	case tier == "allowlist":
		what = "everything is screened at the hop, and the unlisted is refused"
	case fr.Hop == "":
		what = "BYPASS: nothing is in the path at all"
	default:
		what = "the hop watches but does not filter"
	}
	where := "the key never leaves your machine"
	switch fr.Key {
	case choiceui.KeyInSquare:
		where = "the real key rides inside the container"
	case choiceui.KeyAtHop:
		where = "the key stops at the hop and never enters the container"
	}
	name := tier
	if name == "" {
		name = "unreadable"
	}
	return name + " · " + creds + " — " + what + "; " + where
}
