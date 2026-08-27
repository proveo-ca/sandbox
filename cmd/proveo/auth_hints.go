// SPEC: _spec/_paradigms/credential-boundary.puml
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

// subscriptionAuthHint is the how-to generate / obtain a key for a declared env var.
type subscriptionAuthHint struct {
	HowTo string // one-line obtain instructions
	Login string // optional in-sandbox login command
}

// subscriptionAuthHints maps harness name → env var → obtain/login guidance.
var subscriptionAuthHints = map[string]map[string]subscriptionAuthHint{
	"claudecode": {
		"CLAUDE_CODE_OAUTH_TOKEN": {
			HowTo: "generate a Claude Code OAuth token with `claude setup-token`",
			Login: "claude setup-token",
		},
	},
	"cursor": {
		"CURSOR_API_KEY": {
			HowTo: "create a Cursor API key at cursor.com/dashboard → API Keys",
			Login: "agent login",
		},
	},
}

// credentialReachedAgent reports whether this run sent the agent anything it could
// authenticate with.
//
// It reads the run's OWN decision — the rendered env plus what was written to the
// store — rather than re-deriving it. Re-deriving is how the two answers drift, and
// a hint that contradicts the argv is worse than no hint.
func credentialReachedAgent(man manifest.Manifest, target, homeRoot string, env []string, secrets [][2]string, lookup func(string) string) bool {
	if hasPersistedLogin(target, homeRoot) {
		return true
	}
	for _, kv := range secrets {
		if strings.TrimSpace(kv[1]) != "" {
			return true // host-side injection put a real value in the store
		}
	}
	for _, e := range env {
		name, value, valued := strings.Cut(e, "=")
		if !isAuthVarOf(man, name) {
			continue
		}
		// A bare name is forward-by-name: the value is copied from proveo's own
		// environment at launch, so the lookup is what decides whether it carries
		// anything.
		if !valued {
			if lookup != nil && strings.TrimSpace(lookup(name)) != "" {
				return true
			}
			continue
		}
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// isAuthVarOf reports whether name is a credential the harness declares it uses.
func isAuthVarOf(man manifest.Manifest, name string) bool {
	for _, e := range man.Env {
		if e.Secret && e.Name == name {
			return true
		}
	}
	return false
}

// noCredentialHint explains a failed run that had nothing to authenticate with.
//
// It exists for the failure that leaves NO evidence behind. The handler can point at
// a captured tail or a session transcript wherever either exists; when the agent
// dies before its first turn there is neither — an interactive run hands the
// terminal to the child so no tail is taken, and a session that never opened writes
// no transcript. All that reaches the operator is a stopped sandbox, which reads as
// an infrastructure fault and sends them into `sbx exec` after a cause that was
// never in there.
//
// The credential is both the likeliest reason and the one thing knowable from here.
// Silent when a credential did reach the agent: a hint that fires on every failure
// teaches the operator to skip the one time it was right.
func noCredentialHint(man manifest.Manifest, target, homeRoot string, env []string, secrets [][2]string, lookup func(string) string) []string {
	if credentialReachedAgent(man, target, homeRoot, env, secrets, lookup) {
		return nil
	}
	byHarness := subscriptionAuthHints[harnessFamily(man.Name)]
	lines := []string{
		"no credential reached the agent — the likeliest reason it exited before saying anything",
	}
	for _, e := range man.Env {
		if !e.Secret {
			continue
		}
		howTo := byHarness[e.Name].HowTo
		if howTo == "" {
			howTo = e.Description
		}
		if howTo != "" {
			lines = append(lines, fmt.Sprintf("  %s — %s", e.Name, howTo))
		} else {
			lines = append(lines, "  "+e.Name)
		}
		lines = append(lines, fmt.Sprintf("      sbx secret set %s", e.Name))
	}
	// The trap that cost a session: `sbx secret ls` lists the NAME, so a store entry
	// holding an empty value is indistinguishable from a populated one at a glance —
	// and proveo cannot read the value back to tell the operator which they have.
	lines = append(lines,
		"  a store entry can already exist while holding an EMPTY value: `sbx secret ls` shows",
		"  the name either way, and `sbx secret set` overwrites it")
	return lines
}

func printSubscriptionAuthHints(man manifest.Manifest, missing []manifest.EnvVar, out io.Writer) {
	if len(missing) == 0 {
		return
	}
	p := ui.New(out)
	p.Notef("")
	p.Iconf("🔑", "No auth was set for %s — persist a key for the next run:", man.Name)

	sh, ok := shell.Detect(os.Getenv("SHELL"))
	if !ok {
		sh = shell.Shell{Name: "bash", Supported: true}
	}
	home, _ := os.UserHomeDir()
	rc := sh.RCFile(runtime.GOOS, home)

	byHarness := subscriptionAuthHints[harnessFamily(man.Name)]
	for _, e := range missing {
		hint := byHarness[e.Name]
		if hint.HowTo == "" && e.Description != "" {
			hint.HowTo = e.Description
		}
		if hint.HowTo != "" {
			fmt.Fprintf(out, "\n  %s — %s\n", e.Name, hint.HowTo)
		} else {
			fmt.Fprintf(out, "\n  %s\n", e.Name)
		}
		if hint.Login != "" {
			fmt.Fprintf(out, "  In-sandbox login: %s (agent handles auth; login tokens may be scrubbed from proveo home)\n", hint.Login)
		}
		placeholder := "…"
		fmt.Fprintf(out, "  Prefer one of:\n")
		fmt.Fprintf(out, "    • local project .env (gitignored, mode 0600):\n")
		fmt.Fprintf(out, "        printf '%%s\\n' '%s=%s' >> .env && chmod 600 .env\n", e.Name, placeholder)
		fmt.Fprintf(out, "    • SAFE host location (shell rc / secret manager / OS keychain — never commit keys):\n")
		fmt.Fprintf(out, "        %s\n", sh.ExportLine(e.Name, placeholder))
		if rc != "" {
			fmt.Fprintf(out, "        # then append that line to %s\n", rc)
		}
	}
	fmt.Fprintln(out)
}

// harnessFamily returns the base harness name for resume/auth hint lookups
// (e.g. claudecode-solidity → claudecode).
func harnessFamily(name string) string {
	name = strings.TrimSpace(name)
	for _, base := range []string{"claudecode", "cursor", "opencode", "cecli"} {
		if name == base || strings.HasPrefix(name, base+"-") {
			return base
		}
	}
	return name
}
