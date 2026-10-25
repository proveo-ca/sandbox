// SPEC: _spec/internal/choiceui/topology-strip.puml
package run

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
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
	for _, c := range []struct{ tier, creds string }{
		{"allow-all", "broker"}, {"balanced", "forward"}, {"deny-all", "broker"},
	} {
		f := stripForm(c.tier, c.creds)
		fr := topologyOf("claudecode", "broker")(f, 0)
		if !strings.HasPrefix(fr.Caption, c.tier+" · "+c.creds) {
			t.Errorf("%s/%s: caption %q does not name the selection", c.tier, c.creds, fr.Caption)
		}
		if c.creds != "forward" && fr.Key != choiceui.KeyAtHop {
			t.Errorf("%s/%s: sbx holds the secret at its proxy, got %v", c.tier, c.creds, fr.Key)
		}
	}
}

func TestEveryPostureLeavesTheProviderReachable(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{"allow-all", "balanced", "deny-all"} {
		lane, open, _ := lanesOf(tier)
		if open < 1 {
			t.Errorf("%s: %d lanes survive; the agent could not reach its provider", tier, open)
		}
		if lane == choiceui.LaneAsked {
			t.Errorf("%s: nothing asks on sbx, got LaneAsked", tier)
		}
	}
}

func TestSpeakingIsTheAnswerNotTheCursor(t *testing.T) {
	t.Parallel()
	proj := topologyOf("opencode", "broker")
	for _, c := range []struct {
		level string
		want  bool
	}{{EvidenceDefault, false}, {EvidenceVerbose, true}} {
		f := stripForm("allow-all", "broker")
		ev := &f.Rows[len(f.Rows)-1]
		for i, o := range ev.Options {
			if evidenceOf(o) == c.level {
				ev.Selected = i
			}
		}
		if got := proj(f, 0).Speaking; got != c.want {
			t.Errorf("%s: Speaking = %v, want %v", c.level, got, c.want)
		}
	}
}

func TestTheKeyHasAHomeWhenTheRowWasDropped(t *testing.T) {
	t.Parallel()
	f := stripForm("allow-all", "") // no credentials row at all
	fr := topologyOf("cursor", "broker")(f, 0)
	if fr.Key != choiceui.KeyAtHop {
		t.Errorf("with no row to read, the key still lives at the sbx proxy, got %v", fr.Key)
	}
	if !strings.Contains(fr.Caption, "only seen by the host and the cloud recipients") {
		t.Errorf("the caption must say who sees the key, got %q", fr.Caption)
	}
}

func TestForwardPutsTheKeyInTheSandbox(t *testing.T) {
	t.Parallel()
	f := stripForm("allow-all", "forward")
	fr := topologyOf("cursor", "broker")(f, 0)
	if fr.Key != choiceui.KeyInSquare {
		t.Errorf("forward rides the sandbox env, got %v", fr.Key)
	}
	if fr.Hop != "sbx proxy" {
		t.Errorf("hop = %q, want sbx proxy", fr.Hop)
	}
}

// The cursor selects the emphasis; a label with nothing in the figure owns none.
func TestFocusMapsRowsToElements(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: "egress"}, {Label: "credentials"}, {Label: "auth"},
		{Label: rowExecution}, {Label: rowInterface}, {Label: evidenceLabel}, {Label: "mystery"}, {Label: mcpLabel},
	}}
	want := []choiceui.Focus{choiceui.FocusHop, choiceui.FocusKey, choiceui.FocusKey,
		choiceui.FocusSquare, choiceui.FocusReturn, choiceui.FocusSay, choiceui.FocusNone, choiceui.FocusHop}
	for i, w := range want {
		if got := focusOf(f, i); got != w {
			t.Errorf("row %q: focus %v, want %v", f.Rows[i].Label, got, w)
		}
	}
	if got := focusOf(f, 99); got != choiceui.FocusNone {
		t.Errorf("a cursor off the end owns nothing, got %v", got)
	}
}

func TestTheGlyphTierNeedsNoTranslation(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   posture.GlyphMode
		want choiceui.GlyphTier
	}{
		{posture.GlyphsNerd, choiceui.GlyphsNerd},
		{posture.GlyphsASCII, choiceui.GlyphsASCII},
		{posture.GlyphsOff, choiceui.GlyphsOff},
		{posture.GlyphsSafe, choiceui.GlyphsSafe},
	} {
		if c.in != c.want {
			t.Errorf("%v: posture and choiceui disagree, got %v want %v", c.in, c.in, c.want)
		}
	}
}

func TestAnUnreadableBaselineIsDrawnAtItsTightest(t *testing.T) {
	t.Parallel()
	lane, open, refused := lanesOf("unreadable")
	if lane != choiceui.LaneScreened || open != 1 || refused != 2 {
		t.Errorf("an unknown baseline must draw the tightest picture, got %v %d/%d", lane, open, refused)
	}
	if allow, _, _ := lanesOf("allow-all"); allow == lane {
		t.Error("unreadable must not render identically to allow-all")
	}
	fr := choiceui.Frame{}
	if got := captionOf(fr, "unreadable", "broker"); !strings.Contains(got, "could not read") {
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
			Options: []string{addonTUI, addonBrowser, addonHostCDP},
			On:      []bool{true, c.browser, c.chrome}}}}
		if got := interfaceOf(f); got != c.want {
			t.Errorf("browser=%v chrome=%v: %q, want %q", c.browser, c.chrome, got, c.want)
		}
	}
}

