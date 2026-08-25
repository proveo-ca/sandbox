// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/_experiments/docker-sandbox.puml
package sbx

import (
	"bytes"
	"encoding/json"
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
	// dockerMemTotal reports what the daemon can actually hand a container. On
	// Linux that is host memory; on macOS and Windows it is the VM's share of it,
	// which is the number that matters and the one sbx cannot see.
	dockerMemTotal = func() ([]byte, error) {
		return exec.Command("docker", "info", "--format", "{{.MemTotal}}").Output()
	}
)

// Sandbox memory bounds, in bytes. The ceiling matches sbx's own cap; the floor is
// the point below which a limit says more about a broken daemon than about policy,
// so sbx's default is left to apply instead.
const (
	maxSandboxMemory = 32 << 30
	minSandboxMemory = 1 << 30
)

// MemoryLimit returns the -m value for a sandbox run, or "" to leave sbx's default
// in place.
//
// sbx defaults a sandbox to 50% of HOST memory. Wherever Docker runs in a VM — every
// macOS and Windows host — that number can exceed the VM's entire allocation, and a
// limit larger than the machine it sits in cannot bind. The container then grows past
// what the VM has, the VM's OOM killer takes a process, and the run dies with SIGKILL
// (137) carrying no OOMKilled flag and no message: the failure is invisible in exactly
// the place an operator would look for it.
//
// Deriving the same 50% from the daemon's own MemTotal is correct on every platform,
// because on native Linux MemTotal IS host memory and the result is unchanged.
func MemoryLimit() string {
	out, err := dockerMemTotal()
	if err != nil {
		return ""
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || total <= 0 {
		return ""
	}
	limit := total / 2
	if limit > maxSandboxMemory {
		limit = maxSandboxMemory
	}
	if limit < minSandboxMemory {
		return ""
	}
	return strconv.FormatInt(limit/(1<<20), 10) + "m"
}

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

// TemplateLoadArgs builds the argv that reads a `docker save` TAR into the
// sandbox runtime's own image store. The file is appended by the caller.
func TemplateLoadArgs() []string { return []string{"template", "load"} }

// TemplateListArgs builds the argv that lists the images already in that store.
func TemplateListArgs() []string { return []string{"template", "ls"} }

// Test seams for the template store.
var (
	templateList = func() ([]byte, error) {
		return exec.Command(Binary, TemplateListArgs()...).Output()
	}
	// templateLoad crosses the two image stores through a TAR ON DISK, because
	// `sbx template load` takes a FILE — it does not read stdin, so the obvious
	// `docker save | sbx template load` pipe fails with "requires at least 1 and
	// at most 2 arguments" before a byte moves.
	//
	// The tar is the size of the image (multi-GB for the browser variants), so it
	// lands in a temp dir that is removed whether the load succeeds or not: a
	// failed run must not leave a copy of every harness image on the host.
	templateRemove = func(image string) error {
		return exec.Command(Binary, TemplateRemoveArgs(image)...).Run()
	}
	templateLoad = func(image string) error {
		dir, err := os.MkdirTemp("", "proveo-sbx-template-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		tar := filepath.Join(dir, "image.tar")

		save := exec.Command("docker", "save", "-o", tar, image)
		save.Stdout, save.Stderr = os.Stderr, os.Stderr
		if err := save.Run(); err != nil {
			return fmt.Errorf("docker save %s: %w", image, err)
		}
		load := exec.Command(Binary, append(TemplateLoadArgs(), tar)...)
		load.Stdout, load.Stderr = os.Stderr, os.Stderr
		return load.Run()
	}
)

// localImageID is the host engine's ID for an image, shortened to the width the
// sandbox store prints. Overridable in tests.
var localImageID = func(image string) string {
	out, err := exec.Command("docker", "image", "inspect", image, "--format", "{{.Id}}").Output()
	if err != nil {
		return ""
	}
	id := strings.TrimPrefix(strings.TrimSpace(string(out)), "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

// HasTemplate reports whether the sandbox runtime's store holds the SAME image
// the host engine currently has under that reference.
//
// The store prints COLUMNS, not references, and qualifies the repository with a
// registry:
//
//	REPOSITORY                     TAG      IMAGE ID       FLAVOR   CREATED
//	docker.io/proveo/egress-proxy  latest   4ee370d17e72             …
//
// So a substring test for "proveo/egress-proxy:latest" never matches, and one for
// the bare repository matches any tag. Repository and tag are compared
// separately, with the registry qualifier trimmed.
//
// **Identity, not just presence.** Matching repo+tag alone treats a REBUILT
// :latest as already loaded, so the sandbox keeps running the image the store
// happened to receive first — silently, and forever. That is not hypothetical: a
// standardisation of the workspace layout rebuilt these images, and a sandbox
// started afterwards had no /app at all because the store still held the
// pre-rebuild copy.
//
// **Identity is read from a RECEIPT, not from the store's IMAGE ID column**, and
// that distinction is the whole reason this function is not two lines. `sbx
// create` re-bakes the template it was given and rewrites the tag to point at the
// baked result, so the ID under a reference stops being the ID that was loaded
// the moment a sandbox starts — measured: load puts 2ca281d785dc under
// proveo/claudecode:latest, one `sbx create` later the same row reads
// 5fcb2266417f. Comparing against that column therefore mismatches forever after
// the first run and re-loads a multi-GB tar EVERY time, which is worse than the
// staleness it was written to prevent. The receipt records what proveo handed
// over, so a rebuild still reloads and a bake does not.
//
// When the host engine cannot name the image (not built locally), identity is
// unknowable and presence is the best available answer — that path is for an
// image pulled straight into the store, which no rebuild invalidates.
func HasTemplate(image string) bool {
	out, err := templateList()
	if err != nil {
		return false // unreadable store: load again rather than run on nothing
	}
	if _, ok := templateRow(string(out), image); !ok {
		return false
	}
	wantID := localImageID(image)
	if wantID == "" {
		return true // cannot compare identity; presence is all there is
	}
	return readTemplateReceipt(image) == wantID
}

// templateReceiptDir holds one small file per loaded reference, naming the host
// image ID that was handed to the store. Overridable so tests do not touch a real
// user cache.
var templateReceiptDir = func() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "proveo", "sbx-templates")
}

// receiptFile is the receipt path for image, with the characters that cannot
// appear in a filename folded away. Collisions across references are not a
// correctness risk: a wrong receipt reloads an image that was already current,
// which costs time and nothing else.
func receiptFile(image string) string {
	dir := templateReceiptDir()
	if dir == "" {
		return ""
	}
	name := strings.NewReplacer("/", "_", ":", "_").Replace(image)
	return filepath.Join(dir, name)
}

func readTemplateReceipt(image string) string {
	f := receiptFile(image)
	if f == "" {
		return ""
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "" // no receipt: treat as not-current and load
	}
	return strings.TrimSpace(string(b))
}

// writeTemplateReceipt records a completed load. A receipt that cannot be written
// is not an error: the next run reloads, which is correct, just slower.
func writeTemplateReceipt(image string) {
	f := receiptFile(image)
	id := localImageID(image)
	if f == "" || id == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(f), 0o755) != nil {
		return
	}
	_ = os.WriteFile(f, []byte(id), 0o644)
}

// templateRow finds image's row in `sbx template ls` output and returns the id the
// store prints for it.
func templateRow(out, image string) (id string, ok bool) {
	wantRepo, wantTag := splitRef(image)
	for _, f := range templateRows(out) {
		if trimRegistry(f[0]) == wantRepo && f[1] == wantTag {
			if len(f) > 2 {
				return f[2], true
			}
			return "", true
		}
	}
	return "", false
}

// storeTemplateID is the id the sandbox store prints for image, or "" when it holds
// no such image.
//
// This is trustworthy ONLY immediately after a load. `sbx create` re-bakes the
// template and rewrites that column, so comparing it at check time would reload a
// multi-GB tar on every launch forever after — see TestHasTemplateReloadsARebuiltImage.
// At load time, before any sandbox exists, the store prints exactly what it was
// handed, which is what makes the post-load verification below possible at all.
func storeTemplateID(image string) string {
	out, err := templateList()
	if err != nil {
		return ""
	}
	id, _ := templateRow(string(out), image)
	return id
}

// confirmLoaded reports whether the store actually took the image it was handed. A
// receipt written without this outlives the load that failed to land: HasTemplate
// then reads a receipt naming the new id over a store still holding the old content,
// and skips the reload forever. Silent, self-perpetuating, and it cost an afternoon
// of chasing a stale entrypoint that no rebuild could dislodge.
func confirmLoaded(image string) error {
	want, got := localImageID(image), storeTemplateID(image)
	if want == "" || got == "" || got == want {
		return nil // nothing to compare against: presence is all there is
	}
	return fmt.Errorf("sbx kept %s at %s after loading %s: the store did not take the image "+
		"(retry, or `sbx template rm %s` first)", image, got, want, image)
}

// templateRows splits `sbx template ls` output into whitespace-separated fields
// per row, skipping the header.
func templateRows(out string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "REPOSITORY" {
			continue
		}
		rows = append(rows, f)
	}
	return rows
}

