//go:build e2e

// SPEC: _spec/tests/40-agent-e2e-components.puml, _spec/defs/hermes/hermes-paradigm.puml

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestHermesLocalModelE2E drives a real `proveo run hermes --local-model ...`
// session against each model this def was chosen to support, end to end.
// Unlike TestPromptfulE2E's deterministic side-effect task, this asserts the
// model's own prose (a fixed reply), matching internal/imagetest/hermes_test.go's
// hosted-provider check — Hermes's non-interactive `chat -q` tool-invocation
// flags aren't confirmed here, so a plain text round-trip is the reliable
// signal both models are actually reachable and answering.
//
// Neither model is pulled by this test. Like TestPromptfulE2E it assumes an
// operator-populated host Ollama and skips otherwise: Muse Glimmer and Qwen
// 3.8 both ship at 17-18GB minimum with no small distilled variant, so
// auto-pulling here would be a multi-gigabyte download on every run rather
// than an opt-in one.
func TestHermesLocalModelE2E(t *testing.T) {
	requireTmux(t)
	requireDocker(t)

	target := "hermes"
	image := env("PROVEO_TEST_HERMES_IMAGE", harnessImageName(target))
	if !dockerImagePresent(t, image) {
		t.Skipf("harness image %s not built (mise run build %s)", image, target)
	}

	models := []struct{ label, want string }{
		{"qwen3.8", env("PROVEO_TEST_QWEN_TAG", "qwen3.8:latest")},
		{"muse-glimmer", env("PROVEO_TEST_MUSE_GLIMMER_TAG", "muse-glimmer:latest")},
	}
	for _, m := range models {
		t.Run(m.label, func(t *testing.T) {
			tags, err := ollamaTags()
			if err != nil {
				t.Skipf("Ollama unreachable on the host (%v) — no local model to run against", err)
			}
			model, why := resolveOllamaTag(m.want, tags)
			if why != "" {
				t.Skipf("local model %q: %s", m.want, why)
			}

			// proveo refuses a model the host cannot hold rather than swap it in;
			// that is a host precondition, like an absent model, not a hermes fault.
			if err := sbx.LocalModelFits(model, os.Getenv); err != nil {
				t.Skipf("local model %q does not fit this host right now: %v", model, err)
			}

			proveoBin := buildProveo(t)
			work := t.TempDir()

			before, canList := sbxSandboxNames()
			sess := tmux.New(fmt.Sprintf("proveo-e2e-hermes-%s-%d", m.label, os.Getpid()), nil)
			t.Cleanup(func() {
				sess.Kill()
				removeLeakedSandboxes(t, before, canList)
			})

			if err := sess.Start(200, 50, "env", "PROVEO_WIZARD=off", proveoBin, "run", target,
				"--egress-mode", "open", "--local-model", model, "--input", work, "--scope", ".",
				"--", "chat", "-q", "Respond with only the word PONG."); err != nil {
				t.Fatalf("start session: %v", err)
			}

			// A 27B reasoning model under hermes's full tool and skill prompt took
			// 4m17s to its first answer on the host GPU (2026-09-29).
			deadline := time.Now().Add(durationEnv(t, "PROVEO_TEST_TIMEOUT", 10*time.Minute))
			for {
				screen, _ := sess.CaptureAll()
				if answeredPong(screen) {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("hermes chat via %s did not answer PONG within timeout\n--- screen ---\n%s", model, screen)
				}
				time.Sleep(3 * time.Second)
			}
		})
	}
}

// answeredPong finds the model's reply on a line of its own. The prompt echo
// ("Respond with only the word PONG.") carries the word too, so a whole-screen
// match passed before the model said anything.
func answeredPong(screen string) bool {
	for _, line := range strings.Split(screen, "\n") {
		w := strings.Trim(strings.ToUpper(strings.TrimSpace(line)), ".!│ ")
		if w == "PONG" {
			return true
		}
	}
	return false
}
