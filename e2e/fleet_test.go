//go:build e2e

// SPEC: _spec/internal/maintain/image-build-deploy.puml
package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMiseFleetAcceptsCLIBeforeAll(t *testing.T) {
	mise, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("mise is unavailable")
	}
	for _, tt := range []struct {
		verb string
		want []string
	}{
		{"build", []string{"go install ", "imagebuild hermes --tag local", "imagebuild opencode-browser --tag local"}},
		{"deploy", []string{"git tag <patch>", "imagebuild hermes --tag latest --push", "imagebuild opencode-browser --tag latest --push"}},
	} {
		t.Run(tt.verb, func(t *testing.T) {
			cmd := exec.Command(mise, "run", tt.verb, "--cli", "all", "--print")
			cmd.Dir = ".."
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %v\n%s", cmd.Args, err, out)
			}
			plan := string(out)
			for _, want := range tt.want {
				_, rest, ok := strings.Cut(plan, want)
				if !ok {
					t.Fatalf("plan missing %q in order:\n%s", want, out)
				}
				plan = rest
			}
		})
	}
}
