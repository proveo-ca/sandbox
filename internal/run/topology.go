// Package-local projection of the choice form onto the topology strip.
// SPEC: _spec/internal/choiceui/topology-strip.puml, _spec/internal/sbx/policy-baseline.puml
package run

import (
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
)

// topologyOf builds the projection the strip is painted from. This is the ONLY
// place the picture learns what "allow-all" or "docker (sandbox)" mean:
// internal/choiceui owns the geometry and the glyphs and stays ignorant of the
// vocabulary, so a new tier renames a string here and redraws nothing.
//
// The returned function is called at PAINT time, once per frame, and reads the
// same values Selection returns — so the figure cannot drift from the boxes
// above it, and it follows a cursor that `move` never reports.
func topologyOf(man manifest.Manifest, target string, sbxBackend bool, tierDefault, credsDefault string) func(*choiceui.Form, int) *choiceui.Frame {
	return func(f *choiceui.Form, cursor int) *choiceui.Frame {
		// BOTH rows need a fallback, because applicableRows drops any axis with
		// one option — and cursor declares exactly one of each. Reading the form
		// alone drew a "mitm + squid" hop for a run whose plan is a bare bridge
		// network with no sidecar at all: the strip inventing a boundary that
		// does not exist is the worst thing this picture can do.
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
			Square:    squareOf(man, target),
			Hop:       hopOf(tier, creds, sbxBackend),
			Interface: interfaceOf(f),
			Key:       keyHomeOf(creds),
			Lane:      lane,
			Open:      open,
			Refused:   refused,
			Speaking:  evidenceFrom(f.Selections(evidenceLabel)) == EvidenceVerbose,
			Focus:     focusOf(f, cursor),
		}
		fr.Caption = captionOf(fr, tier, creds, sbxBackend)
		return &fr
	}
}

// lanesOf is how many lanes leave the hop, and how many die at it.
//
// The counts are ILLUSTRATIVE, not measured. A real "how many hosts survive"
// would mean reading the Kit allowlist (sandbox.Spec: harness capabilities plus
// detected providers) or Squid's config at prompt time, which is a bigger change
// than the picture is worth — so the captions below say what the tier DOES and
// never claim a number.
//
// One lane always survives. On the strictest posture the agent still reaches its
// model — deny-all is the absence of an allow rule, and the Kit can only add —
// so a frame with no lane at all would say the agent cannot think.
func lanesOf(tier string, sbx bool) (choiceui.LaneKind, int, int) {
	if sbx {
		switch tier {
		case "allow-all":
			return choiceui.LaneWatched, 3, 0
		case "balanced":
			return choiceui.LaneScreened, 2, 1
		default:
			// deny-all, and — deliberately — the unreadable baseline. proveo could
			// not read the host policy, so it may be anything; drawing the most
			// permissive posture and captioning it "the host allows every
			// destination" would state as fact the one thing nobody measured.
			// An unknown boundary is drawn as the tightest one.
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

// hopOf names who is in the path. An empty name means nobody is: that is the one
// docker shape with no MITM, and the columns are held open rather than closed up.
func hopOf(tier, creds string, sbx bool) string {
	if sbx {
		// There is no proveo egress layer here at all. "broker" still means the
		// value never reaches the agent, but the party doing it is sbx.
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

// squareOf labels the container the agent runs in with the daemon behind it.
func squareOf(man manifest.Manifest, target string) string {
	switch man.Docker {
	case manifest.DockerSbx:
		return "sbx · " + target
	case manifest.DockerDind:
		return "dind · " + target
	}
	return target
}

// interfaceOf names what the agent can drive. The TUI is always there; the rest
// join it, which is why they read as additions rather than as alternatives.
func interfaceOf(f *choiceui.Form) string {
	driven := []string{"tui"}
	if rowTicked(f, rowInterface, addonBrowser) {
		driven = append(driven, "browser")
	}
	if rowTicked(f, rowInterface, addonChrome) {
		driven = append(driven, "chrome")
	}
	if len(driven) == 1 {
		return "interface"
	}
	return "interface: " + strings.Join(driven, " + ")
}

// focusOf maps the cursor's row onto the element that row owns.
//
// A locked row is never focused, because `move` skips it — so on a sbx harness,
// where the baseline row is locked, the hop is simply never the highlighted
// element. That is honest: the row is locked precisely because there is nothing
// there to choose.
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

// captionOf states the same facts as the figure, in one sentence, for the
// terminal too short to draw the figure at all.
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

// stripGlyphs maps the shared PROVEO_GLYPHS ladder onto the two tiers the strip
// has. "off" draws the ASCII figure rather than nothing: off says the terminal
// cannot render decoration, not that the operator wants less information, and
// the figure carries information the checkboxes cannot.
func stripGlyphs(m posture.GlyphMode) choiceui.GlyphTier {
	if m == posture.GlyphsNerd {
		return choiceui.GlyphsNerd
	}
	return choiceui.GlyphsASCII
}