// splitRef splits an image reference into repository and tag, defaulting the tag
// the way docker does.
func splitRef(image string) (repo, tag string) {
	repo, tag = image, "latest"
	if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i+1:], "/") {
		repo, tag = image[:i], image[i+1:]
	}
	return trimRegistry(repo), tag
}

// trimRegistry drops a leading registry host so a locally-built "proveo/x" and
// the store's "docker.io/proveo/x" compare equal.
func trimRegistry(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		return strings.Join(parts[1:], "/")
	}
	return repo
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
// TemplateRemoveArgs builds the argv that drops an image from the sandbox store.
// Removing before loading is deterministic where overwriting is merely expected.
func TemplateRemoveArgs(image string) []string { return []string{"template", "rm", image} }

// ForceReload reports whether the operator has asked for the template to be handed
// over again regardless of receipts — the escape hatch for a store that has gone out
// of sync in a way proveo cannot see.
func ForceReload(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_SBX_RELOAD"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func EnsureTemplate(image string, report func(string, ...any)) error {
	if image == "" {
		return nil
	}
	if ForceReload(os.Getenv) {
		if report != nil {
			report("PROVEO_SBX_RELOAD=1 — dropping %s from the sandbox image store first", image)
		}
		_ = templateRemove(image)
	} else if HasTemplate(image) {
		return nil
	}
	if report != nil {
		report("loading %s into the sandbox image store (local, per-user)", image)
	}
	if err := templateLoad(image); err != nil {
		return fmt.Errorf("sbx template load %s: %w", image, err)
	}
	if err := confirmLoaded(image); err != nil {
		return err // no receipt: the next run retries rather than skipping forever
	}
	writeTemplateReceipt(image)
	return nil
}

