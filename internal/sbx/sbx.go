// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/_experiments/docker-sandbox.puml
package sbx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Binary is the Docker Sandboxes CLI this package drives.
const Binary = "sbx"

// MinVersion is the oldest sbx whose CLI surface this package targets. proveo
// owns the version rather than leaving it to whatever the operator's package
// manager happens to hold: sbx is pre-GA and its surface moves, so a host that
// is merely "installed" is not a host that can be driven. v0.35 → v0.39 alone
// moved workspaces from `-v host:container` to positional paths, replaced the
// image positional with `--template`, made `rm` refuse to run non-interactively
// without `--force`, and rewrote the Kit schema — every one of which fails at
// run time, deep inside a sandbox the operator cannot see.
const MinVersion = "0.39.0"

// Test seams; production leaves these at their defaults.
var (
	lookPath  = exec.LookPath
	goos      = runtime.GOOS
	goarch    = runtime.GOARCH
	kvmDevice = "/dev/kvm"
	stat      = os.Stat
	runVer    = func() ([]byte, error) { return exec.Command(Binary, "version").Output() }
)

// Available reports whether the host can run the sbx backend, and if not, why.
// A too-old CLI is reported as unavailable rather than tried: falling back to
// docker+egress is a posture the operator can read, whereas a mid-run flag
// rejection is not.
func Available() (bool, string) {
	if _, err := lookPath(Binary); err != nil {
		return false, fmt.Sprintf("%s CLI not found on PATH", Binary)
	}
	switch {
	case goos == "darwin" && goarch == "arm64":
	case goos == "linux":
		if _, err := stat(kvmDevice); err != nil {
			return false, fmt.Sprintf("linux requires KVM (%s unavailable)", kvmDevice)
		}
	default:
		return false, fmt.Sprintf("unsupported platform %s/%s (want darwin/arm64 or linux+KVM)", goos, goarch)
	}
	got, err := Version()
	if err != nil {
		return false, fmt.Sprintf("%s version unreadable: %v", Binary, err)
	}
	if Older(got, MinVersion) {
		return false, fmt.Sprintf("%s %s is older than the %s this build targets", Binary, got, MinVersion)
	}
	return true, ""
}

// verLine matches the CLI's own report: "sbx version: v0.39.0 <sha>".
var verLine = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// Version reports the installed CLI's version, without a leading "v".
func Version() (string, error) {
	out, err := runVer()
	if err != nil {
		return "", err
	}
	m := verLine.FindStringSubmatch(string(out))
	if m == nil {
		return "", fmt.Errorf("no version in %q", strings.TrimSpace(string(out)))
	}
	return m[1] + "." + m[2] + "." + m[3], nil
}

// Older reports whether got precedes want. An unparseable side is treated as
// NOT older, so a version scheme this build has never seen is assumed newer
// rather than blocking a host the operator just upgraded.
func Older(got, want string) bool {
	g, gok := parseVer(got)
	w, wok := parseVer(want)
	if !gok || !wok {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return g[i] < w[i]
		}
	}
	return false
}

func parseVer(s string) ([3]int, bool) {
	m := verLine.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InstallCmd is the shell command that brings sbx to MinVersion or newer:
// installing it when absent, upgrading it when present but too old. Empty on a
// platform with no supported install route.
func InstallCmd(installed bool) string {
	switch goos {
	case "darwin":
		if goarch != "arm64" {
			return ""
		}
		if installed {
			return "brew update && brew upgrade docker/tap/sbx"
		}
		return "brew trust docker/tap && brew install docker/tap/sbx"
	case "windows":
		if installed {
			return "winget upgrade -h Docker.sbx"
		}
		return "winget install -h Docker.sbx"
	case "linux":
		if installed {
			return "sudo apt update && sudo apt install --only-upgrade docker-sbx"
		}
		return "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx"
	default:
		return ""
	}
}

// Installed reports whether the CLI is on PATH at all, which is what decides
// between an install and an upgrade.
func Installed() bool {
	_, err := lookPath(Binary)
	return err == nil
}

// InstallHint is the platform's install line for the sbx CLI.
func InstallHint() string {
	switch goos {
	case "darwin":
		if goarch != "arm64" {
			return ""
		}
		return "brew trust docker/tap && brew install docker/tap/sbx && sbx login"
	case "windows":
		return "winget install -h Docker.sbx && sbx login"
	case "linux":
		return "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx && sbx login"
	default:
		return ""
	}
}

// TemplateLoadArgs builds the argv that reads a `docker save` stream into the
// sandbox runtime's own image store.
func TemplateLoadArgs() []string { return []string{"template", "load"} }

// TemplateListArgs builds the argv that lists the images already in that store.
func TemplateListArgs() []string { return []string{"template", "ls"} }

// Test seams for the template store.
var (
	templateList = func() ([]byte, error) {
		return exec.Command(Binary, TemplateListArgs()...).Output()
	}
	templateLoad = func(image string) error {
		save := exec.Command("docker", "save", image)
		load := exec.Command(Binary, TemplateLoadArgs()...)
		pipe, err := save.StdoutPipe()
		if err != nil {
			return err
		}
		load.Stdin = pipe
		load.Stdout, load.Stderr = os.Stderr, os.Stderr
		save.Stderr = os.Stderr
		if err := load.Start(); err != nil {
			return err
		}
		if err := save.Run(); err != nil {
			_ = load.Wait()
			return fmt.Errorf("docker save %s: %w", image, err)
		}
		return load.Wait()
	}
)

// HasTemplate reports whether image is already in the sandbox runtime's store.
func HasTemplate(image string) bool {
	out, err := templateList()
	if err != nil {
		return false
	}
	// Match the repository half too: the store may print a digest or a resolved
	// tag rather than the reference it was loaded under.
	repo := image
	if i := strings.LastIndex(image, ":"); i > 0 {
		repo = image[:i]
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, image) || strings.Contains(line, repo) {
			return true
		}
	}
	return false
}

