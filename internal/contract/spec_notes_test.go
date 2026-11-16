// SPEC: _spec/_conventions/spec-conventions.puml
package contract_test

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var specNotesEnforced = []string{
	"_spec/defs/opencode/engine-local-history.puml",
	"_spec/_paradigms/opencode-oauth-persistence.puml",
	"_spec/packages/lib/opencode-seed-progress.puml",
	"_spec/_devops/release-gate.puml",
	"_spec/_devops/git-hooks.puml",
	"_spec/_experiments/operator-fingerprint.puml",
	"_spec/_experiments/agent-council-ledger.puml",
	"_spec/_experiments/cdp-shared-browser.puml",
	"_spec/_experiments/mobile-device-reach.puml",
	"_spec/internal/sbx/host-inference.puml",
	"_spec/internal/sbx/host-browser-cdp.puml",
	"_spec/internal/sbx/host-android-adb.puml",
	"_spec/cmd/proveo/operator-name.puml",
	"_spec/defs/hermes/hermes-persistence.puml",
	"_spec/internal/schedule/schedule.puml",
	"_spec/_devops/sandbox-template-rebase.puml",
	"_spec/internal/sbx/host-readiness.puml",
	"_spec/internal/sbx/launch-env.puml",
	"_spec/internal/sbx/shell-agent-launch.puml",
	"_spec/internal/backend/exit-code.puml",
	"_spec/internal/ptyproxy/pty-ownership.puml",
	"_spec/defs/codex/codex-topology.puml",
	"_spec/internal/ptyproxy/opencode-keyboard-stall.puml",
	"_spec/internal/contract/what-a-contract-test-is.puml",
	"_spec/internal/credentials/credential-decisions.puml",
	"_spec/internal/gitaccess/authorship.puml",
	"_spec/internal/posture/one-value-two-renderings.puml",
	"_spec/internal/run/run-spec.puml",
	"_spec/internal/sbx/credential-resolution.puml",
	"_spec/internal/sbx/policy-baseline.puml",
	"_spec/internal/schedule/driving-a-tty.puml",
	"_spec/defs/agent-definition-sharing.puml",
	"_spec/internal/choiceui/choice-prompt-render.puml",
	"_spec/internal/entrypoint/model-alias-bridges.puml",
	"_spec/internal/sbx/sbx-kit-contract.puml",
	"_spec/internal/sbx/startup-dispatch.puml",
	"_spec/internal/choiceui/topology-strip.puml",
	"_spec/internal/choiceui/viewport.puml",
	"_spec/_plans/secret-custody.puml",
	"_spec/internal/credentials/secret-references.puml",
	"_spec/internal/workspace/workspace-scan.puml",
	"_spec/_paradigms/retire-dind.puml",
	"_spec/_paradigms/workspace-boundary.puml",
	"_spec/_plans/host-held-provider-keys.puml",
	"_spec/_plans/tools-and-localhost-tunnels.puml",
	"_spec/internal/sbx/ide-attach.puml",
	"_spec/_plans/sbx-dep-tree-isolation.puml",
	"_spec/packages/lib/config-seeding-and-persistence.puml",
	"_spec/packages/lib/github-ssh-hosts.puml",
	"_spec/packages/lib/git-sync-turn.puml",
	"_spec/_devops/image-size-reduction.puml",
	"_spec/internal/maintain/build-schedule.puml",
	"_spec/internal/sbx/mcp-gateway.puml",
	"_spec/internal/envflag/env-switches.puml",
	"_spec/internal/envreg/env-registry.puml",
	"_spec/packages/lib/seed-and-launch.puml",
	"_spec/defs/harden-pass.puml",
	"_spec/defs/base-image-layers.puml",
	"_spec/defs/harness-image-layers.puml",
	"_spec/_paradigms/capability-ladder.puml",
	"_spec/_devops/all-defs-green.puml",
	"_spec/defs/opencode/native-v2-integration.puml",
	"_spec/defs/opencode/opencode-paradigm.puml",
	"_spec/defs/opencode/opencode-topology.puml",
	"_spec/_devops/agent-version-pin.puml",
	"_spec/_devops/agent-update-prompt.puml",
	"_spec/_plans/tui-upgrades-delivery.puml",
	"_spec/_devops/unit-baseline.puml",
	"_spec/internal/backend/teardown.puml",
	"_spec/internal/choiceui/tui-stack.puml",
	"_spec/internal/choiceui/confirm-dialogs.puml",
	"_spec/internal/choiceui/scope-tree.puml",
	"_spec/internal/choiceui/wizard-keys.puml",
	"_spec/internal/ui/step-lines.puml",
	"_spec/cmd/proveo/init-preflight-and-result.puml",
	"_spec/internal/results/result-store.puml",
	"_spec/internal/promptkit/prompt-kit.puml",
	"_spec/cmd/proveo/init-sbx-bootstrap.puml",
	"_spec/internal/choiceui/session.puml",
	"_spec/internal/sbx/state-sync.puml",
	"_spec/tests/00-test-fixtures.puml",
}

