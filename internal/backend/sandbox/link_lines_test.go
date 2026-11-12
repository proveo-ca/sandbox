// SPEC: _spec/internal/workspace/mount-symlink-escape.puml
package sandbox

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestLinksIntoOneDirectoryTakeOneValueAndKeepOneLogLineEach(t *testing.T) {
	t.Parallel()
	mounted := func(rel, target string) workspace.Link {
		return workspace.Link{Rel: rel, Target: target, Action: workspace.LinkMounted}
	}
	var links []workspace.Link
	for _, name := range []string{".gitignore", "Pluvo.iml", "codeStyles", "vcs.xml"} {
		links = append(links, mounted(".idea/"+name, "/repo/main/.idea/"+name))
	}
	links = append(links,
		mounted("_spec", "/sync/_spec"),
		mounted(".vscode/settings.json", "/repo/main/.vscode/settings.json"),
		mounted(".idea/renamed.xml", "/repo/main/.idea/other.xml"),
		workspace.Link{Rel: "secrets", Target: "/etc", Action: workspace.LinkRefused, Reason: "outside every allowed root"},
	)
	tilde := func(p string) string { return strings.Replace(p, "/sync", "~/sync", 1) }
	var screen, warned, logged []string
	for _, ln := range linkLines(links, nil, nil, tilde) {
		switch ln.to {
		case lineScreen:
			screen = append(screen, ln.text)
		case lineWarn:
			warned = append(warned, ln.text+" | "+strings.Join(ln.detail, " "))
		default:
			logged = append(logged, ln.text)
		}
	}
	want := []string{
		".idea/ → /repo/main/.idea/ (4 links)",
		"_spec → ~/sync/_spec",
		".vscode/settings.json → /repo/main/.vscode/settings.json",
		".idea/renamed.xml → /repo/main/.idea/other.xml",
	}
	if !slices.Equal(screen, want) {
		t.Errorf("links values:\n%s\nwant:\n%s", strings.Join(screen, "\n"), strings.Join(want, "\n"))
	}
	if !slices.Equal(warned, []string{"secrets → /etc — not in the sandbox | outside every allowed root"}) {
		t.Errorf("a refused link must be one ⚠ item with its reason as detail, got %q", warned)
	}
	if len(logged) != 4 {
		t.Fatalf("the run log must keep one line per grouped link, got %d:\n%s", len(logged), strings.Join(logged, "\n"))
	}
	for i, name := range []string{".gitignore", "Pluvo.iml", "codeStyles", "vcs.xml"} {
		if w := fmt.Sprintf(".idea/%s → /repo/main/.idea/%s (symlink leaves the workspace; target mounted)", name, name); logged[i] != w {
			t.Errorf("log line %d = %q, want %q", i, logged[i], w)
		}
	}
}

func TestALinkWhoseTargetAWorktreeCloneDroppedIsNoValueAndSaysSoInTheLog(t *testing.T) {
	t.Parallel()
	links := []workspace.Link{
		{Rel: ".idea/vcs.xml", Target: "/repo/main/.idea/vcs.xml", Action: workspace.LinkMounted},
		{Rel: ".idea/misc.xml", Target: "/repo/main/.idea/misc.xml", Action: workspace.LinkMounted},
		{Rel: "_spec", Target: "/sync/_spec", Action: workspace.LinkMounted},
	}
	var screen, logged []string
	for _, ln := range linkLines(links, []string{"/repo/main/.idea/vcs.xml", "/repo/main/.idea/misc.xml"}, nil, Absolute) {
		if ln.to == lineScreen {
			screen = append(screen, ln.text)
		} else {
			logged = append(logged, ln.text)
		}
	}
	if !slices.Equal(screen, []string{"_spec → /sync/_spec"}) {
		t.Errorf("links values = %q, want only the mounted _spec", screen)
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "target not mounted in a clone") || strings.Contains(strings.Join(logged, "\n"), "target mounted)") {
		t.Errorf("the log must say a dropped target is not mounted:\n%s", strings.Join(logged, "\n"))
	}
}

func TestLinksToFilesSbxDoesNotBindAreAWarningNotAValue(t *testing.T) {
	t.Parallel()
	links := []workspace.Link{
		{Rel: ".idea/vcs.xml", Target: "/repo/main/.idea/vcs.xml", Action: workspace.LinkMounted},
		{Rel: ".idea/misc.xml", Target: "/repo/main/.idea/misc.xml", Action: workspace.LinkMounted},
		{Rel: ".idea/codeStyles", Target: "/repo/main/.idea/codeStyles", Action: workspace.LinkMounted},
		{Rel: "Pluvo.iml", Target: "/repo/main/Pluvo.iml", Action: workspace.LinkMounted},
	}
	unbound := unboundLinks(links, []sbx.Mount{{Host: "/repo/hotfix"}, {Host: "/repo/main/.idea/codeStyles"}})
	if want := []string{"/repo/main/.idea/vcs.xml", "/repo/main/.idea/misc.xml", "/repo/main/Pluvo.iml"}; !slices.Equal(unbound, want) {
		t.Fatalf("unboundLinks = %q, want every target no bind holds %q", unbound, want)
	}
	var screen, warned, logged []string
	for _, ln := range linkLines(links, nil, unbound, Absolute) {
		switch ln.to {
		case lineScreen:
			screen = append(screen, ln.text)
		case lineWarn:
			warned = append(warned, ln.text+" | "+strings.Join(ln.detail, " "))
		default:
			logged = append(logged, ln.text)
		}
	}
	if !slices.Equal(screen, []string{".idea/codeStyles → /repo/main/.idea/codeStyles"}) {
		t.Errorf("links values = %q, want only the bound directory", screen)
	}
	want := []string{
		".idea/ → /repo/main/.idea/ (2 links) — not in the sandbox | ",
		"Pluvo.iml → /repo/main/Pluvo.iml — not in the sandbox | ",
	}
	if !slices.Equal(warned, want) {
		t.Errorf("⚠ items:\n%s\nwant:\n%s", strings.Join(warned, "\n"), strings.Join(want, "\n"))
	}
	if log := strings.Join(logged, "\n"); strings.Count(log, "target not mounted: sbx binds directories only") != 3 || strings.Contains(log, "target mounted)") {
		t.Errorf("the run log must keep one not-mounted row per file link:\n%s", log)
	}
}
