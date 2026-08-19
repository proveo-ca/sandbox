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