const (
	specNoteMinLines = 2
	specNoteMaxLines = 5
	specNoteMaxCols  = 50
)

var (
	noteOpen   = regexp.MustCompile(`^note\s+(?:top|bottom|left|right)\s+of\s+\S+\s*$`)
	noteAs     = regexp.MustCompile(`^note\s+as\s+\S+\s*$`)
	noteOver   = regexp.MustCompile(`^note\s+over\s+[^:]+$`)
	noteInline = regexp.MustCompile(`^\s*note\s+(?:(?:top|bottom|left|right)\s+of|over)\s+[^:]*:`)
)

type specNote struct {
	anchor string
	line   int
	body   []string
}

func parseSpecNotes(src string) []specNote {
	var out []specNote
	var cur *specNote
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case noteOpen.MatchString(trimmed) || noteAs.MatchString(trimmed) || noteOver.MatchString(trimmed):
			out = append(out, specNote{anchor: trimmed, line: i + 1})
			cur = &out[len(out)-1]
		case trimmed == "end note":
			cur = nil
		case cur != nil:
			cur.body = append(cur.body, line)
		case noteInline.MatchString(line):
			// `note left of X : text` is a one-liner by construction; the floor forbids it.
			out = append(out, specNote{anchor: trimmed, line: i + 1, body: []string{line}})
		}
	}
	return out
}

func unwrappable(line string) bool {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "- ")
	t = strings.Trim(t, "[]")
	return t != "" && !strings.ContainsAny(t, " \t")
}

func TestSpecNotesFitTheBox(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range specNotesEnforced {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		notes := parseSpecNotes(string(b))
		if len(notes) == 0 {
			t.Errorf("%s: enforced but has no notes — drop it from the list", rel)
		}
		for _, n := range notes {
			switch {
			case len(n.body) < specNoteMinLines:
				t.Errorf("%s:%d %q has %d line(s); split short text across %d",
					rel, n.line, n.anchor, len(n.body), specNoteMinLines)
			case len(n.body) > specNoteMaxLines:
				t.Errorf("%s:%d %q has %d lines; max %d — split onto another anchor",
					rel, n.line, n.anchor, len(n.body), specNoteMaxLines)
			}
			for k, line := range n.body {
				// Columns are a display property, so count runes: an em dash is one
				// column and three bytes, and byte length would reject a legal note.
				if w := utf8.RuneCountInString(line); w > specNoteMaxCols && !unwrappable(line) {
					t.Errorf("%s:%d %q line %d is %d cols; max %d",
						rel, n.line, n.anchor, k+1, w, specNoteMaxCols)
				}
			}
		}
	}
}