func TestHoveringTheHostDropsTheSandbox(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: "credentials", Options: []string{"forward", "broker"}, Selected: 1, Locked: true, Off: []bool{true, false}},
		{Label: "egress", Options: []string{"allow-all"}, Selected: 0},
		{Label: rowExecution, Options: []string{addonHost, addonSandbox}, Selected: 0, Multi: true, Off: []bool{true, true}, On: []bool{false, true}},
	}}
	onHost := topologyOf("opencode", "broker")(f, 2)
	if !onHost.Direct || onHost.Square != "opencode" || onHost.Hop != "" || onHost.Key != choiceui.KeyInSquare {
		t.Errorf("hovering host: direct=%v square=%q hop=%q key=%v", onHost.Direct, onHost.Square, onHost.Hop, onHost.Key)
	}
	if !strings.Contains(onHost.Caption, "forward") || !strings.Contains(onHost.Caption, "rides with the agent") {
		t.Errorf("caption %q must show the key forwarded", onHost.Caption)
	}
	if f.Rows[0].Selected != 1 || !f.Rows[0].Off[0] || !f.Rows[0].Locked {
		t.Error("the preview selected forward")
	}
	if f.Rows[2].On[0] || !f.Rows[2].Off[0] {
		t.Error("the preview enabled host")
	}
	f.Rows[2].Selected = 1
	onSandbox := topologyOf("opencode", "broker")(f, 2)
	if onSandbox.Direct || onSandbox.Square != squareOf("opencode") || onSandbox.Hop != "sbx proxy" || onSandbox.Key != choiceui.KeyAtHop {
		t.Errorf("hovering sandbox: direct=%v square=%q hop=%q key=%v", onSandbox.Direct, onSandbox.Square, onSandbox.Hop, onSandbox.Key)
	}
	f.Rows[2].Selected = 0
	away := topologyOf("opencode", "broker")(f, 1)
	if away.Direct || away.Hop != "sbx proxy" || away.Key != choiceui.KeyAtHop || !strings.Contains(away.Square, "sbx") {
		t.Errorf("cursor elsewhere: direct=%v square=%q hop=%q key=%v", away.Direct, away.Square, away.Hop, away.Key)
	}
}

func TestHoveringForwardPutsTheKeyBesideTheAgent(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: "egress", Options: []string{"allow-all"}, Selected: 0, Locked: true},
		{Label: "credentials", Options: []string{"forward", "broker"}, Selected: 1, Hover: 0, Locked: true},
	}}
	onRow := topologyOf("claudecode", "broker")(f, 1)
	if onRow.Key != choiceui.KeyInSquare {
		t.Errorf("hovering forward: key = %v, want it beside the agent", onRow.Key)
	}
	if !strings.Contains(onRow.Caption, "forward") {
		t.Errorf("caption %q must name the hovered mode", onRow.Caption)
	}
	f.Rows[1].Hover = 1
	onBroker := topologyOf("claudecode", "broker")(f, 1)
	if onBroker.Key != choiceui.KeyAtHop {
		t.Errorf("hovering broker: key = %v, want it at the hop", onBroker.Key)
	}
	f.Rows[1].Hover = 0
	away := topologyOf("claudecode", "broker")(f, 0)
	if away.Key != choiceui.KeyAtHop {
		t.Errorf("cursor elsewhere: key = %v, want the selection at the hop", away.Key)
	}
}

func TestTheFigureFollowsTheHoveredBaseline(t *testing.T) {
	t.Parallel()
	const egressRowIdx = 0
	form := &choiceui.Form{Rows: []choiceui.Row{{
		Label:    "egress",
		Options:  []string{"allow-all", "balanced", "deny-all"},
		Locked:   true,
		Selected: 2, // the host is on deny-all
	}}}

	cases := []struct {
		name          string
		hover, cursor int
		wantTier      string
		open, refused int
	}{
		{"hovering balanced previews balanced", 1, egressRowIdx, "balanced", 2, 1},
		{"hovering allow-all previews allow-all", 0, egressRowIdx, "allow-all", 3, 0},
		{"hovering deny-all matches the host", 2, egressRowIdx, "deny-all", 1, 2},
		// The cursor elsewhere means nobody is reading this row: show what is
		// actually in force.
		{"cursor on another row shows the selection", 1, 7, "deny-all", 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form.Rows[egressRowIdx].Hover = tc.hover
			if got := egressTier(form, tc.cursor, "open"); got != tc.wantTier {
				t.Fatalf("egressTier = %q, want %q", got, tc.wantTier)
			}
			_, open, refused := lanesOf(tc.wantTier)
			if open != tc.open || refused != tc.refused {
				t.Errorf("lanesOf(%q) = %d open, %d refused; want %d and %d",
					tc.wantTier, open, refused, tc.open, tc.refused)
			}
		})
	}
}

