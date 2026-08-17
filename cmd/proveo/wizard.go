// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/_paradigms/credential-boundary.puml
// The missing-env wizard follows the DinD sidecar prompt pattern
// (apps/cli/public/cli/lib/runners.sh): detect the need, honor an env
// short-circuit, prompt only on a TTY, and default to the safe path (Enter
// skips, non-interactive runs just warn). Collected values are set in the
// process env only — provider detection, the broker secret file, and the bare
// `-e NAME` forwarding all read them from there, so a secret never lands on
// an argv.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

// wizardEnabled reports whether the wizard may prompt at all
// (PROVEO_WIZARD=off|0|no|false disables it, mirroring PROVEO_CREDENTIAL_BROKER).
func wizardEnabled() bool {
	switch strings.ToLower(os.Getenv("PROVEO_WIZARD")) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

// secretReader reads one secret line with echo off. Injectable so promptEnv is
// unit-testable without a PTY.
type secretReader func() (string, error)

// termSecret reads a secret from the stdin terminal without echoing it.
func termSecret() (string, error) {
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr) // ReadPassword swallows the user's newline
	return string(b), err
}

// promptYesNo asks a one-line question and reads one line: y/yes => true,
// empty => def, anything else => false (the DinD-prompt convention: only an
// explicit yes is a yes).
func promptYesNo(question string, def bool, in io.Reader, out io.Writer) bool {
	suffix := "[Y/n]"
	if !def {
		suffix = "[y/N]"
	}
	fmt.Fprintf(out, "%s %s ", question, suffix)
	s, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	case "":
		return def
	}
	return false
}

// promptEnv asks for each missing declared env var and returns name->value for
// the ones the user filled in. Enter (or a read error) skips a var — skipped
// vars stay missing and the caller warns about them, exactly like today's
// non-interactive behavior.
func promptEnv(target string, missing []manifest.EnvVar, in io.Reader, out io.Writer, readSecret secretReader) map[string]string {
	p := ui.New(out)
	n := "s"
	if len(missing) == 1 {
		n = ""
	}
	p.Iconf("🔑", "%s reads %d env var%s not set in your environment (Enter to skip):", target, len(missing), n)
	var r *bufio.Reader
	got := map[string]string{}
	for _, e := range missing {
		if e.Description != "" {
			fmt.Fprintf(out, "   %s — %s\n", e.Name, e.Description)
		}
		var v string
		var err error
		if e.Secret && readSecret != nil {
			fmt.Fprintf(out, "   %s (hidden): ", e.Name)
			v, err = readSecret()
		} else {
			if r == nil {
				r = bufio.NewReader(in)
			}
			fmt.Fprintf(out, "   %s: ", e.Name)
			v, err = r.ReadString('\n')
		}
		v = strings.TrimSpace(v)
		if err != nil && v == "" {
			continue
		}
		if v != "" {
			got[e.Name] = v
		}
	}
	return got
}