// ReloadTemplate hands the image over again, receipt or no receipt. It exists
// for the repair path: see StartFailure.
func ReloadTemplate(image string, report func(string, ...any)) error {
	if image == "" {
		return nil
	}
	if report != nil {
		report("reloading %s into the sandbox image store", image)
	}
	if err := templateLoad(image); err != nil {
		return fmt.Errorf("sbx template load %s: %w", image, err)
	}
	if err := confirmLoaded(image); err != nil {
		return err
	}
	writeTemplateReceipt(image)
	return nil
}

// Exists reports whether sbx currently holds a sandbox by this name. It is the
// signal that separates a sandbox which never STARTED from an agent that ran and
// exited non-zero, and only the former may be retried — retrying the latter would
// run the agent a second time.
//
// The distinction cannot be read off sbx's output: it prints "ERROR: failed to
// create sandbox: failed to run sandbox container" on STDOUT, and stdout is the
// agent's terminal. Wrapping it to sniff for that text would leave sbx writing to
// something that is not an *os.File, which is exactly what its own PTY checks
// look for. Asking what exists afterwards costs one cheap `sbx ls` and stays out
// of the terminal's way.
//
// This matters because sbx's stored template can go bad on its own: `sbx create`
// re-bakes the template it was handed, and a baked cursor template reached a
// state where EVERY create failed with "failed to run sandbox container" —
// including argv that had worked minutes earlier, with `sbx diagnose` reporting
// 12/12 healthy. Loading the image again repaired it immediately. Before the load
// was made conditional this never surfaced, because proveo re-loaded the template
// on every run and overwrote each bad bake before anything could run on it.
func Exists(name string) bool {
	if name == "" {
		return false
	}
	out, err := sandboxList()
	if err != nil {
		// Unreadable listing: claim it exists, so a retry that might double-run an
		// agent is never taken on a guess.
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range strings.Fields(line) {
			if f == name {
				return true
			}
		}
	}
	return false
}

