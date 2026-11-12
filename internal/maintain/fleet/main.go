// SPEC: _spec/internal/maintain/image-build-deploy.puml
// Command fleet builds, deploys or tests harness images; mise run build, deploy and test-defs call it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/ui"
)

const usage = "usage: fleet [-tag=<tag>] [-no-cache] [-print] build|deploy|test <target>|all"

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		ui.Failf("%v", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], os.Getenv, cwd, os.Stdout, os.Stderr))
}

// run parses args, then runs one fleet verb; it returns the process exit code.
func run(args []string, getenv func(string) string, cwd string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	tag := fs.String("tag", "", "image tag (build: local, deploy: latest)")
	noCache := fs.Bool("no-cache", false, "pass --no-cache to docker buildx build")
	printOnly := fs.Bool("print", false, "print the plan instead of running it")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) != 2 || (rest[0] != "build" && rest[0] != "deploy" && rest[0] != "test") {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	pr := ui.New(stderr)
	defs, err := maintain.DefsDir(getenv, cwd)
	if err != nil {
		pr.Failf("%v", err)
		return 1
	}
	if err := maintain.Fleet(rest[0], rest[1], *tag, *noCache, *printOnly, defs, stdout); err != nil {
		pr.Failf("%v", err)
		return 1
	}
	return 0
}
