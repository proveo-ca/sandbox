// SPEC: _spec/_plans/host-shell-to-go.puml, _spec/_devops/buildx-driver-selection.puml, _spec/_devops/agent-version-pin.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/internal/maintain/build-schedule.puml
package imagebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/ui"
)

// Docker runs the docker CLI.
type Docker interface {
	Output(args ...string) (string, error)
	Stream(w io.Writer, args ...string) error
}

// ExecDocker is the real docker CLI.
type ExecDocker struct{}

func (ExecDocker) Output(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).Output()
	return string(out), err
}

func (ExecDocker) Stream(w io.Writer, args ...string) error {
	c := exec.Command("docker", args...)
	c.Stdout, c.Stderr = w, w
	return c.Run()
}

// Builder holds everything a build reads from its surroundings.
type Builder struct {
	Docker   Docker
	Getenv   func(string) string
	Out      io.Writer
	UI       *ui.Printer
	Arch     string
	Fetch    func(url string) ([]byte, error)
	MkdirAll func(path string, perm os.FileMode) error
}

// New is a Builder wired to the real host.
func New() *Builder {
	return &Builder{
		Docker: ExecDocker{}, Getenv: os.Getenv, Out: os.Stdout, UI: ui.Default,
		Arch: runtime.GOARCH, Fetch: httpFetch, MkdirAll: os.MkdirAll,
	}
}

func httpFetch(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (b *Builder) env(k string) string {
	if b.Getenv == nil {
		return ""
	}
	return b.Getenv(k)
}

// ── agent version pin ───────────────────────────────────────────────────────

var cursorVersion = regexp.MustCompile(`/versions/([0-9][0-9.]*-[0-9a-f]+)/`)

// AgentVersion resolves the release an image pins its agent to.
func (b *Builder) AgentVersion(overrideVar, eco, pkg string) (string, error) {
	if v := strings.TrimSpace(b.env(overrideVar)); v != "" {
		b.UI.Notef("pin: %s@%s (from %s)", pkg, v, overrideVar)
		return v, nil
	}
	var v string
	var lastErr error
	switch eco {
	case "npm":
		registry := strings.TrimRight(b.env("NPM_CONFIG_REGISTRY"), "/")
		if registry == "" {
			registry = "https://registry.npmjs.org"
		}
		v, lastErr = b.jsonField(registry+"/"+pkg+"/latest", "version")
	case "pypi":
		v, lastErr = b.jsonField("https://pypi.org/pypi/"+pkg+"/json", "info", "version")
	case "cursor":
		var body []byte
		if body, lastErr = b.Fetch(pkg); lastErr == nil {
			if m := cursorVersion.FindSubmatch(body); m != nil {
				v = string(m[1])
			}
		}
	default:
		b.UI.Failf("agent version: unknown ecosystem '%s' (want npm|pypi|cursor)", eco)
		return "", fmt.Errorf("unknown ecosystem %q", eco)
	}
	v = strings.Join(strings.Fields(v), "")
	if v == "" {
		last := ""
		if lastErr != nil {
			last = fmt.Sprintf("\n  last fetch: %v", lastErr)
		}
		b.UI.Failf("could not resolve the current %s release (%s).\n"+
			"  The agent install is pinned by version, so a rebuild is reproducible and a\n"+
			"  cached layer cannot hide an upstream release. Offline or behind a proxy, name\n"+
			"  the version yourself:   %s=<x.y.z> proveo build <target>%s", pkg, eco, overrideVar, last)
		return "", fmt.Errorf("could not resolve %s (%s)", pkg, eco)
	}
	b.UI.Notef("pin: %s@%s (resolved upstream; override with %s=<version>)", pkg, v, overrideVar)
	return v, nil
}

func (b *Builder) jsonField(url string, path ...string) (string, error) {
	body, err := b.Fetch(url)
	if err != nil {
		return "", err
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return "", nil
		}
		v = m[k]
	}
	s, _ := v.(string)
	return s, nil
}

// ── image references ───────────────────────────────────────────────────────

// HostPlatform is the single platform a local --load builds.
func HostPlatform(arch string) string {
	if arch == "arm64" {
		return "linux/arm64"
	}
	return "linux/amd64"
}

// ImageRef is overrideVar's value, else repo:tag.
func (b *Builder) ImageRef(overrideVar, repo, tag string) string {
	if v := b.env(overrideVar); v != "" {
		return v
	}
	if tag == "" {
		tag = "latest"
	}
	return repo + ":" + tag
}

// RefTag is the tag of ref, "latest" when it names none.
func RefTag(ref string) string {
	last := ref[strings.LastIndex(ref, "/")+1:]
	if _, tag, ok := strings.Cut(last, ":"); ok {
		return tag
	}
	return "latest"
}