// sandboxList reads the sandbox listing. Overridable in tests.
var sandboxList = func() ([]byte, error) {
	return exec.Command(Binary, "ls").CombinedOutput()
}

// imageEntrypoint reads the image's own ENTRYPOINT. Overridable in tests.
var imageEntrypoint = func(image string) []string {
	out, err := exec.Command("docker", "image", "inspect", image,
		"--format", "{{json .Config.Entrypoint}}").Output()
	if err != nil {
		return nil
	}
	var ep []string
	if err := json.Unmarshal(bytes.TrimSpace(out), &ep); err != nil {
		return nil
	}
	return ep
}

// ImageEntrypoint is what the Kit must run to reproduce a `docker run` of this
// image. It is READ FROM THE IMAGE rather than restated per def, because the two
// would drift the moment a def changed its entrypoint and nothing would notice
// until a sandbox ran the wrong command.
//
// It matters at all because sbx's own agent command would otherwise replace it —
// and proveo's entrypoint is where the seeded guide file, the model-alias
// bridging, the LSP wiring and the subagent set come from. Losing it would leave
// the image in place and the harness gone.
func ImageEntrypoint(image string) []string { return imageEntrypoint(image) }

// Mount is a workspace bind into the sandbox VM.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// BuiltinAgents are the agent names sbx defines itself. A kind: sandbox Kit
// DECLARES an agent, and sbx refuses to let a declaration shadow one of these —
// `agent "cursor" is already registered (built-in agents cannot be overridden by
// a kit)`. Recorded here for the test that proves proveo never lands on one;
// nothing reads it at run time, because AgentName dodges the whole set.
var BuiltinAgents = []string{
	"claude", "codex", "copilot", "cursor", "docker-agent",
	"droid", "gemini", "kiro", "opencode", "shell",
}

// AgentName is the agent a target's Kit declares, and it is namespaced rather
// than checked against BuiltinAgents: sbx adds built-ins over time, so a
// collision test against today's list would pass until the day sbx ships an
// agent named like one of ours and every run of it starts failing. The prefix
// cannot collide by construction.
//
// The failure it avoids is quiet: cursor's runs died ~12s in with the session
// gone before the shell and nothing captured, which read as a sandbox timeout
// for as long as the error stayed unread.
func AgentName(target string) string { return "proveo-" + target }

