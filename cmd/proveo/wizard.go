// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/_paradigms/credential-boundary.puml
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

// secretReader reads one secret line with echo off. Injectable so promptEnv is
// unit-testable without a PTY.
type secretReader func() (string, error)

// termSecret reads a secret from the stdin terminal without echoing it.
func termSecret() (string, error) {
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr) // ReadPassword swallows the user's newline
	return string(b), err
}

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

func promptEnv(target string, missing []manifest.EnvVar, in io.Reader, out io.Writer, readSecret secretReader) map[string]string {
	p := ui.New(out)
	n := "s"
	if len(missing) == 1 {
		n = ""
	}
	p.Hostf("%s reads %d env var%s not set in your environment (Enter to skip):", target, len(missing), n)
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
