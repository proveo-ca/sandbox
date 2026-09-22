// SPEC: _spec/_plans/host-shell-to-go.puml, _spec/_devops/buildx-driver-selection.puml, _spec/_devops/agent-version-pin.puml
package imagebuild

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/ui"
)

const hubDNSErr = `ERROR: failed to solve: docker/sandbox-templates:shell-docker-0.5.0: failed to resolve source metadata for docker.io/docker/sandbox-templates:shell-docker-0.5.0: failed to do request: Head "https://registry-1.docker.io/v2/docker/sandbox-templates/manifests/shell-docker-0.5.0": dial tcp: lookup registry-1.docker.io: Temporary failure in name resolution
`

type fakeDocker struct {
	calls     []string
	buildErr  string
	published map[string]bool
}

func (f *fakeDocker) Output(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch {
	case len(args) >= 2 && args[0] == "context" && args[1] == "show":
		return "default\n", nil
	case len(args) >= 2 && args[0] == "buildx" && args[1] == "inspect":
		return "Driver: docker\nStatus: running\n", nil
	case len(args) >= 3 && args[1] == "imagetools":
		if f.published[args[3]] {
			return "", nil
		}
		return "", errors.New("not found")
	}
	return "", nil
}

func (f *fakeDocker) Stream(w io.Writer, args ...string) error {
	f.calls = append(f.calls, strings.Join(args, " "))
	if slices.Contains(args, "--pull=false") {
		_, _ = fmt.Fprintln(w, "loaded from local FROM")
		return nil
	}
	if f.buildErr == "" {
		return nil
	}
	_, _ = io.WriteString(w, f.buildErr)
	return errors.New("exit status 1")
}

func (f *fakeDocker) builds() []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "buildx build ") {
			out = append(out, c)
		}
	}
	return out
}

func newTest(t *testing.T, d *fakeDocker, env map[string]string) (*Builder, *strings.Builder) {
	t.Helper()
	var errb strings.Builder
	return &Builder{
		Docker:   d,
		Getenv:   func(k string) string { return env[k] },
		Out:      io.Discard,
		UI:       ui.New(&errb),
		Arch:     "arm64",
		Fetch:    func(string) ([]byte, error) { return nil, errors.New("offline") },
		MkdirAll: os.MkdirAll,
	}, &errb
}

func TestRegistryDNSErrorDetector(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		in   string
		hit  bool
	}{
		{"hub lookup", hubDNSErr, true},
		{"no such host", "dial tcp: lookup registry-1.docker.io: no such host\n", true},
		{"unrelated", "ERROR: failed to solve: base name should not be blank\n", false},
	} {
		if got := IsRegistryDNSError(c.in); got != c.hit {
			t.Errorf("%s: IsRegistryDNSError = %v, want %v", c.name, got, c.hit)
		}
	}
}

func TestPullFlagsFromEnv(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		env    string
		want   string
		wantOK bool
	}{
		{"auto", "", true},
		{"0", "--pull=false", true},
		{"never", "--pull=false", true},
		{"1", "--pull=true", true},
		{"always", "--pull=true", true},
		{"maybe", "", false},
	} {
		b, errb := newTest(t, &fakeDocker{}, map[string]string{"PROVEO_DOCKER_PULL": c.env})
		got, err := b.PullFlags()
		if !c.wantOK {
			if err == nil || !strings.Contains(errb.String(), "PROVEO_DOCKER_PULL") {
				t.Errorf("%s: want a refusal naming the env, got err=%v stderr=%q", c.env, err, errb)
			}
			continue
		}
		if err != nil || strings.Join(got, " ") != c.want {
			t.Errorf("%s: PullFlags = %v, %v; want %q", c.env, got, err, c.want)
		}
	}
}