// RunConfig describes one agent run on the sbx backend.
type RunConfig struct {
	Name string // sandbox/session name; empty lets sbx assign one
	// Agent is sbx's own agent name (claude · cursor · shell · …) and it is
	// MANDATORY: the first positional is parsed as an agent, so leaving it empty
	// makes sbx read the first workspace path as an agent name and refuse the run
	// with "is not a sandbox or known agent".
	Agent   string
	KitDir  string   // directory holding the rendered Kit spec.yaml
	Image   string   // template image, passed as -t
	Memory  string   // -m limit; empty leaves sbx's host-derived default in place
	Mounts  []Mount  // workspace binds, passed POSITIONALLY
	Env     []string // non-secret KEY=VALUE (or bare NAME) passthrough
	Command []string // trailing agent command (after "--")
}

// RunArgs builds the sbx invocation for cfg:
//
//	sbx run [flags] AGENT [PATH[:ro]...] [-- AGENT_ARGS...]
//
// The shape is not docker's, and the differences are the ones that made a
// docker-shaped argv fail one flag at a time:
//
//	workspaces  POSITIONAL paths, not -v. `--volume` exists only with --cloud, and
//	            a local sandbox mounts each path at its own HOST path over
//	            virtiofs — there is no container-side target to name.
//	image       -t/--template. The first positional is the AGENT, so passing an
//	            image reference there is read as an unknown agent name.
//	workdir     no such flag. The cwd is the first workspace; PROVEO_WORKDIR in the
//	            environment is how a harness is told where it landed.
//
// Flags are emitted before the positionals: the usage line puts them there, and
// an interspersed parse is not something to rely on from a pre-GA CLI.
func RunArgs(cfg RunConfig) []string {
	args := []string{"run"}
	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}
	if cfg.KitDir != "" {
		args = append(args, "--kit", cfg.KitDir)
	}
	if cfg.Image != "" {
		args = append(args, "-t", cfg.Image)
	}
	if cfg.Memory != "" {
		args = append(args, "-m", cfg.Memory)
	}
	for _, e := range cfg.Env {
		args = append(args, "-e", e)
	}
	if cfg.Agent != "" {
		args = append(args, cfg.Agent)
	}
	for _, m := range cfg.Mounts {
		spec := m.Host
		if m.ReadOnly {
			spec += ":ro"
		}
		args = append(args, spec)
	}
	if len(cfg.Command) > 0 {
		args = append(args, "--")
		args = append(args, cfg.Command...)
	}
	return args
}

// RemoveArgs builds the ephemeral teardown invocation (VM + images + volumes).
//
// --force is not optional for a script: `sbx rm` asks for confirmation and would
// otherwise block on a prompt no run can answer, and it also declines to remove a
// sandbox still considered in use.
func RemoveArgs(name string) []string {
	return []string{"rm", "--force", name}
}

// NotFound reports whether output from `sbx rm` means the sandbox was never
// there. A run whose `sbx run` failed before creating anything hits this on the
// way out, and warning about a failed teardown then points the operator at the
// wrong thing entirely.
func NotFound(out string) bool {
	return strings.Contains(strings.ToLower(out), "not found")
}

