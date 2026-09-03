package run

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
)

func stripForm(tier, creds string) *choiceui.Form {
	rows := []choiceui.Row{{Label: "egress", Options: []string{tier}}}
	if creds != "" {
		rows = append(rows, choiceui.Row{Label: "credentials", Options: []string{creds}})
	}
	rows = append(rows, evidenceRow(EvidenceDefault))
	return &choiceui.Form{Rows: rows}
}

// The projection reads exactly what Selection reads, so the picture can never
// claim a tier the form is not showing.
func TestFrameAgreesWithTheForm(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tier, creds string
		sbx         bool
	}{
		{"allow-all", "broker", true}, {"balanced", "forward", true}, {"deny-all", "broker", true},
		{"open", "broker", false}, {"allowlist", "forward", false}, {"review", "broker", false},
	} {
		f := stripForm(c.tier, c.creds)
		fr := topologyOf(manifest.Manifest{Docker: manifest.DockerSbx}, "claudecode", c.sbx, c.tier, "broker")(f, 0)
		if !strings.HasPrefix(fr.Caption, c.tier+" · "+c.creds) {
			t.Errorf("%s/%s: caption %q does not name the selection", c.tier, c.creds, fr.Caption)
		}
		if fr.Key != keyHomeOf(c.creds) {
			t.Errorf("%s/%s: the key is not where the credentials row says", c.tier, c.creds)
		}
	}
}

// One lane always survives. deny-all is the ABSENCE of an allow rule and the Kit
// can only add, so the agent still reaches its model — a frame with no lane at
// all would say it cannot think.
func TestEveryPostureLeavesTheProviderReachable(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tier string
		sbx  bool
	}{
		{"allow-all", true}, {"balanced", true}, {"deny-all", true},
		{"open", false}, {"allowlist", false},
	} {
		lane, open, _ := lanesOf(c.tier, c.sbx)
		if open < 1 {
			t.Errorf("%s: %d lanes survive; the agent could not reach its provider", c.tier, open)
		}
		if lane == choiceui.LaneAsked {
			t.Errorf("%s: only review asks, got LaneAsked", c.tier)
		}
	}
	// review is the exception, and the reason is different: nothing has been
	// consented YET, which is a question rather than a refusal.
	if lane, open, _ := lanesOf("review", false); lane != choiceui.LaneAsked || open != 0 {
		t.Errorf("review must draw no lane and ask instead, got lane=%v open=%d", lane, open)
	}
}

// The two backends put a different party in the path, and only one docker shape
// has nobody there at all.
func TestHopNamesTheRightParty(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tier, creds string
		sbx         bool
		want        string
	}{
		{"deny-all", "broker", true, "sbx proxy"},
		{"allow-all", "forward", true, "sbx proxy"},
		{"open", "broker", false, "mitm"},
		{"open", "forward", false, ""},
		{"allowlist", "forward", false, "mitm + squid"},
		{"review", "broker", false, "mitm + squid"},
	} {
		if got := hopOf(c.tier, c.creds, c.sbx); got != c.want {
			t.Errorf("%s/%s sbx=%v: hop = %q, want %q", c.tier, c.creds, c.sbx, got, c.want)
		}
	}
}

// Speaking is read the same way the RUN reads it. It used to come from a
// checkbox pair whose "neither ticked" state quietly meant default and whose
// Selected was only the cursor; as one radio there is no third state to
// disagree about, and the figure cannot drift from the run.
func TestSpeakingIsTheAnswerNotTheCursor(t *testing.T) {
	t.Parallel()
	proj := topologyOf(manifest.Manifest{}, "opencode", false, "open", "broker")
	for _, c := range []struct {
		level string
		want  bool
	}{{EvidenceDefault, false}, {EvidenceVerbose, true}} {
		f := stripForm("open", "broker")
		ev := &f.Rows[len(f.Rows)-1]
		for i, o := range ev.Options {
			if o == c.level {
				ev.Selected = i
			}
		}
		if got := proj(f, 0).Speaking; got != c.want {
			t.Errorf("%s: Speaking = %v, want %v", c.level, got, c.want)
		}
	}
}

// A row with one option is dropped by applicableRows, so the key still needs a
// home — cursor is the harness that hits this.
func TestTheKeyHasAHomeWhenTheRowWasDropped(t *testing.T) {
	t.Parallel()
	f := stripForm("allow-all", "") // no credentials row at all
	fr := topologyOf(manifest.Manifest{Docker: manifest.DockerSbx}, "cursor", true, "allow-all", "forward")(f, 0)
	if fr.Key != choiceui.KeyInSquare {
		t.Errorf("with no row to read, the key must fall back to the resolved value, got %v", fr.Key)
	}
}