// RefRepo is ref without its tag.
func RefRepo(ref string) string {
	slash := strings.LastIndex(ref, "/")
	if i := strings.IndexByte(ref[slash+1:], ':'); i >= 0 {
		return ref[:slash+1+i]
	}
	return ref
}

// RequirePublished refuses a --push whose parent is not in the registry.
func (b *Builder) RequirePublished(ref, tag string) error {
	if _, err := b.Docker.Output("buildx", "imagetools", "inspect", ref); err == nil {
		return nil
	}
	name := RefRepo(ref[strings.LastIndex(ref, "/")+1:])
	tagflag := ""
	if tag != "" && tag != "latest" {
		tagflag = " --tag " + tag
	}
	b.UI.Failf("%s is not published.\n"+
		"  A --push build takes its base from the registry, and this script will\n"+
		"  not publish %s as a side effect of deploying its child.\n"+
		"  Deploying one target whose parents are unpublished is not supported yet.\n"+
		"  →  proveo deploy all%s   — publishes every base before its children\n"+
		"  →  proveo deploy %s%s   — then re-run this target", ref, ref, tagflag, name, tagflag)
	return fmt.Errorf("%s is not published", ref)
}

// ── buildx builder ─────────────────────────────────────────────────────────

func (b *Builder) inspectField(builder, field, want string) bool {
	out, _ := b.Docker.Output("buildx", "inspect", builder)
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.EqualFold(f[0], field+":") && strings.EqualFold(f[1], want) {
			return true
		}
	}
	return false
}

func (b *Builder) containerBuilder() string {
	name := b.env("PROVEO_BUILDX_BUILDER")
	if name == "" {
		name = "proveo-multiarch"
	}
	if !b.inspectField(name, "status", "running") {
		b.UI.Notef("creating buildx builder %s (docker-container)", name)
		_, _ = b.Docker.Output("buildx", "create", "--name", name, "--driver", "docker-container", "--bootstrap")
	}
	return name
}

// EnsureBuildx picks the builder: a docker-driver one for a host-platform
// --load, the container driver for --push or a foreign platform.
func (b *Builder) EnsureBuildx(push bool, platforms string) (string, error) {
	if _, err := b.Docker.Output("buildx", "version"); err != nil {
		b.UI.Failf("docker buildx is required for proveo image builds")
		return "", errors.New("docker buildx unavailable")
	}
	if push {
		return b.containerBuilder(), nil
	}
	host := HostPlatform(b.Arch)
	if platforms != "" && platforms != host {
		b.UI.Notef("%s != host %s: using the cross-capable container driver.\n"+
			"  A locally built parent image is NOT visible to it — base images resolve from the registry.", platforms, host)
		return b.containerBuilder(), nil
	}
	if v := b.env("PROVEO_BUILDX_LOCAL_BUILDER"); v != "" {
		return v, nil
	}
	if ctx, _ := b.Docker.Output("context", "show"); strings.TrimSpace(ctx) != "" {
		ctx = strings.TrimSpace(ctx)
		if b.inspectField(ctx, "driver", "docker") && b.inspectField(ctx, "status", "running") {
			return ctx, nil
		}
	}
	name := b.containerBuilder()
	b.UI.Warnf("no running docker-driver builder; using %s — a locally built base image\n"+
		"  will NOT be visible to its dependents (override with PROVEO_BUILDX_LOCAL_BUILDER)", name)
	return name, nil
}

// ── flags ──────────────────────────────────────────────────────────────────

// PullFlags maps PROVEO_DOCKER_PULL onto buildx's --pull.
func (b *Builder) PullFlags() ([]string, error) {
	switch v := b.env("PROVEO_DOCKER_PULL"); v {
	case "", "auto":
		return nil, nil
	case "0", "false", "no", "never", "off":
		return []string{"--pull=false"}, nil
	case "1", "true", "yes", "on", "always":
		return []string{"--pull=true"}, nil
	default:
		b.UI.Failf("PROVEO_DOCKER_PULL=%s: want auto|0|1 (or never/always)", v)
		return nil, fmt.Errorf("PROVEO_DOCKER_PULL=%s", v)
	}
}

// HasPull reports whether argv already decides --pull.
func HasPull(argv []string) bool {
	for _, a := range argv {
		if a == "--pull" || strings.HasPrefix(a, "--pull=") {
			return true
		}
	}
	return false
}

// ArgRef is the value of the first -t/--tag in argv.
func ArgRef(argv []string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-t" || argv[i] == "--tag" {
			return argv[i+1], true
		}
	}
	return "", false
}

