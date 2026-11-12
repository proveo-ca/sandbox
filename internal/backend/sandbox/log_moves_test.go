//go:build !windows

// SPEC: _spec/_plans/output-readability.puml
package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// What the run log holds instead of the screen, pinned screen against log.

func TestAReattachSaysHowToBuildFreshOnlyInTheRunLog(t *testing.T) {
	screen, log := captured(t)
	reattachLog("sb")
	if screen.Len() != 0 {
		t.Errorf("the re-attach's log lines reached the screen: %q", screen.String())
	}
	for _, want := range []string{"re-attaching to sb, which an earlier run left here", "`sbx rm --force sb` builds it fresh next run"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the run log lacks %q:\n%s", want, log.String())
		}
	}
}

// policySbx puts an sbx on PATH whose `policy check` answers body and exits code.
func policySbx(t *testing.T, body string, code string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1 $2\" in\n  \"policy check\") printf '%s\\n' '" + body + "'; exit " + code + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestTheBaselinesAllowAndCheckCommandsStayInTheRunLog(t *testing.T) {
	policySbx(t, `{"allowed":false}`, "0")
	screen, log := captured(t)
	facts, items := Baseline([]string{"api.anthropic.com"})
	PrintBlock(func() {}, facts, items)
	if got := screen.String(); got != "network: sbx's global policy allows listed hosts only\n" {
		t.Errorf("the screen = %q, want the fact alone", got)
	}
	if !strings.Contains(log.String(), "`sbx policy allow network api.anthropic.com` lets the agent reach its provider") {
		t.Errorf("the run log lacks the allow command:\n%s", log.String())
	}

	policySbx(t, "", "1")
	screen, log = captured(t)
	facts, items = Baseline([]string{"api.anthropic.com"})
	PrintBlock(func() {}, facts, items)
	if got := screen.String(); got != "warn: sbx network baseline — unreadable\n" {
		t.Errorf("the screen = %q, want the ⚠ alone", got)
	}
	for _, want := range []string{"`sbx policy check network " + unallowlistedProbe + "` reads it", "`sbx policy allow network api.anthropic.com`"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the run log lacks %q:\n%s", want, log.String())
		}
	}
}

func TestAPlanTokensSignInCommandStaysInTheRunLog(t *testing.T) {
	keys := map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth-test"}
	_, w, _ := Spec(Input{Target: "claudecode", Lookup: func(k string) string { return keys[k] }})
	screen, log := captured(t)
	PrintNotes(w.Report, Absolute)
	if got := screen.String(); got != "  sandbox secret: CLAUDE_CODE_OAUTH_TOKEN — not stored\n" {
		t.Errorf("the screen = %q, want the note alone", got)
	}
	if !strings.Contains(log.String(), "sign in with ") {
		t.Errorf("the run log lacks the sign-in command:\n%s", log.String())
	}
}
