// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (FOUR PAGES ON ONE CARD), _spec/internal/choiceui/wireframe.puml (THE CHECKLIST CARD)
package main

import (
	"fmt"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/githubaccess"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/testx"
)

// The recorded host the frames draw: darwin/arm64 with sbx 0.50.0, two passed checks and two that
// need the operator.
var (
	framesHost    = sbx.Host{OS: "darwin", Arch: "arm64"}
	framesPrereqs = []sbx.Prereq{{Name: "Apple silicon", OK: true, Detail: "macOS Sonoma 14 or later"}}
	framesDiag    = []sbx.Check{
		{Name: "virtualisation", Status: "pass", Message: "supported"},
		{Name: "Binary version", Status: "warn", Message: "update available: v9.9.9",
			Detail: "https://example.invalid/sbx/v9.9.9", Hint: "Download the latest release from the link above"},
		{Name: "SSH client config", Status: "fail", Message: "an unmanaged sandbox Host stanza can shadow the managed configuration",
			Detail: "/home/op/.ssh/config:5: Host", Hint: "Remove or rename the reported Host *.sbx stanza, then run sbx setup ssh"},
	}
	framesNetwork = networkState{fact: "listed hosts only"}
)

// framesPlan is darwin's release plan into the operator's home prefix.
func framesPlan(t *testing.T) sbx.Plan {
	t.Helper()
	plan, err := sbx.PlanFor(framesHost, sbx.DefaultPrefix(homeDir()), "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// framesPageOne is page 1 on the recorded host: sbx installed as given, Docker as auth says.
func framesPageOne(t *testing.T, installed string, auth sbx.Auth, pick string) *choiceui.Form {
	plan := framesPlan(t)
	in := sbxPageIn{host: framesHost, plan: plan, installed: installed, prereqs: framesPrereqs, auth: auth, network: framesNetwork}
	if installed != "" {
		in.diag, in.diagnosed = framesDiag, true
	}
	def := defaultDecisions(plan, installed, initOptions{}, true)
	if pick != "" {
		def.Install = pick
	}
	count := func(d initDecisions) int {
		n := 0
		if d.Install != installSkip {
			n += 2
			if d.AddPath {
				n++
			}
		}
		if d.Login && (installed == "" || auth == sbx.AuthOut) {
			n++
		}
		return n
	}
	return sbxForm(in, initStepAt(1), def, initOptions{}, nil, count)
}

// framesDone are the done rows the recorded walk leaves before page n.
func framesDone(n int) []choiceui.Row {
	done := []pageDone{
		{mark: choiceui.MarkNeeds, outcome: "kept 0.50.0 · Docker signed in · 2 checks need you", brief: "kept 0.50.0 · Docker signed in · 2 checks need you"},
		{mark: choiceui.MarkDone, outcome: "Ada Example <ada@example.invalid> · git's global config · GitHub later · prompt kit later",
			brief: "Ada Example <ada@example.invalid> · 2 later"},
		{mark: choiceui.MarkDone, outcome: "3 held · 4 not held"},
	}
	var rows []choiceui.Row
	for i := range n - 1 {
		rows = append(rows, done[i].row(pageNames[i]))
	}
	return rows
}

// framesPageTwo is page 2 with git identity's fields open, or with what the host already holds.
func framesPageTwo(t *testing.T, rerun bool) *choiceui.Form {
	def := defaultDecisions(framesPlan(t), "0.50.0", initOptions{}, true)
	def.Gh, def.Prompt = false, false
	in := gitPageIn{id: identity("Ada Example", ""), gitOK: true}
	var seen map[string]rowState
	if rerun {
		in.id = identity("Ada Example", "ada@example.invalid")
		seen = map[string]rowState{rowGh: {mark: choiceui.MarkDone, outcome: "signed in as ada · sandbox Git: SSH agent"}}
	}
	return gitForm(in, initStepAt(2), def, initOptions{}, framesDone(2), seen, func(d initDecisions) int {
		n := 0
		for _, now := range []bool{d.Git && !rerun, d.Gh && !rerun, d.Prompt} {
			if now {
				n++
			}
		}
		return n
	})
}

// framesPageThree is page 3 with one key only this host's env holds.
func framesPageThree() *choiceui.Form {
	slots := []credSlot{
		{Label: "claudecode", Kind: kindSignIn, Store: "anthropic", Def: "claudecode"},
		{Label: "codex", Kind: kindSignIn, Store: "openai", Def: "codex", Held: true},
		{Label: "cursor", Kind: kindSignIn, Store: "cursor", Def: "cursor"},
		{Label: "opencode", Kind: kindSubscription, Store: "opencode", Def: "opencode", Held: true},
		{Label: "anthropic", Kind: kindKey, Store: "anthropic", Def: "opencode", EnvVars: []string{"ANTHROPIC_API_KEY"}},
		{Label: "openai", Kind: kindKey, Store: "openai", Def: "opencode"},
		{Label: "cursor", Kind: kindKey, Store: "cursor", Def: "opencode"},
	}
	f := credentialForm(initStepAt(3), credentialRows(slots), framesDone(3), false)
	f.EnterWord = func(*choiceui.Form) string { return runWord(1) }
	return f
}

// initFrameCases are the pages the walk draws, each at 80x24 and 120x36.
func initFrameCases(t *testing.T) []struct {
	name string
	form func() *choiceui.Form
} {
	return []struct {
		name string
		form func() *choiceui.Form
	}{
		{"p1-rest", func() *choiceui.Form { return framesPageOne(t, "0.50.0", sbx.AuthOut, "") }},
		{"p1-fresh", func() *choiceui.Form { return framesPageOne(t, "", sbx.AuthUnknown, "") }},
		{"p1-tarball", func() *choiceui.Form { return framesPageOne(t, "0.50.0", sbx.AuthOut, installTarball) }},
		{"p1-running", func() *choiceui.Form {
			f := framesPageOne(t, "0.50.0", sbx.AuthOut, "")
			for i := range f.Rows {
				switch f.Rows[i].Label {
				case rowInstall:
					f.Rows[i].Kind, f.Rows[i].Mark, f.Rows[i].Outcome = choiceui.RowStatus, choiceui.MarkLater, "kept 0.50.0"
				case rowLogin:
					f.Rows[i].Kind, f.Rows[i].Mark, f.Rows[i].Outcome = choiceui.RowStatus, choiceui.MarkRunning, "sbx login · Docker not signed in"
				}
			}
			f.Progress = &choiceui.Progress{Plan: []string{rowLogin}, Help: loginStep(initDecisions{}, applyCtx{}, nil).help}
			return f
		}},
		{"p1-rerun", func() *choiceui.Form { return framesPageOne(t, "0.50.0", sbx.AuthIn, "") }},
		{"p2", func() *choiceui.Form { return framesPageTwo(t, false) }},
		{"p2-rerun", func() *choiceui.Form { return framesPageTwo(t, true) }},
		{"p3", framesPageThree},
		{"p4", func() *choiceui.Form {
			return operatorForm(initStepAt(4), "", framesDone(4), setupRow(sbx.SetupOffer{EnvOnly: 2, Known: true}, true, false))
		}},
		{"github-access", func() *choiceui.Form {
			h := githubaccess.Host{Login: "ada", Protocol: githubaccess.SSH, Keys: []string{"SHA256:Kx2f…Qm (ED25519)"}, Forwarding: true, ForwardingKnown: true}
			bare := githubAccessForm(h, "")
			f := subPage(initStepAt(2), bare.Title, bare.Header, bare.Rows, framesDone(2), aheadRows(3, setupAhead(false)))
			f.EnterWord = func(*choiceui.Form) string { return runWord(1) }
			return f
		}},
	}
}

// TestInitPagesMatchTheirGoldenFrames holds each init page, as the walk builds it, to frames
// recorded from the renderer at 80x24 and 120x36 in testdata/init.txtar; -update records them.
func TestInitPagesMatchTheirGoldenFrames(t *testing.T) {
	t.Setenv("HOME", "/home/op")
	t.Setenv("NO_COLOR", "")
	t.Setenv("PROVEO_GLYPHS", "")
	g := testx.Open(t, "init")
	for _, c := range initFrameCases(t) {
		for _, size := range [][2]int{{80, 24}, {120, 36}} {
			f := c.form()
			f.Glyphs = choiceui.GlyphsNerd
			g.Assert(fmt.Sprintf("%s_%dx%d.txt", c.name, size[0], size[1]), f.Render(size[0], size[1]))
		}
	}
}

// identity is a git identity git resolves on the recorded host.
func identity(name, email string) gitidentity.Identity {
	return gitidentity.Identity{Name: name, Email: email}
}