// SecretSetArgs builds the credential-injection argv.
//
// --force is what makes the write non-interactive on a SECOND run. `sbx secret
// set` reads the value from stdin, which works once; when the secret already
// exists it first asks "Overwrite? (y/N)", and the piped value answers THAT
// prompt instead — so the write is cancelled and the agent silently starts with
// the credential it had before. Every run after the first hit this.
//
// The value still travels on stdin. --token would pass it as an argument, which
// the credential boundary forbids: a secret on an argv is visible in `ps` and in
// shell history (_spec/_paradigms/credential-boundary.puml).
func SecretSetArgs(name string) []string {
	return []string{"secret", "set", "--force", name}
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

// KitSchemaVersion is the kit-spec version this package writes.
//
// v2 is the current one, and the version is not cosmetic — it selects the SHAPE
// of two blocks. Under v1 the allowlist is "network.allowedDomains" (which v0.39
// still accepts, with a deprecation warning) and "sandbox.entrypoint" is a
// mapping; under v2 the allowlist moves to "permissions.network.allow" and the
// entrypoint becomes a plain list. Mixing them fails validation rather than
// degrading, so the version and the field names travel together.
const KitSchemaVersion = 2

// Kit is the posture rendered as a Kit spec.yaml (kit-spec v2).
//
// It is a MIXIN, not a sandbox. A `kind: sandbox` Kit declares an agent, and sbx's
// agent list is closed: an agent it does not already ship gets no artifact, so its
// binding gate is skipped and the interactive session is dropped seconds in. A
// mixin declares no agent, composes onto one sbx already knows, and contributes
// only what proveo actually owns — reachability, resolved env, and the seed step.
//
// SchemaVersion is a STRING because the spec says so (SPEC-v2.md). Credentials are
// deliberately absent: the built-in agent declares its own, and a mixin repeating a
// service is rejected outright ("defined in both").
type Kit struct {
	SchemaVersion string         `yaml:"schemaVersion"`
	Kind          string         `yaml:"kind"`
	Name          string         `yaml:"name"`
	DisplayName   string         `yaml:"displayName,omitempty"`
	Description   string         `yaml:"description,omitempty"`
	Permissions   KitPermissions `yaml:"permissions,omitempty"`
	Environment   *KitEnv        `yaml:"environment,omitempty"`
	Setup         *KitSetup      `yaml:"setup,omitempty"`
}

// KitEnv carries values RESOLVED ON THE HOST. A setup command runs in its own
// process and cannot export into the agent, so anything env-shaped has to arrive
// already decided rather than as work for a script to redo inside.
type KitEnv struct {
	Variables map[string]string `yaml:"variables,omitempty"`
}

// KitSetup holds the container-side steps. Only file-shaped work belongs here:
// files outlive the process that wrote them, exports do not.
type KitSetup struct {
	Startup []KitCommand `yaml:"startup,omitempty"`
}

// KitCommand is one setup step. `startup` takes a LIST command (install takes a
// string) — the two spellings differ and the loader is strict about it.
type KitCommand struct {
	Command     []string `yaml:"command"`
	User        string   `yaml:"user,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

// KitSchemaVersionV2 is the schemaVersion every rendered Kit declares.
const KitSchemaVersionV2 = "2"

// SeedCommand is the seed step as a Kit setup.startup entry. Both backends reach
// the same function; only the invocation differs.
func SeedCommand(target string) KitCommand {
	return KitCommand{
		Command:     []string{"/usr/local/bin/proveo-seed", target},
		User:        "1000",
		Description: "proveo: compose subagents, settings and workspace trust",
	}
}

// KitSandbox names the image and what runs in it. Entrypoint is what keeps
// proveo's harness layer — the seeded guide file, the model bridging, the LSP and
// subagent wiring — instead of sbx's own agent command replacing it.
type KitSandbox struct {
	Image      string   `yaml:"image"`
	Entrypoint []string `yaml:"entrypoint,omitempty"`
}

// KitPermissions carries the network policy.
type KitPermissions struct {
	Network KitNet `yaml:"network,omitempty"`
}

// KitNet is the egress allowlist, declared rather than enforced by a sidecar.
type KitNet struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// KitCredential is one provider's brokered credential. This is proveo's broker
// expressed declaratively: proxyManaged keeps the value out of the VM entirely,
// and inject names the destination and header it may be attached to — the same
// on-route/off-route rule internal/broker implements for the docker path.
type KitCredential struct {
	Service string    `yaml:"service"`
	APIKey  KitAPIKey `yaml:"apiKey"`
}

type KitAPIKey struct {
	Name         string      `yaml:"name"`
	ProxyManaged bool        `yaml:"proxyManaged"`
	Inject       []KitInject `yaml:"inject,omitempty"`
}

type KitInject struct {
	Domain string `yaml:"domain"`
	Header string `yaml:"header"`
	Format string `yaml:"format,omitempty"`
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
