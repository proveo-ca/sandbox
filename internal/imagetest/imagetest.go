// SPEC: _spec/_plans/host-shell-to-go.puml, _spec/tests/testing-strategy.puml
package imagetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/maintain"
)

// DefaultTimeout bounds every docker call a suite makes.
const DefaultTimeout = 2 * time.Minute

// Resolve picks the image a suite tests: envVar's value when set, else ref,
// then its newer :local build when that names :latest (maintain.ResolveImage).
func Resolve(envVar, ref string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); envVar != "" && v != "" {
		ref = v
	}
	chosen, _ := maintain.ResolveImage(ref, created)
	return chosen
}

func created(ref string) (time.Time, bool) {
	out, err := exec.Command("docker", "image", "inspect", ref, "--format", "{{.Created}}").Output()
	if err != nil {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	return ts, err == nil
}

// Present reports whether ref exists in the local engine.
func Present(ref string) bool {
	return exec.Command("docker", "image", "inspect", ref).Run() == nil
}

// Result is one docker call's combined output and exit status.
type Result struct {
	Out  string
	Err  error
	Code int
}

// OK reports a zero exit.
func (r Result) OK() bool { return r.Err == nil }

// Docker runs `docker args...` bounded by timeout, with extra env.
func Docker(timeout time.Duration, env []string, args ...string) Result {
	return DockerStdin(timeout, env, "", args...)
}

// DockerStdin is Docker with stdin.
func DockerStdin(timeout time.Duration, env []string, stdin string, args ...string) Result {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "docker", args...)
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
	}
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return Result{Out: buf.String(), Err: err, Code: code}
}

// Suite is one image's checks; each check is a subtest named by its
// description, so results compare one-to-one across implementations.
type Suite struct {
	T     *testing.T
	Image string
}

// New skips the whole suite when image is absent.
func New(t *testing.T, image string) *Suite {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if !Present(image) {
		t.Skipf("image %s not built (mise run build)", image)
	}
	t.Logf("image: %s", image)
	return &Suite{T: t, Image: image}
}

// Exec runs cmd under bash in a throwaway container of the suite's image.
func (s *Suite) Exec(cmd string) Result { return ExecIn(s.Image, cmd) }

// ExecIn runs cmd under bash in a throwaway container of image.
func ExecIn(image, cmd string) Result {
	return Docker(DefaultTimeout, nil, "run", "--rm", "--entrypoint", "bash", image, "-c", cmd)
}

// Check runs fn as a subtest named desc.
func (s *Suite) Check(desc string, fn func(t *testing.T)) {
	s.T.Helper()
	s.T.Run(desc, fn)
}

// Skip records desc as skipped with reason.
func (s *Suite) Skip(desc, reason string) {
	s.T.Helper()
	s.T.Run(desc, func(t *testing.T) { t.Skip(reason) })
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// Success asserts cmd exits zero in image.
func (s *Suite) Success(desc, image, cmd string) {
	s.Check(desc, func(t *testing.T) {
		if r := ExecIn(image, cmd); !r.OK() {
			t.Errorf("command failed: %s\n  output: %s", cmd, clip(r.Out))
		}
	})
}

// Failure asserts cmd exits non-zero in image.
func (s *Suite) Failure(desc, image, cmd string) {
	s.Check(desc, func(t *testing.T) {
		if r := ExecIn(image, cmd); r.OK() {
			t.Errorf("expected failure but got success: %s", cmd)
		}
	})
}

// Contains asserts cmd's output contains want.
func (s *Suite) Contains(desc, image, cmd, want string) {
	s.Check(desc, func(t *testing.T) {
		if r := ExecIn(image, cmd); !strings.Contains(r.Out, want) {
			t.Errorf("expected to contain: %s\n  actual: %s", want, clip(r.Out))
		}
	})
}

// Matches asserts some line of cmd's output matches pattern, as grep -E does.
func (s *Suite) Matches(desc, image, cmd, pattern string) {
	s.Check(desc, func(t *testing.T) {
		re := regexp.MustCompile("(?m)" + pattern)
		if r := ExecIn(image, cmd); !re.MatchString(r.Out) {
			t.Errorf("expected to match: %s\n  actual: %s", pattern, clip(r.Out))
		}
	})
}

// Inspect asserts `docker inspect --format=format image` contains want.
func (s *Suite) Inspect(desc, image, format, want string) {
	s.Check(desc, func(t *testing.T) {
		r := Docker(DefaultTimeout, nil, "inspect", "--format="+format, image)
		if !strings.Contains(r.Out, want) {
			t.Errorf("expected: %s\n  got: %s", want, clip(r.Out))
		}
	})
}

// Env reports the first non-empty value among keys.
func Env(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