// The column cap is exempted only for text no rewrite can shorten. Widening that to
// "any long line" would retire the rule quietly, so the boundary is pinned here.
func TestUnwrappableExemptsLinksAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		line string
		want bool
		why  string
	}{
		{"  - [[https://example.com/a/very/long/path/to/a/spec.md]]", true,
			"a bare link in a list is one token; wrapping it breaks the link"},
		{"  https://example.com/a/very/long/path", true, "same without the list marker"},
		{"  **A sentence that simply runs on and on past fifty columns.**", false,
			"prose is exactly what the cap exists to compress"},
		{"  - see https://example.com and also the note below", false,
			"a link inside a sentence can still be rewritten around"},
		{"", false, "an empty line is not a token"},
	} {
		if got := unwrappable(c.line); got != c.want {
			t.Errorf("unwrappable(%q) = %v, want %v — %s", c.line, got, c.want, c.why)
		}
	}
}

var (
	ownerRow  = regexp.MustCompile(`^'\s+(\S+\.puml) \.+ (.+)$`)
	specPath  = regexp.MustCompile(`[\w./-]+\.puml`)
	findingID = regexp.MustCompile(`\b(?:SEC|[CDFNST])\d+[a-z]?\b`)
)

// The retired findings record maps each finding ID to the living spec that now carries its decision.
func TestRetiredFindingsMapNamesLivingOwners(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	const plan = "_spec/_plans/repo-simplification.puml"
	b, err := os.ReadFile(filepath.Join(root, plan))
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(b), "' The owner specs, paths under _spec/:\n")
	if !ok {
		t.Fatalf("%s: no owner map header", plan)
	}
	block, _, _ = strings.Cut(block, "'\n")
	rows := 0
	for line := range strings.SplitSeq(block, "\n") {
		for _, p := range specPath.FindAllString(line, -1) {
			if _, err := os.Stat(filepath.Join(root, "_spec", p)); err != nil {
				t.Errorf("%s owner map names _spec/%s, which does not exist", plan, p)
			}
		}
		m := ownerRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows++
		owner, err := os.ReadFile(filepath.Join(root, "_spec", m[1]))
		if err != nil {
			continue
		}
		for _, id := range findingID.FindAllString(m[2], -1) {
			if !regexp.MustCompile(`\b` + id + `\b`).Match(owner) {
				t.Errorf("%s maps %s to _spec/%s, which never cites %s", plan, id, m[1], id)
			}
		}
	}
	if rows == 0 {
		t.Fatalf("%s: owner map has no rows", plan)
	}
}

// renamedIdents maps each name gopls renamed to its replacements, scoped to the files whose prose named it.
var renamedIdents = []struct {
	files   []string
	renamed map[string][]string
}{
	{[]string{"_spec/internal/gitaccess/authorship.puml", "internal/gitaccess/identity.go", "internal/gitaccess/identity_test.go"},
		map[string][]string{"Resolve": {"ResolveIdentity"}}},
	{[]string{"_spec/internal/promptkit/setup-path.puml", "_spec/internal/promptkit/prompt-kit.puml",
		"internal/promptkit/shell.go", "internal/promptkit/shell_test.go"},
		map[string][]string{"Detect": {"DetectShell"}, "Marker": {"PathMarker"}}},
	{[]string{"_spec/internal/sbx/host-android-adb.puml", "_spec/internal/sbx/host-browser-cdp.puml",
		"internal/hostdev/hostadb.go", "internal/hostdev/hostadb_test.go", "internal/hostdev/hostcdp.go", "internal/hostdev/hostcdp_test.go"},
		map[string][]string{"Ensure": {"EnsureADB", "EnsureCDP"}, "Ready": {"ADBReady", "CDPReady"}, "Port": {"ADBPort", "CDPPort"},
			"Addon": {"AddonAndroid", "AddonChrome"}, "LaunchArgs": {"EmulatorArgs", "ChromeArgs"}}},
	{[]string{"_spec/internal/schedule/driving-a-tty.puml", "internal/schedule/tmux.go", "internal/schedule/tmux_test.go"},
		map[string][]string{"Runner": {"TmuxRunner"}, "Available": {"TmuxAvailable"}, "Session": {"TmuxSession"},
			"New": {"NewTmux"}, "SessionEnv": {"TmuxEnv"}}},
	{[]string{"_spec/cmd/proveo/operator-name.puml", "internal/proveohome/operator.go", "internal/proveohome/operator_test.go"},
		map[string][]string{"Sanitize": {"SanitizeOperator"}, "Save": {"SaveOperator"}, "Load": {"LoadOperator"}}},
	{[]string{"_spec/internal/credentials/secret-references.puml", "internal/credentials/secretref.go", "internal/credentials/secretref_test.go"},
		map[string][]string{"Parse": {"ParseSecretRef"}, "IsReference": {"IsSecretRef"}}},
	{[]string{"_spec/internal/workspace/workspace-scan.puml", "internal/workspace/scan.go", "internal/workspace/scan_test.go"},
		map[string][]string{"MaxDepth": {"ScanMaxDepth"}, "DefaultBudget": {"DefaultScanBudget"}, "Result": {"ScanResult"}}},
}

