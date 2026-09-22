// SPEC: _spec/_devops/release-gate.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/proveo-ca/proveo/internal/ui"
)

// verdict is one row of a release-gate table: PASS, FAIL, SKIP or MISS.
type verdict struct {
	Kind string
	Text string
}

func (v verdict) String() string { return v.Kind + " " + v.Text }

// step is one planned command: extra env plus argv.
type step struct {
	Env  []string
	Argv []string
}

func (s step) String() string {
	return strings.Join(append(append([]string{}, s.Env...), s.Argv...), " ")
}

// teeRun streams a command's stdout+stderr to the terminal while keeping a copy.
func teeRun(dir string, s step) (string, int) {
	var buf bytes.Buffer
	w := io.MultiWriter(os.Stdout, &buf)
	c := exec.Command(s.Argv[0], s.Argv[1:]...)
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout, c.Stderr = w, w
	c.Env = append(os.Environ(), s.Env...)
	err := c.Run()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return buf.String(), ee.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(w, "%v\n", err)
		return buf.String(), 127
	}
	return buf.String(), 0
}

// printVerdicts renders the table and, when any row failed, the not-a-pass note.
func printVerdicts(title string, vs []verdict, failed bool, note []string) {
	ui.Section(ui.SectionResults)
	ui.Notef("%s", title)
	for _, v := range vs {
		switch v.Kind {
		case "PASS":
			ui.Okf("%s", v)
		case "FAIL":
			ui.Failf("%s", v)
		default:
			ui.Warnf("%s", v)
		}
	}
	if failed {
		for _, l := range note {
			ui.Notef("%s", l)
		}
	}
}

func printPlan(w io.Writer, steps []step) {
	for _, s := range steps {
		fmt.Fprintln(w, s)
	}
}

// envOr returns the environment value of key, or def when unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