// EnsureTemplate puts image into the sandbox runtime's image store, which is
// SEPARATE from the host engine's: `sbx template ls` is empty on a machine whose
// docker holds every proveo image, so a Kit naming one would resolve to nothing.
//
// The transfer is local and per-user by design — `docker save | sbx template
// load` over a pipe, no registry. Each operator has their own docker login and
// their own sbx config, so a shared registry would make a per-user concern into
// an authenticated remote one, and would publish proveo's images somewhere they
// do not need to be.
//
// Already-present is not re-loaded: the images are multi-GB and a run must not
// pay that twice.
func EnsureTemplate(image string, report func(string, ...any)) error {
	if image == "" || HasTemplate(image) {
		return nil
	}
	if report != nil {
		report("loading %s into the sandbox image store (local, per-user)", image)
	}
	if err := templateLoad(image); err != nil {
		return fmt.Errorf("sbx template load %s: %w", image, err)
	}
	return nil
}

// Mount is a workspace bind into the sandbox VM.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// RunConfig describes one agent run on the sbx backend.
type RunConfig struct {
	Name    string   // sandbox/session name; empty lets sbx assign one
	KitDir  string   // directory holding the rendered Kit spec.yaml
	Image   string   // agent image/template reference
	Mounts  []Mount  // workspace + home binds
	Env     []string // non-secret KEY=VALUE passthrough
	Workdir string   // working directory inside the sandbox
	Command []string // trailing agent command (after "--")
}

// RunArgs builds the sbx invocation for cfg.
func RunArgs(cfg RunConfig) []string {
	args := []string{"run"}
	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}
	if cfg.KitDir != "" {
		args = append(args, "--kit", cfg.KitDir)
	}
	for _, m := range cfg.Mounts {
		spec := m.Host + ":" + m.Container
		if m.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}
	for _, e := range cfg.Env {
		args = append(args, "-e", e)
	}
	if cfg.Workdir != "" {
		args = append(args, "-w", cfg.Workdir)
	}
	if cfg.Image != "" {
		args = append(args, cfg.Image)
	}
	if len(cfg.Command) > 0 {
		args = append(args, "--")
		args = append(args, cfg.Command...)
	}
	return args
}

// RemoveArgs builds the ephemeral teardown invocation (VM + images + volumes).
func RemoveArgs(name string) []string {
	return []string{"rm", name}
}

// SecretSetArgs builds the credential-injection argv.
func SecretSetArgs(name string) []string {
	return []string{"secret", "set", name}
}

// secretSet is overridable in tests.
var secretSet = func(name, value string) error {
	cmd := exec.Command(Binary, SecretSetArgs(name)...)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// SecretSet injects one credential host-side.
func SecretSet(name, value string) error {
	return secretSet(name, value)
}

// Kit is the posture rendered as a Kit spec.yaml.
type Kit struct {
	Name           string   `yaml:"name"`
	Image          string   `yaml:"image"`
	Network        KitNet   `yaml:"network"`
	CredentialsEnv []string `yaml:"credentialsEnv"`
}

// KitNet carries the allowlist.
type KitNet struct {
	AllowedDomains []string `yaml:"allowedDomains,omitempty"`
}

// WriteKit renders k into dir/spec.yaml and returns dir.
func WriteKit(dir string, k Kit) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(k); err != nil {
		return "", fmt.Errorf("sbx kit encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("sbx kit encode: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sbx kit dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("sbx kit write: %w", err)
	}
	return dir, nil
}