// The cursor selects the emphasis; a label with nothing in the figure owns none.
func TestFocusMapsRowsToElements(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: "egress"}, {Label: "credentials"}, {Label: "auth"},
		{Label: rowExecution}, {Label: rowInterface}, {Label: evidenceLabel}, {Label: "mystery"},
	}}
	want := []choiceui.Focus{choiceui.FocusHop, choiceui.FocusKey, choiceui.FocusKey,
		choiceui.FocusSquare, choiceui.FocusReturn, choiceui.FocusSay, choiceui.FocusNone}
	for i, w := range want {
		if got := focusOf(f, i); got != w {
			t.Errorf("row %q: focus %v, want %v", f.Rows[i].Label, got, w)
		}
	}
	if got := focusOf(f, 99); got != choiceui.FocusNone {
		t.Errorf("a cursor off the end owns nothing, got %v", got)
	}
}

// The tier crosses from posture to choiceui with no translation, because the
// two names denote ONE type. stripGlyphs used to sit here doing the conversion
// and collapsing "off" to ASCII on the way; the collapse now lives in
// choiceui.glyphsFor, asserted by TestGlyphsOffDrawsTheASCIISet, and this is
// the guard that the two vocabularies have not drifted back apart.
func TestTheGlyphTierNeedsNoTranslation(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   posture.GlyphMode
		want choiceui.GlyphTier
	}{
		{posture.GlyphsNerd, choiceui.GlyphsNerd},
		{posture.GlyphsASCII, choiceui.GlyphsASCII},
		{posture.GlyphsOff, choiceui.GlyphsOff},
	} {
		if c.in != c.want {
			t.Errorf("%v: posture and choiceui disagree, got %v want %v", c.in, c.in, c.want)
		}
	}
}

// The cursor harness declares one egress mode AND one credential mode, so
// applicableRows drops BOTH rows and the form answers "" to each. Reading the
// form alone drew a "mitm + squid" hop for a run whose plan is a bare bridge
// network with no sidecar — the strip inventing a boundary that is not there.
func TestBothAxesFallBackWhenTheirRowWasDropped(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{evidenceRow(EvidenceDefault)}}
	fr := topologyOf(manifest.Manifest{Docker: manifest.DockerDind}, "cursor", false, "open", "forward")(f, 0)
	if fr.Hop != "" {
		t.Errorf("open + forward has no hop at all; the strip drew %q", fr.Hop)
	}
	if fr.Key != choiceui.KeyInSquare {
		t.Errorf("forward puts the real key inside the container, got %v", fr.Key)
	}
	if !strings.HasPrefix(fr.Caption, "open · forward") {
		t.Errorf("the caption must name the resolved values, got %q", fr.Caption)
	}
}

// proveo could not read the host baseline, so it may be anything. Drawing the
// most permissive posture and captioning it "the host allows every destination"
// would state as fact the one thing nobody measured.
func TestAnUnreadableBaselineIsDrawnAtItsTightest(t *testing.T) {
	t.Parallel()
	lane, open, refused := lanesOf("unreadable", true)
	if lane != choiceui.LaneScreened || open != 1 || refused != 2 {
		t.Errorf("an unknown baseline must draw the tightest picture, got %v %d/%d", lane, open, refused)
	}
	if allow, _, _ := lanesOf("allow-all", true); allow == lane {
		t.Error("unreadable must not render identically to allow-all")
	}
	fr := choiceui.Frame{}
	if got := captionOf(fr, "unreadable", "broker", true); !strings.Contains(got, "could not read") {
		t.Errorf("the caption must say the baseline is unknown, got %q", got)
	}
}

// A zero Frame must not read as a wide-open topology: the enum's zero value is
// the screened hop precisely so a forgotten field fails to the tighter picture.
func TestTheZeroFrameIsNotWideOpen(t *testing.T) {
	t.Parallel()
	if (choiceui.Frame{}).Lane != choiceui.LaneScreened {
		t.Error("the zero LaneKind must be the screened hop, not the watched one")
	}
}

// The interface dot names everything the agent drives, and chrome without
// browser must not read as "interface + chrome" with the tui silently dropped.
func TestInterfaceNamesEveryDrivenSurface(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		browser, chrome bool
		want            string
	}{
		{false, false, "tui"},
		{true, false, "tui + browser"},
		{false, true, "tui + chrome"},
		{true, true, "tui + browser + chrome"},
	} {
		f := &choiceui.Form{Rows: []choiceui.Row{{Label: rowInterface, Multi: true,
			Options: []string{addonTUI, addonBrowser, addonChrome},
			On:      []bool{true, c.browser, c.chrome}}}}
		if got := interfaceOf(f); got != c.want {
			t.Errorf("browser=%v chrome=%v: %q, want %q", c.browser, c.chrome, got, c.want)
		}
	}
}