// CacheFlags names the local BuildKit cache shared by --load and --push.
func (b *Builder) CacheFlags(argv []string) []string {
	switch b.env("PROVEO_BUILDKIT_CACHE") {
	case "0", "false", "no", "off":
		return nil
	}
	ref, ok := ArgRef(argv)
	if !ok {
		return nil
	}
	root := b.env("PROVEO_BUILDKIT_CACHE_DIR")
	if root == "" {
		base := b.env("XDG_CACHE_HOME")
		if base == "" {
			base = filepath.Join(b.env("HOME"), ".cache")
		}
		root = filepath.Join(base, "proveo", "buildkit")
	}
	dest := filepath.Join(root, RefRepo(ref))
	if err := b.MkdirAll(dest, 0o755); err != nil {
		b.UI.Warnf("buildkit cache dir %s is not writable; building without a named cache", dest)
		return nil
	}
	return []string{"--cache-from=type=local,src=" + dest, "--cache-to=type=local,dest=" + dest + ",mode=max"}
}

var registryDNS = regexp.MustCompile(`(?i)temporary failure in name resolution|lookup registry-1\.docker\.io|no such host|server misbehaving`)

// IsRegistryDNSError reports a build log that failed on Docker Hub's DNS.
func IsRegistryDNSError(log string) bool { return registryDNS.MatchString(log) }

// RegistryDNSHelp names the escape hatches for a Hub DNS failure.
const RegistryDNSHelp = `Docker Hub name lookup failed (registry-1.docker.io).
   That is the daemon's DNS, not the Dockerfile FROM line.

   If the FROM image is already local, skip the registry HEAD:
     PROVEO_DOCKER_PULL=0 mise run build

   If it is not local, Hub must resolve first:
     docker pull docker/sandbox-templates:shell-docker-0.5.0
     getent hosts registry-1.docker.io

   Frozen container-builder resolv.conf (proveo-multiarch):
     docker buildx rm proveo-multiarch
`

// ── build ──────────────────────────────────────────────────────────────────

// Build runs one buildx build: --load a single host-platform image, or
// --push every platform.
func (b *Builder) Build(push bool, args []string) error {
	switch b.env("PROVEO_DOCKER_PUSH") {
	case "1", "true", "yes", "on":
		push = true
	}
	platforms := b.env("PROVEO_PLATFORMS")
	if platforms == "" {
		platforms = "linux/amd64,linux/arm64"
	}
	out := []string{"--push"}
	if !push {
		if ref, _ := ArgRef(args); RefTag(ref) == "latest" {
			b.UI.Failf("refusing to --load an image tagged :latest.\n" +
				"  :latest means published. Build locally as :local, then promote:\n" +
				"  →  proveo build <target>            — writes :local\n" +
				"  →  proveo deploy <target>           — promotes :local to :latest and pushes")
			return errors.New("refusing to --load :latest")
		}
		if strings.Contains(platforms, ",") {
			platforms = HostPlatform(b.Arch)
			b.UI.Notef("local image load is single-platform; building %s (PROVEO_DOCKER_PUSH=1 / --push publishes amd64+arm64)", platforms)
		}
		out = []string{"--load"}
	}
	builder, err := b.EnsureBuildx(push, platforms)
	if err != nil {
		return err
	}
	var pull []string
	if !HasPull(args) {
		if pull, err = b.PullFlags(); err != nil {
			return err
		}
	}
	cache := b.CacheFlags(args)

	invoke := func(pull []string) (string, error) {
		argv := append(append(append(append([]string{}, out...), pull...), cache...), args...)
		b.UI.Notef("buildx --builder %s --platform %s %s", builder, platforms, strings.Join(argv, " "))
		var log bytes.Buffer
		err := b.Docker.Stream(io.MultiWriter(b.Out, &log),
			append([]string{"buildx", "build", "--builder", builder, "--platform", platforms}, argv...)...)
		return log.String(), err
	}
	log, err := invoke(pull)
	pullAuto := b.env("PROVEO_DOCKER_PULL") == "" || b.env("PROVEO_DOCKER_PULL") == "auto"
	if err != nil && !push && pullAuto && len(pull) == 0 && !HasPull(args) && IsRegistryDNSError(log) {
		b.UI.Warnf("registry DNS failed; retrying with --pull=false so a local FROM image can satisfy the build")
		log, err = invoke([]string{"--pull=false"})
	}
	if err != nil && IsRegistryDNSError(log) {
		b.UI.Failf("%s", RegistryDNSHelp)
	}
	return err
}

// BrowserVariant layers the browser def onto parent as dest.
func (b *Builder) BrowserVariant(push bool, parent, dest, userName, layerDir string, extra ...string) error {
	args := append(append([]string{}, extra...),
		"--build-arg", "BASE_IMAGE="+parent,
		"--build-arg", "USER_NAME="+userName,
		"-f", filepath.Join(layerDir, "Dockerfile"),
		"-t", dest,
		layerDir)
	return b.Build(push, args)
}