func TestLoadRetriesWithoutPullOnHubDNS(t *testing.T) {
	t.Parallel()
	for _, pull := range []string{"", "auto"} {
		d := &fakeDocker{buildErr: hubDNSErr}
		b, errb := newTest(t, d, map[string]string{"PROVEO_DOCKER_PULL": pull, "PROVEO_BUILDKIT_CACHE": "0"})
		if err := b.Build(false, []string{"-t", "proveo/base:local", "/tmp"}); err != nil {
			t.Fatalf("PULL=%q: retry should succeed with --pull=false: %v\n%s", pull, err, errb)
		}
		builds := d.builds()
		if len(builds) != 2 || !strings.Contains(builds[1], "--pull=false") {
			t.Fatalf("PULL=%q: builds = %q, want a fail then --pull=false", pull, builds)
		}
		if !strings.Contains(errb.String(), "retrying with --pull=false") {
			t.Errorf("PULL=%q: no retry notice:\n%s", pull, errb)
		}
	}
}

func TestLoadDoesNotRetryOnUnrelatedBuildError(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{buildErr: "ERROR: failed to solve: base name should not be blank\n"}
	b, _ := newTest(t, d, map[string]string{"PROVEO_BUILDKIT_CACHE": "0"})
	if err := b.Build(false, []string{"-t", "proveo/base:local", "/tmp"}); err == nil {
		t.Fatal("blank FROM must still fail")
	}
	if builds := d.builds(); len(builds) != 1 || strings.Contains(builds[0], "--pull=false") {
		t.Fatalf("non-DNS failure must build once without --pull=false: %q", builds)
	}
}

func TestLoadPullNeverSkipsTheRegistryHEAD(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{buildErr: hubDNSErr}
	b, _ := newTest(t, d, map[string]string{"PROVEO_DOCKER_PULL": "0", "PROVEO_BUILDKIT_CACHE": "0"})
	if err := b.Build(false, []string{"-t", "proveo/base:local", "/tmp"}); err != nil {
		t.Fatalf("PROVEO_DOCKER_PULL=0 should --pull=false on the first invoke: %v", err)
	}
	if builds := d.builds(); len(builds) != 1 || !strings.Contains(builds[0], "--pull=false") {
		t.Fatalf("builds = %q, want one build carrying --pull=false", builds)
	}
}

func TestRegistryDNSHelpPrintsOnAFinalDNSFailure(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{buildErr: hubDNSErr}
	b, errb := newTest(t, d, map[string]string{"PROVEO_DOCKER_PULL": "1", "PROVEO_BUILDKIT_CACHE": "0"})
	if err := b.Build(false, []string{"-t", "proveo/base:local", "/tmp"}); err == nil {
		t.Fatal("an explicit --pull=true must not be retried away")
	}
	for _, want := range []string{"registry-1.docker.io", "PROVEO_DOCKER_PULL=0",
		"docker pull docker/sandbox-templates:shell-docker-0.5.0", "docker buildx rm proveo-multiarch"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("help missing %q\n%s", want, errb)
		}
	}
}

func TestLoadRefusesTheLatestTag(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"proveo/base:latest", "proveo/base"} {
		d := &fakeDocker{}
		b, errb := newTest(t, d, nil)
		if err := b.Build(false, []string{"-t", ref, "/tmp"}); err == nil || !strings.Contains(errb.String(), "refusing to --load") {
			t.Errorf("%s: want a refusal, got err=%v\n%s", ref, err, errb)
		}
		if len(d.builds()) != 0 {
			t.Errorf("%s: refused build still ran buildx", ref)
		}
	}
}