func modelForm(model string) *choiceui.Form {
	f := stripForm("allow-all", "broker")
	f.Rows = append(f.Rows, choiceui.Row{Label: rowModel, Options: []string{model}})
	return f
}

func TestTheKeyShowsOnlyForAPIKeys(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		option, square, hostModel string
		key                       choiceui.KeyHome
	}{
		{modelAPIKeys, squareOf("hermes"), "", choiceui.KeyAtHop},
		{credentials.AuthSubscription, squareWith(squareOf("hermes"), "plan"), "", choiceui.KeyNone},
		{localModels[0].label(), squareOf("hermes"), "Qwen 3.8", choiceui.KeyNone},
		{localModels[1].label(), squareOf("hermes"), "Glimmer", choiceui.KeyNone},
	} {
		fr := topologyOf("hermes", "broker")(modelForm(c.option), 0)
		if fr.Key != c.key || fr.Square != c.square || fr.HostModel != c.hostModel {
			t.Errorf("%q: key=%v square=%q model=%q, want key=%v square=%q model=%q",
				c.option, fr.Key, fr.Square, fr.HostModel, c.key, c.square, c.hostModel)
		}
		if c.hostModel != "" {
			if fr.HostRight != "Ollama" || strings.Contains(fr.Host, "+") {
				t.Errorf("%q: host = %q right = %q, want the account beside Ollama", c.option, fr.Host, fr.HostRight)
			}
			if fr.HostOS != hostOSArch() || strings.Contains(fr.HostOS, "(") || !strings.HasSuffix(fr.HostOS, "/"+archShort()) {
				t.Errorf("%q: os/arch = %q, want %q", c.option, fr.HostOS, hostOSArch())
			}
		}
		if c.hostModel == "" && fr.HostOS != hostOSArch() {
			t.Errorf("%q: os/arch = %q, want %q", c.option, fr.HostOS, hostOSArch())
		}
		if c.hostModel == "" && (fr.HostRight != "" || strings.Contains(fr.Host, "Ollama")) {
			t.Errorf("%q: host = %q right = %q names Ollama without a local model", c.option, fr.Host, fr.HostRight)
		}
		if c.key == choiceui.KeyNone && !strings.Contains(fr.Caption, "no API key") {
			t.Errorf("%q: caption %q must say no API key is in play", c.option, fr.Caption)
		}
	}
}

func TestHoveringALocalModelLightsTheHostCells(t *testing.T) {
	t.Parallel()
	f := modelForm(localModels[0].label())
	if !topologyOf("hermes", "broker")(f, len(f.Rows)-1).HostLit {
		t.Error("cursor on Qwen left Ollama and the model unlit")
	}
	if topologyOf("hermes", "broker")(f, 0).HostLit {
		t.Error("cursor off the model row still lit Ollama and the model")
	}
	keys := modelForm(modelAPIKeys)
	if topologyOf("hermes", "broker")(keys, len(keys.Rows)-1).HostLit {
		t.Error("API keys lit Ollama and the model")
	}
}

func TestTheSquareNamesTheMicroVM(t *testing.T) {
	t.Parallel()
	if !strings.HasPrefix(vmPlatform(), "linux/") {
		t.Fatalf("vm = %q, want linux and an arch", vmPlatform())
	}
	if got := squareOf("opencode"); got != "sbx ("+vmPlatform()+") · opencode" {
		t.Errorf("square = %q", got)
	}
}

func TestSquareTrimsOnlyTheModelToSquareMax(t *testing.T) {
	t.Parallel()
	if got := squareWith("sbx · claudecode", "qwen3.8"); got != "sbx · claudecode + qwen3.8" {
		t.Errorf("the widest listed pair must fit whole, got %q", got)
	}
	got := squareWith("sbx · claudecode", "hf.co/unsloth/Qwen3-GGUF:Q4")
	if n := len([]rune(got)); n != choiceui.SquareMax || !strings.HasPrefix(got, "sbx · claudecode + ") || !strings.HasSuffix(got, "…") {
		t.Errorf("a long tag is trimmed to exactly SquareMax with an ellipsis, harness intact: %q (%d)", got, n)
	}
	if got := squareWith("sbx · claudecode", ""); got != "sbx · claudecode" {
		t.Errorf("API keys adds nothing, got %q", got)
	}
}

func TestTheMCPRowNotesTheHop(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		answer, want string
	}{{mcpOn, "+ mcp"}, {mcpOff, ""}} {
		r := mcpRow("claudecode", c.answer, false)
		f := &choiceui.Form{Rows: []choiceui.Row{r}}
		if got := topologyOf("claudecode", "broker")(f, -1).HopNote; got != c.want {
			t.Errorf("mcp %s: hop note %q, want %q", c.answer, got, c.want)
		}
	}
}
