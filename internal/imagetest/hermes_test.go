//go:build image

// SPEC: _spec/tests/testing-strategy.puml, _spec/defs/hermes/hermes-paradigm.puml
package imagetest_test

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagetest"
)

var hmSeq atomic.Int64

// hmRun runs `docker run --name <unique> args...` bounded by timeout and
// force-removes the container afterwards, so a timed-out client leaves nothing behind.
func hmRun(t *testing.T, timeout time.Duration, env []string, args ...string) imagetest.Result {
	t.Helper()
	name := fmt.Sprintf("proveo-imagetest-hermes-%d-%d", os.Getpid(), hmSeq.Add(1))
	t.Cleanup(func() { imagetest.Docker(30*time.Second, nil, "rm", "-f", name) })
	return imagetest.Docker(timeout, env, append([]string{"run", "--rm", "--name", name}, args...)...)
}

func hmClip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func TestImageHermes(t *testing.T) {
	image := imagetest.Resolve("IMAGE", "proveo/hermes:latest")
	s := imagetest.New(t, image)

	hmBuild(s)
	hmTools(s)
	hmSecurity(s)
	hmLocalModel(s)
	hmLLM(s)
}

func hmBuild(s *imagetest.Suite) {
	img := s.Image
	s.Check("image is available", func(t *testing.T) {
		if !imagetest.Present(img) {
			t.Fatalf("image %s not present", img)
		}
	})
	s.Inspect("has security.non-root=true label", img, `{{index .Config.Labels "security.non-root"}}`, "true")
	s.Inspect("entrypoint uses dumb-init", img, `{{json .Config.Entrypoint}}`, "dumb-init")

	// SPEC: _spec/_devops/agent-version-pin.puml
	s.Inspect("proveo.agent label names the agent package", img, `{{index .Config.Labels "proveo.agent"}}`, "hermes-agent")

	// SPEC: _spec/packages/lib/seed-and-launch.puml
	s.Success("ships the Kit's startup command (/usr/local/bin/proveo-seed)", img, "test -x /usr/local/bin/proveo-seed")
}

func hmTools(s *imagetest.Suite) {
	img := s.Image
	tools := []struct{ name, cmd string }{
		{"hermes", "/opt/hermes/bin/hermes --version"},
		{"python3", "python3 --version"},
		{"node", "node --version"},
		{"git", "git --version"},
		{"ffmpeg", "ffmpeg -hide_banner -version"},
		{"rg", "rg --version"},
		{"dumb-init", "dumb-init --version"},
	}
	for _, tl := range tools {
		s.Success(tl.name+" is installed", img, tl.cmd)
	}

	// The browser tool is native to hermes, not a bolt-on -browser variant —
	// unlike every other def, this image always carries a working Chromium.
	s.Success("chromium binary resolved by upstream's own pin is present and runs", img,
		`test -f /etc/hermes/agent-browser-executable-path && "$(cat /etc/hermes/agent-browser-executable-path)" --version`)
}

func hmSecurity(s *imagetest.Suite) {
	img := s.Image
	s.Failure("no setuid binaries", img, "find / -xdev -perm -4000 -type f 2>/dev/null | grep -q .")
	s.Failure("no setgid binaries", img, "find / -xdev -perm -2000 -type f 2>/dev/null | grep -q .")
	s.Failure("nc not available", img, "which nc")
	s.Failure("netcat not available", img, "which netcat")

	// hermes's own exec shim drops root -> the "hermes" user; confirm it
	// actually does, since this def (unlike every other one here) stays root
	// at build time so entrypoint.sh can run upstream's stage2-hook.sh.
	s.Contains("hermes exec shim drops root to the hermes user", img,
		"/opt/hermes/bin/hermes --version >/dev/null 2>&1; id -un", "hermes")

	// SPEC: _spec/defs/hermes/hermes-paradigm.puml — the excluded stealth tier
	s.Failure("no bot-detection-evasion config anywhere in the image", img,
		`grep -riE "residential.proxy|captcha.solv|fingerprint.rand" /entrypoint.sh /opt/proveo 2>/dev/null`)
}

func hmLocalModel(s *imagetest.Suite) {
	img := s.Image
	s.Check("PROVEO_LOCAL_MODEL wires an OpenAI-compatible endpoint, not a config file", func(t *testing.T) {
		r := hmRun(t, 30*time.Second, nil,
			"-e", "PROVEO_LOCAL_MODEL=qwen3.8", "-e", "OLLAMA_API_BASE=http://ollama:11434",
			"--entrypoint", "bash", img, "-c",
			`source /entrypoint-lib.sh 2>/dev/null; source <(sed -n '/^configure_hermes_local_model/,/^}/p' /entrypoint.sh); configure_hermes_local_model; echo "BASE=$OPENAI_BASE_URL MODEL=$HERMES_MODEL"`)
		want := "BASE=http://ollama:11434/v1 MODEL=ollama/qwen3.8"
		if !strings.Contains(r.Out, want) {
			t.Errorf("expected %q, output: %s", want, hmClip(r.Out, 300))
		}
	})
}

func hmLLM(s *imagetest.Suite) {
	img := s.Image
	providers := []struct{ provider, envvar string }{
		{"openai", "OPENAI_API_KEY"},
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"xai", "XAI_API_KEY"},
	}
	for _, p := range providers {
		desc := "[" + p.provider + "] hermes chat completes via " + p.envvar
		key := os.Getenv(p.envvar)
		if key == "" {
			s.Skip(desc, "no "+p.envvar)
			continue
		}
		s.Check(desc, func(t *testing.T) {
			r := hmRun(t, 150*time.Second, nil, "-e", p.envvar+"="+key, "--entrypoint", "bash", img, "-c",
				`timeout 120 /opt/hermes/bin/hermes chat -q "Respond with only the word PONG." 2>&1`)
			if !strings.Contains(strings.ToUpper(r.Out), "PONG") {
				t.Errorf("[%s] hermes chat (output: %s)", p.provider, hmClip(r.Out, 300))
			}
		})
	}
}
