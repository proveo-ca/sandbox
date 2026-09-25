//go:build e2e

// SPEC: _spec/tests/40-agent-e2e-components.puml, _spec/defs/hermes/hermes-paradigm.puml

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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
		{"muse-glimmer", env("PROVEO_TEST_MUSE_GLIMMER_TAG", "muse-glimmer:30b-nvfp4")},
		{"qwen3.8", env("PROVEO_TEST_QWEN_TAG", "qwen3.8:27b-q4_K_M")},
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

			proveoBin := buildProveo(t)
			work := t.TempDir()

			sess := tmux.New(fmt.Sprintf("proveo-e2e-hermes-%s-%d", m.label, os.Getpid()), nil)
			t.Cleanup(sess.Kill)

			if err := sess.Start(200, 50, "env", "PROVEO_WIZARD=off", proveoBin, "run", target,
				"--egress-mode", "open", "--local-model", model, "--input", work, "--scope", ".",
				"--", "chat", "-q", "Respond with only the word PONG."); err != nil {
				t.Fatalf("start session: %v", err)
			}

			deadline := time.Now().Add(3 * time.Minute)
			for {
				screen, _ := sess.CaptureAll()
				if strings.Contains(strings.ToUpper(screen), "PONG") {
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
