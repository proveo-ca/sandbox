// SPEC: _spec/internal/maintain/image-build-deploy.puml
// Command fleet builds, deploys or tests harness images and, with -cli, the CLI; mise run build, deploy and test-images call it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/proveo-ca/proveo/internal/cdn/release"
	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/ui"
)

const usage = "usage: fleet [-tag=<tag>] [-no-cache] [-print] [-cli [-cli-version=<v>] [-yes]] build|deploy|test [<target>|all]"

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
	cli := fs.Bool("cli", false, "build: go install the CLI; deploy: prepare or publish a CLI release")
	cliVersion := fs.String("cli-version", "patch", "deploy -cli: patch, minor, major or vX.Y.Z")
	yes := fs.Bool("yes", false, "deploy -cli: answer the release confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 || len(rest) > 2 || (rest[0] != "build" && rest[0] != "deploy" && rest[0] != "test") ||
		(len(rest) == 1 && !*cli && rest[0] != "test") || (*cli && rest[0] == "test") {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	pr := ui.New(stderr)
	if *cli && rest[0] == "deploy" {
		if _, target := imagebuild.Specs[*cliVersion]; len(rest) == 1 && (target || *cliVersion == "all") {
			rest = append(rest, *cliVersion)
			*cliVersion = "patch"
		}
		if err := release.ValidVersion(*cliVersion); err != nil {
			pr.Failf("--cli took %q as its version (%v); name the target before --cli: mise run deploy <target> --cli [version]", *cliVersion, err)
			return 2
		}
	}
	if rest[0] == "test" && (len(rest) == 1 || rest[1] == "all") {
		if err := maintain.RunImageGate(*printOnly, stdout); err != nil {
			pr.Failf("%v", err)
			return 1
		}
		return 0
	}
	if *cli {
		ready, err := cliVerb(rest[0], *cliVersion, *yes, *printOnly, cwd, stdout, pr)
		if err != nil {
			pr.Failf("%v", err)
			return 1
		}
		if !ready {
			return 0
		}
	}
	if len(rest) == 1 {
		return 0
	}
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

// cliVerb returns whether the CLI work permits the image steps to run.
func cliVerb(verb, version string, yes, printOnly bool, root string, stdout io.Writer, pr *ui.Printer) (bool, error) {
	if printOnly {
		fmt.Fprintln(stdout, "# cli")
	}
	if verb == "build" {
		if err := maintain.RunPlan(maintain.CLIBuildPlan(root), maintain.PlanIO{PrintOnly: printOnly, Out: stdout}); err != nil {
			return false, err
		}
		if !printOnly {
			pr.Okf("installed proveo and proveo-entrypoint into GOPATH/bin; proveo --version reads dev@<sha>")
		}
		return true, nil
	}
	if printOnly {
		for _, line := range release.Plan(version) {
			fmt.Fprintln(stdout, line)
		}
		return true, nil
	}
	return release.Deploy(release.Repo{Root: root}, version, release.DefaultSteps(root, yes, pr), pr)
}
