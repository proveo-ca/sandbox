// SPEC: _spec/internal/workspace/mount-symlink-escape.puml
package run

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestLinksIntoOneDirectoryTakeOneScreenLineAndKeepOneLogLineEach(t *testing.T) {
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
	var screen, logged []string
	for _, ln := range linkLines(links) {
		switch ln.to {
		case lineScreen, lineWarn:
			screen = append(screen, ln.text)
		default:
			logged = append(logged, ln.text)
		}
	}
	want := []string{
		".idea/ → /repo/main/.idea/ (4 symlinks leave the workspace; targets mounted)",
		"_spec → /sync/_spec (symlink leaves the workspace; target mounted)",
		".vscode/settings.json → /repo/main/.vscode/settings.json (symlink leaves the workspace; target mounted)",
		".idea/renamed.xml → /repo/main/.idea/other.xml (symlink leaves the workspace; target mounted)",
		"secrets → /etc is not available inside the sandbox: outside every allowed root",
	}
	if !slices.Equal(screen, want) {
		t.Errorf("screen lines:\n%s\nwant:\n%s", strings.Join(screen, "\n"), strings.Join(want, "\n"))
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
