// SPEC: _spec/internal/choiceui/wizard-keys.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// pageOneOn is page 1 on host with sbx 0.50.0 installed.
func pageOneOn(t *testing.T, host sbx.Host) *choiceui.Form {
	t.Helper()
	plan, err := sbx.PlanFor(host, sbx.DefaultPrefix(homeDir()), "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	in := sbxPageIn{host: host, plan: plan, installed: "0.50.0", prereqs: framesPrereqs, auth: sbx.AuthOut,
		network: framesNetwork, diag: framesDiag, diagnosed: true}
	return sbxForm(in, initStepAt(1), defaultDecisions(plan, "0.50.0", initOptions{}, true), initOptions{}, nil,
		func(initDecisions) int { return 1 })
}

// On every init page, ←/→ land on every disabled option of every radio row, each shows a status
// line, the answer is never a disabled option, and a full lap leaves the answer where it was.
func TestEveryDisabledInitOptionTakesTheCursorAndStatesItsStatus(t *testing.T) {
	t.Setenv("HOME", "/home/op")
	forms := map[string]*choiceui.Form{}
	for _, c := range initFrameCases(t) {
		forms[c.name] = c.form()
	}
	for _, host := range []sbx.Host{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}} {
		forms["p1 on "+host.OS+"/"+host.Arch] = pageOneOn(t, host)
	}
	sawOff := false
	for name, f := range forms {
		for i := range f.Rows {
			r := f.Rows[i]
			if r.Field || len(r.Options) == 0 || f.Walk(r.Label, "right", "left") == nil {
				continue
			}
			before := f.Selection(r.Label)
			for _, key := range []string{"right", "left"} {
				seen := map[string]bool{}
				for range r.Options {
					hv := f.Walk(r.Label, key)[0]
					seen[hv.Option] = true
					j := slices.Index(r.Options, hv.Option)
					if j < 0 || j >= len(r.Off) || !r.Off[j] {
						continue
					}
					sawOff = true
					if strings.TrimSpace(strings.Join(hv.Status, "")) == "" {
						t.Errorf("%s: %s · %q is disabled and shows no status line; help %q", name, r.Label, hv.Option, hv.Help)
					}
					if got := f.Selection(r.Label); got == hv.Option {
						t.Errorf("%s: %s answers %q, a disabled option", name, r.Label, got)
					}
				}
				for j, opt := range r.Options {
					if j < len(r.Off) && r.Off[j] && !seen[opt] {
						t.Errorf("%s: %s · %q is disabled and %s never lands on it", name, r.Label, opt, key)
					}
				}
				if got := f.Selection(r.Label); got != before {
					t.Errorf("%s: %s answers %q after a lap of %s, want %q as before", name, r.Label, got, key, before)
				}
			}
		}
	}
	if !sawOff {
		t.Error("no init page drew a disabled option; the install row's distro package is gone")
	}
}

// Hovering distro package on darwin names it and says why it is off once, with the command
// --print names; sbx install keeps its answer.
func TestHoveringDistroPackageSaysWhyItIsOff(t *testing.T) {
	t.Setenv("HOME", "/home/op")
	f := framesPageOne(t, "0.50.0", sbx.AuthOut, installTarball)
	hv := f.Walk(rowInstall, "right")[0]
	if hv.Option != installPackaged || f.Selection(rowInstall) != installTarball {
		t.Fatalf("→ from the tarball: cursor on %q, answer %q; want distro package hovered, the tarball kept", hv.Option, f.Selection(rowInstall))
	}
	status := strings.Join(hv.Status, " ")
	if !strings.HasPrefix(status, "off: not offered — proveo cannot pin a package-manager install") ||
		!strings.Contains(status, "--print") || strings.Count(status, "not offered") != 1 {
		t.Errorf("status = %q, want one off: line naming --print", hv.Status)
	}
	if len(hv.Help) == 0 || !strings.HasPrefix(hv.Help[0], "› distro package — ") || strings.Contains(strings.Join(hv.Help, " "), "not offered") {
		t.Errorf("help = %q, want the option named and described, its status apart", hv.Help)
	}
	for _, size := range [][2]int{{80, 24}, {120, 36}} {
		frame := f.RenderOn(rowInstall, size[0], size[1])
		if !strings.Contains(frame, "  › distro package — ") || !strings.Contains(frame, "  off: not offered — proveo cannot pin") {
			t.Errorf("%dx%d: the two help lines must name distro package and its status:\n%s", size[0], size[1], frame)
		}
	}
}