type proseLine struct {
	line int
	text string
}

// proseOf returns every line of a spec, and only the comments and string literals of a Go file.
func proseOf(rel, src string) []proseLine {
	var out []proseLine
	if !strings.HasSuffix(rel, ".go") {
		for i, l := range strings.Split(src, "\n") {
			out = append(out, proseLine{i + 1, l})
		}
		return out
	}
	fset := token.NewFileSet()
	var s scanner.Scanner
	s.Init(fset.AddFile(rel, -1, len(src)), []byte(src), nil, scanner.ScanComments)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return out
		}
		if tok == token.COMMENT || tok == token.STRING {
			out = append(out, proseLine{fset.Position(pos).Line, lit})
		}
	}
}

// namesStale reports a whole-word old that is not the "<old> became <new>" record of the rename.
func namesStale(old, text string) bool {
	for _, m := range regexp.MustCompile(`\b`+regexp.QuoteMeta(old)+`\b`).FindAllStringIndex(text, -1) {
		if !strings.HasPrefix(text[m[1]:], " became") {
			return true
		}
	}
	return false
}

func TestProseNamesTheRenamedIdentifier(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, r := range renamedIdents {
		var code strings.Builder
		for _, rel := range r.files {
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				continue
			}
			if strings.HasSuffix(rel, ".go") {
				code.Write(b)
			}
			for _, p := range proseOf(rel, string(b)) {
				for old, now := range r.renamed {
					if namesStale(old, p.text) {
						t.Errorf("%s:%d names %s; gopls renamed it to %s", rel, p.line, old, strings.Join(now, " / "))
					}
				}
			}
		}
		for _, now := range r.renamed {
			for _, n := range now {
				if !regexp.MustCompile(`\b` + n + `\b`).MatchString(code.String()) {
					t.Errorf("no Go file in %v names %s: update its renamedIdents row", r.files, n)
				}
			}
		}
	}
}

func TestNamesStaleSparesOnlyTheRenameRecord(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		old, text string
		want      bool
	}{
		{"Resolve", `' Resolve therefore runs host-side`, true},
		{"Detect", `CMD ARROW_MAIN SH : " ""Detect($SHELL)"" "`, true},
		{"MaxDepth", `"a marker below MaxDepth=%d must not be found"`, true},
		{"Resolve", `' internal/gitaccess, and this file moved with it. Resolve became`, false},
		{"Resolve", `' ResolveIdentity therefore runs host-side`, false},
		{"Resolve", `' Resolve became ResolveIdentity; Resolve stays in the diagram`, true},
	} {
		if got := namesStale(c.old, c.text); got != c.want {
			t.Errorf("namesStale(%s, %q) = %v, want %v", c.old, c.text, got, c.want)
		}
	}
}
