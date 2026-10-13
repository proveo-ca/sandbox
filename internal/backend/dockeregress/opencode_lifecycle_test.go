// SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/internal/runner/hardened-run-argv.puml
package dockeregress

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/runner"
)

func TestOpenCodeDockerPlanRetainsOnlyStatePublicationFailures(t *testing.T) {
	for _, target := range []string{"opencode", "opencode-browser", "codex"} {
		_, cfg, err := Assemble(Input{Target: target, Image: "fixture", Mode: "open", Credentials: "forward", Sid: "proveo-fixture", UID: "1000", GID: "1000"})
		if err != nil {
			t.Fatal(err)
		}
		args := runner.DockerRunArgs(cfg)
		if target == "codex" {
			if cfg.RetainOnStateFailure || !slices.Contains(args, "--rm") {
				t.Fatalf("another harness changed lifecycle: %+v %v", cfg, args)
			}
			continue
		}
		if !cfg.RetainOnStateFailure || slices.Contains(args, "--rm") || cfg.Name != "proveo-fixture-"+target {
			t.Fatalf("OpenCode plan does not preserve failed private state: %+v %v", cfg, args)
		}
		if !slices.Contains(cfg.Env, "PROVEO_OPENCODE_ENGINE_ID="+cfg.Name) {
			t.Fatalf("plan omitted retained engine identity: %v", cfg.Env)
		}
	}
}

func TestOpenCodeDockerExplicitRemovalFollowsExitCode(t *testing.T) {
	bin := t.TempDir()
	removed := filepath.Join(bin, "removed")
	script := "#!/bin/sh\nif [ \"$1\" = run ]; then exit \"$FAKE_DOCKER_CODE\"; fi\nif [ \"$1\" = rm ]; then printf '%s' \"$2\" > \"$FAKE_DOCKER_REMOVED\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_REMOVED", removed)
	for _, code := range []string{"0", "130", "78", "74"} {
		t.Run(code, func(t *testing.T) {
			t.Setenv("FAKE_DOCKER_CODE", code)
			_ = os.Remove(removed)
			err := ExecAgentWithProxy(runner.Config{Name: "retained-fixture", Image: "fixture", RetainOnStateFailure: true}, nil)
			if code != "0" {
				var exit backend.ExitError
				if !errors.As(err, &exit) || strings.TrimSpace(exit.Error()) != "agent exited with code "+code {
					t.Fatalf("native exit was lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(removed)
			if code == "74" && !os.IsNotExist(statErr) || code != "74" && statErr != nil {
				t.Fatalf("container removal does not match exit %s: %v", code, statErr)
			}
		})
	}
}