func TestLoadAndPushShareTheNamedBuildKitCache(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	d := &fakeDocker{}
	b, errb := newTest(t, d, map[string]string{"PROVEO_BUILDKIT_CACHE_DIR": cacheDir})
	if err := b.Build(false, []string{"-t", "proveo/cursor:local", "/tmp"}); err != nil {
		t.Fatalf("load: %v\n%s", err, errb)
	}
	if err := b.Build(true, []string{"-t", "proveo/cursor:latest", "/tmp"}); err != nil {
		t.Fatalf("push: %v\n%s", err, errb)
	}
	want := filepath.Join(cacheDir, "proveo", "cursor")
	from := "--cache-from=type=local,src=" + want
	to := "--cache-to=type=local,dest=" + want + ",mode=max"
	builds := d.builds()
	if len(builds) != 2 {
		t.Fatalf("builds = %q", builds)
	}
	for i, mode := range []string{"--load", "--push"} {
		for _, arg := range []string{from, to, mode} {
			if !strings.Contains(builds[i], arg) {
				t.Errorf("build %d lacks %q\n%s", i, arg, builds[i])
			}
		}
	}
	if !strings.Contains(builds[0], "--platform linux/arm64 ") || !strings.Contains(builds[1], "--platform linux/amd64,linux/arm64 ") {
		t.Errorf("load must be host-only and push multi-arch: %q", builds)
	}
}

func TestNamedBuildKitCacheCanBeDisabled(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{}
	b, _ := newTest(t, d, map[string]string{"PROVEO_BUILDKIT_CACHE": "0", "PROVEO_BUILDKIT_CACHE_DIR": t.TempDir()})
	if err := b.Build(false, []string{"-t", "proveo/cursor:local", "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(d.builds(), " "), "--cache-") {
		t.Fatalf("PROVEO_BUILDKIT_CACHE=0 still passed a cache flag: %q", d.builds())
	}
}

func TestAgentVersionResolverIsUniformAcrossEcosystems(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body          string
		env                 map[string]string
		override, eco, pkg  string
		wantURL, want, note string
	}{
		{name: "npm registry", body: `{"name":"opencode-ai","version":"1.18.27"}`,
			override: "OPENCODE_VERSION", eco: "npm", pkg: "opencode-ai",
			wantURL: "https://registry.npmjs.org/opencode-ai/latest", want: "1.18.27",
			note: "pin: opencode-ai@1.18.27 (resolved upstream; override with OPENCODE_VERSION=<version>)"},
		{name: "npm honours NPM_CONFIG_REGISTRY", body: `{"version":"2.0.0"}`,
			env:      map[string]string{"NPM_CONFIG_REGISTRY": "https://npm.example/"},
			override: "OPENCODE_VERSION", eco: "npm", pkg: "opencode-ai",
			wantURL: "https://npm.example/opencode-ai/latest", want: "2.0.0", note: "@2.0.0 (resolved upstream"},
		{name: "pypi current release", body: `{"info":{"name":"cecli-dev","version":"1.4.0"},"releases":{"1.3.0":[]}}`,
			override: "CECLI_VERSION", eco: "pypi", pkg: "cecli-dev",
			wantURL: "https://pypi.org/pypi/cecli-dev/json", want: "1.4.0",
			note: "pin: cecli-dev@1.4.0 (resolved upstream; override with CECLI_VERSION=<version>)"},
		{name: "cursor reads the release out of the installer",
			body:     "FINAL_DIR=\"$HOME/.local/share/cursor-agent/versions/2026.08.31-4057e58\"\nln -s ~/.local/share/cursor-agent/versions/2026.08.31-4057e58/cursor-agent ~/.local/bin/agent\n",
			override: "CURSOR_AGENT_VERSION", eco: "cursor", pkg: "https://cursor.com/install",
			wantURL: "https://cursor.com/install", want: "2026.08.31-4057e58",
			note: "@2026.08.31-4057e58 (resolved upstream; override with CURSOR_AGENT_VERSION=<version>)"},
		{name: "an exported override wins without asking upstream",
			env:      map[string]string{"OPENCODE_VERSION": "1.18.20"},
			override: "OPENCODE_VERSION", eco: "npm", pkg: "opencode-ai",
			want: "1.18.20", note: "pin: opencode-ai@1.18.20 (from OPENCODE_VERSION)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b, errb := newTest(t, &fakeDocker{}, c.env)
			var asked []string
			b.Fetch = func(url string) ([]byte, error) { asked = append(asked, url); return []byte(c.body), nil }
			got, err := b.AgentVersion(c.override, c.eco, c.pkg)
			if err != nil || got != c.want {
				t.Fatalf("AgentVersion = %q, %v; want %q\n%s", got, err, c.want, errb)
			}
			if !strings.Contains(errb.String(), c.note) {
				t.Errorf("stderr lacks %q:\n%s", c.note, errb)
			}
			if c.wantURL == "" && len(asked) != 0 {
				t.Errorf("an override must not ask upstream, fetched %q", asked)
			}
			if c.wantURL != "" && (len(asked) != 1 || asked[0] != c.wantURL) {
				t.Errorf("fetched %q, want [%s]", asked, c.wantURL)
			}
		})
	}
}

