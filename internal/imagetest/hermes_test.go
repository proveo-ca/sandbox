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
	// unlike every other def, this image always carries a working browser.
	// v2026.9.24's non-desktop build stages Playwright's headless-shell
	// Chromium (not the "full Chromium" the desktop/-desktop tag ships), at a
	// version-and-arch-suffixed path, so this globs for it rather than a
	// fixed path or the /etc/hermes marker file the desktop build alone
	// writes.
	s.Success("headless-shell chromium resolved by upstream's own pin is present and runs", img,
		`bin="$(find /opt/hermes/.playwright -maxdepth 3 -type f -iname 'chrome-headless-shell' 2>/dev/null | head -1)"; test -n "$bin" && "$bin" --version`)
}

// hermesKnownSetuidBinaries is the accepted baseline: standard Debian
// shadow-utils/util-linux setuid tools this image inherits from upstream
// (unlike proveo's own base images, never run through proveo-harden), plus
// s6-overlay-suexec and ssh-agent/ssh-keysign, which the exec shim's own
// privilege drop and git-over-ssh respectively depend on. A binary NOT on
// this list is the regression signal; these specific ones are not.
// SPEC: _spec/defs/hermes/hermes-paradigm.puml
var hermesKnownSetuidBinaries = map[string]bool{
	"/usr/bin/chfn": true, "/usr/bin/umount": true, "/usr/bin/gpasswd": true,
	"/usr/bin/mount": true, "/usr/bin/newgrp": true, "/usr/bin/chsh": true,
	"/usr/bin/expiry": true, "/usr/bin/chage": true, "/usr/bin/passwd": true,
	"/usr/bin/su": true, "/usr/bin/ssh-agent": true, "/usr/lib/openssh/ssh-keysign": true,
	"/usr/sbin/unix_chkpwd": true,
	"/package/admin/s6-overlay-helpers-0.1.2.2/command/s6-overlay-suexec": true,
}

func hmSecurity(s *imagetest.Suite) {
	img := s.Image
	s.Check("no setuid/setgid binaries beyond the accepted upstream baseline", func(t *testing.T) {
		r := hmRun(t, imagetest.DefaultTimeout, nil, "--entrypoint", "bash", img, "-c",
			"find / -xdev \\( -perm -4000 -o -perm -2000 \\) -type f 2>/dev/null")
		var unexpected []string
		for line := range strings.SplitSeq(strings.TrimSpace(r.Out), "\n") {
			if line == "" || hermesKnownSetuidBinaries[line] {
				continue
			}
			unexpected = append(unexpected, line)
		}
		if len(unexpected) > 0 {
			t.Errorf("new setuid/setgid binaries not on the accepted baseline: %s", strings.Join(unexpected, ", "))
		}
	})
	s.Failure("nc not available", img, "which nc")
	s.Failure("netcat not available", img, "which netcat")

	// hermes's own exec shim drops root -> the "hermes" user via
	// /command/s6-setuidgid before exec'ing the real binary, so a bare
	// `id -un` after the call reports the CALLER's uid, not the dropped
	// child's — the shim's process image is long gone by then. The real
	// signal is its own contract: it fails loud with a specific message and
	// exit 126 if /command/s6-setuidgid is missing, so success + no such
	// message is what the drop actually happening looks like from outside.
	s.Success("hermes exec shim runs (drops root -> hermes internally, or fails loud)", img,
		"/opt/hermes/bin/hermes --version")
	s.Failure("hermes exec shim did not refuse and silently stay root", img,
		"/opt/hermes/bin/hermes --version 2>&1 | grep -q 'refusing to silently run as root'")

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

// TestImageHermesBakedModel is the "easier to ensure" local-model check: a
// baked variant needs no host-side pre-pulled Ollama, no --local-model flag,
// and no network dependency for inference, so unlike
// e2e/hermes_local_model_test.go this round-trips for real on every run where
// the image is built -- the same imagetest.New skip-if-absent gate as
// TestImageHermes, nothing more to precondition.
func TestImageHermesBakedModel(t *testing.T) {
	variants := []struct{ envVar, name, tag string }{
		{"MUSE_GLIMMER_IMAGE", "hermes-muse-glimmer", "muse-glimmer:30b-q4_K_M"},
		{"QWEN_IMAGE", "hermes-qwen3.8", "qwen3.8:27b-q4_K_M"},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			img := imagetest.Resolve(v.envVar, "proveo/"+v.name+":latest")
			s := imagetest.New(t, img)
			s.Inspect("proveo.baked-model label names the pulled tag", img,
				`{{index .Config.Labels "proveo.baked-model"}}`, v.tag)
			s.Check("hermes chat completes via the baked model, no env vars needed", func(t *testing.T) {
				r := hmRun(t, 180*time.Second, nil, "--entrypoint", "bash", img, "-c",
					`timeout 150 /opt/hermes/bin/hermes chat -q "Respond with only the word PONG." 2>&1`)
				if !strings.Contains(strings.ToUpper(r.Out), "PONG") {
					t.Errorf("[%s] hermes chat via baked model (output: %s)", v.name, hmClip(r.Out, 400))
				}
			})
		})
	}
}