func TestAgentVersionResolverRefusesRatherThanGuessing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ override, eco, pkg string }{
		{"OPENCODE_VERSION", "npm", "opencode-ai"},
		{"CECLI_VERSION", "pypi", "cecli-dev"},
	} {
		b, errb := newTest(t, &fakeDocker{}, nil)
		got, err := b.AgentVersion(c.override, c.eco, c.pkg)
		if err == nil || got != "" {
			t.Fatalf("%s: resolver succeeded offline with %q — a caller would bake it", c.eco, got)
		}
		for _, want := range []string{"could not resolve", c.override + "=<x.y.z>"} {
			if !strings.Contains(errb.String(), want) {
				t.Errorf("%s: failure message lacks %q:\n%s", c.eco, want, errb)
			}
		}
	}
	b, errb := newTest(t, &fakeDocker{}, nil)
	if _, err := b.AgentVersion("X_VERSION", "cargo", "x"); err == nil || !strings.Contains(errb.String(), "unknown ecosystem") {
		t.Errorf("unknown ecosystem accepted (err=%v):\n%s", err, errb)
	}
}

func TestRequirePublishedNamesTheDeployOrder(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{published: map[string]bool{"proveo/base:latest": true}}
	b, errb := newTest(t, d, nil)
	if err := b.RequirePublished("proveo/base:latest", "latest"); err != nil {
		t.Fatalf("a published parent was refused: %v", err)
	}
	if err := b.RequirePublished("proveo/claudecode:rc1", "rc1"); err == nil {
		t.Fatal("an unpublished parent was accepted")
	}
	for _, want := range []string{"proveo/claudecode:rc1 is not published", "proveo deploy all --tag rc1", "proveo deploy claudecode --tag rc1"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, errb)
		}
	}
}

func TestRefTagAndRepo(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ ref, tag, repo string }{
		{"proveo/base:local", "local", "proveo/base"},
		{"proveo/base", "latest", "proveo/base"},
		{"localhost:5000/proveo/base:rc1", "rc1", "localhost:5000/proveo/base"},
		{"localhost:5000/proveo/base", "latest", "localhost:5000/proveo/base"},
	} {
		if got := RefTag(c.ref); got != c.tag {
			t.Errorf("RefTag(%q) = %q, want %q", c.ref, got, c.tag)
		}
		if got := RefRepo(c.ref); got != c.repo {
			t.Errorf("RefRepo(%q) = %q, want %q", c.ref, got, c.repo)
		}
	}
}

func TestLoadBuilderPrefersARunningDockerDriver(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{}
	b, _ := newTest(t, d, nil)
	got, err := b.EnsureBuildx(false, "linux/arm64")
	if err != nil || got != "default" {
		t.Fatalf("EnsureBuildx(load, host) = %q, %v; want the running docker-driver context", got, err)
	}
	if got, _ := b.EnsureBuildx(false, "linux/amd64"); got != "proveo-multiarch" {
		t.Errorf("a foreign platform must use the container builder, got %q", got)
	}
	if got, _ := b.EnsureBuildx(true, "linux/amd64,linux/arm64"); got != "proveo-multiarch" {
		t.Errorf("push must use the container builder, got %q", got)
	}
}
