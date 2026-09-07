// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml
package sbx

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Release is the sbx build `proveo init` puts on the host.
const Release = "0.42.0"

// ReleaseRepo is where Docker publishes the builds.
const ReleaseRepo = "docker/sbx-releases"

func ReleaseTag() string { return "v" + Release }

func ReleaseAssetURL(asset string) string {
	return "https://github.com/" + ReleaseRepo + "/releases/download/" + ReleaseTag() + "/" + asset
}

// DefaultPrefix is where the release's own install.sh puts things when nobody
// says otherwise, and proveo does not disagree with it: a per-user prefix
// needs no root, and an operator who wants /usr/local can still say so.
func DefaultPrefix(home string) string {
	if strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".docker", "sbx")
}

// Host is everything asset selection depends on, in one value.
type Host struct {
	OS   string // runtime.GOOS
	Arch string // runtime.GOARCH

	Distro  string // ID
	Like    string // ID_LIKE
	Version string // VERSION_ID
}

func DetectHost() Host {
	h := Host{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if h.OS != "linux" {
		return h
	}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return h
	}
	kv := parseOSRelease(string(b))
	h.Distro, h.Like, h.Version = kv["ID"], kv["ID_LIKE"], kv["VERSION_ID"]
	return h
}

func parseOSRelease(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if unquoted, err := strconv.Unquote(v); err == nil {
			v = unquoted
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}

// Step is one command in an install plan, carrying why it is there so `--print`
// can explain the plan instead of merely listing argv.
type Step struct {
	What string
	Argv []string
	Dir  string   // empty runs in the download directory
	Env  []string // appended to the environment, KEY=VALUE
}

// Plan is how one host gets the pinned sbx.
type Plan struct {
	Asset  string // release asset filename
	URL    string
	Prefix string // where the install lands; empty when the installer owns the location
	Bin    string // the sbx that must exist afterwards; empty when the installer puts it on PATH itself
	Steps  []Step
	Note   string // what the plan cannot do for the operator

	Alt string

	Packaged *Packaged

	Provenance *Provenance
}

// Packaged is an install performed by the host's package manager, from an asset
// on this release.
type Packaged struct {
	Asset string
	URL   string
	Argv  []string // runs in the download directory; carries sudo by design
	Why   string
}

// Provenance locates a published sha256 for a plan's asset.
type Provenance struct {
	Asset   string // the .provenance.json on the same release
	URL     string
	Subject string // the subject inside it whose digest covers Plan.Asset
}

// DigestFromProvenance pulls one subject's sha256 out of an in-toto
// statement.
func DigestFromProvenance(b []byte, subject string) (string, error) {
	var st struct {
		Subject []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return "", fmt.Errorf("provenance: %w", err)
	}
	for _, s := range st.Subject {
		if s.Name != subject {
			continue
		}
		if d := strings.TrimSpace(strings.ToLower(s.Digest["sha256"])); d != "" {
			return d, nil
		}
		return "", fmt.Errorf("provenance subject %q carries no sha256", subject)
	}
	return "", fmt.Errorf("provenance names no subject %q", subject)
}

func linuxProvenance(arch string) *Provenance {
	asset := "DockerSandboxes-linux-" + arch + ".provenance.json"
	return &Provenance{
		Asset:   asset,
		URL:     ReleaseAssetURL(asset),
		Subject: "static/linux/" + arch + "/docker-sbx_" + Release + ".tgz",
	}
}

const (
	assetDarwin  = "DockerSandboxes-darwin.tar.gz"
	assetWindows = "DockerSandboxes.msi"
)

// PlanFor resolves the pinned release into the steps this host needs.
func PlanFor(h Host, prefix, download string) (Plan, error) {
	switch h.OS {
	case "darwin":
		if h.Arch != "arm64" {
			return Plan{}, fmt.Errorf("no Docker Sandboxes build for darwin/%s — it needs Apple silicon (macOS Sonoma 14 or later)", h.Arch)
		}
		return Plan{
			Asset:  assetDarwin,
			URL:    ReleaseAssetURL(assetDarwin),
			Prefix: prefix,
			Bin:    filepath.Join(prefix, "bin", "sbx"),
			Steps: []Step{
				{What: "create the prefix", Argv: []string{"mkdir", "-p", prefix}},
				{What: "unpack bin/, libexec/ and completions/ into the prefix",
					Argv: []string{"tar", "-xzf", filepath.Join(download, assetDarwin), "-C", prefix}},
			},
			Note: "the .dmg on the same release installs the Docker Sandboxes app instead; this tarball is the same CLI and VM assets without it",
			Alt:  "brew trust docker/tap && brew install docker/tap/sbx",
		}, nil

	case "linux":
		arch, ok := linuxArch(h.Arch)
		if !ok {
			return Plan{}, fmt.Errorf("no Docker Sandboxes build for linux/%s (want amd64 or arm64)", h.Arch)
		}
		asset := "DockerSandboxes-linux-" + arch + ".tar.gz"
		extract := filepath.Join(download, "unpacked")
		return Plan{
			Asset:  asset,
			URL:    ReleaseAssetURL(asset),
			Prefix: prefix,
			Bin:    filepath.Join(prefix, "bin", "sbx"),
			Steps: []Step{
				{What: "create a staging directory", Argv: []string{"mkdir", "-p", extract}},
				{What: "unpack the release", Argv: []string{"tar", "-xzf", filepath.Join(download, asset), "-C", extract}},
				{What: "run the release's own installer into the prefix",
					Argv: []string{filepath.Join(extract, "docker-sbx", "install.sh")},
					Env:  []string{"PREFIX=" + prefix}},
			},
			Note:       "needs no root, and installs no SELinux policy — the bundled profile is AppArmor's",
			Alt:        linuxAlt(h),
			Packaged:   linuxPackaged(h, arch),
			Provenance: linuxProvenance(arch),
		}, nil

	case "windows":
		if h.Arch != "amd64" {
			return Plan{}, fmt.Errorf("no Docker Sandboxes build for windows/%s (want amd64)", h.Arch)
		}
		return Plan{
			Asset: assetWindows,
			URL:   ReleaseAssetURL(assetWindows),
			Steps: []Step{
				{What: "install for the current user", Argv: []string{"msiexec.exe", "/i",
					filepath.Join(download, assetWindows), "/quiet"}},
			},
			Note: "the MSI owns its install location and puts sbx on PATH itself; DockerSandboxesMachine.msi on the same release is the machine-wide variant, and needs an elevated shell",
			Alt:  "winget install -h Docker.sbx",
		}, nil
	}
	return Plan{}, fmt.Errorf("no Docker Sandboxes build for %s/%s", h.OS, h.Arch)
}

func linuxArch(arch string) (string, bool) {
	switch arch {
	case "amd64", "arm64":
		return arch, true
	}
	return "", false
}

func linuxPackaged(h Host, arch string) *Packaged {
	family := strings.ToLower(h.Distro + " " + h.Like)
	var asset string
	var argv []string
	switch {
	case strings.Contains(family, "debian") || strings.Contains(family, "ubuntu"):
		asset = "DockerSandboxes-linux-" + arch + "-ubuntu2604.deb"
		if ubuntuBefore2604(h.Version) {
			asset = "DockerSandboxes-linux-" + arch + "-ubuntu2404.deb"
		}
		argv = []string{"sudo", "apt-get", "install", "-y", "./" + asset}
	case strings.Contains(family, "rhel"), strings.Contains(family, "fedora"),
		strings.Contains(family, "centos"), strings.Contains(family, "rocky"),
		strings.Contains(family, "almalinux"):
		asset = "DockerSandboxes-linux-" + arch + "-rockylinux8.rpm"
		argv = []string{"sudo", "dnf", "install", "-y", "./" + asset}
	default:
		return nil
	}
	return &Packaged{
		Asset: asset,
		URL:   ReleaseAssetURL(asset),
		Argv:  argv,
		Why:   "system-wide, with the distro's packaging; needs root, and no published digest covers it",
	}
}

func linuxAlt(h Host) string {
	family := strings.ToLower(h.Distro + " " + h.Like)
	if strings.Contains(family, "debian") || strings.Contains(family, "ubuntu") {
		return "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx"
	}
	return ""
}

func ubuntuBefore2604(version string) bool {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	n, err := strconv.Atoi(major)
	return err == nil && n < 26
}

// Stage says what an unmet prerequisite actually stops.
type Stage string

const (
	BlocksNothing Stage = ""        // worth saying, blocks neither
	BlocksInstall Stage = "install" // do not download; nothing good follows
	BlocksRun     Stage = "run"     // install anyway; the run is what waits
)

// Prereq is one host condition and its verdict.
type Prereq struct {
	Name   string
	OK     bool
	Blocks Stage
	Detail string
	Fix    string
}

// Probe is the host inspection Prereqs performs, injected so every platform's
// checks can be table-tested from any machine.
type Probe struct {
	Exists   func(string) bool
	LookPath func(string) (string, error)
	Groups   func() ([]string, error)
	ReadFile func(string) ([]byte, error)
}

func DefaultProbe() Probe {
	return Probe{
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		LookPath: func(name string) (string, error) {
			return exec.LookPath(name)
		},
		Groups:   currentUserGroups,
		ReadFile: os.ReadFile,
	}
}

func currentUserGroups() ([]string, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	ids, err := u.GroupIds()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, id := range ids {
		if g, err := user.LookupGroupId(id); err == nil {
			names = append(names, g.Name)
		}
	}
	return names, nil
}

// Prereqs is what the host must be before sbx can run an agent at all.
func Prereqs(h Host, p Probe) []Prereq {
	switch h.OS {
	case "linux":
		return linuxPrereqs(p)
	case "darwin":
		return []Prereq{{
			Name:   "Apple silicon",
			OK:     h.Arch == "arm64",
			Blocks: BlocksInstall,
			Detail: "macOS Sonoma 14 or later on Apple silicon",
			Fix:    "there is no Intel build to install",
		}}
	case "windows":
		return nil // virtualisation is `sbx diagnose`'s to report
	}
	return nil
}

// linuxPrereqs covers only what must hold BEFORE sbx exists. Everything about
// whether the host can RUN a sandbox — virtualisation, the daemon, storage,
// permissions, disk — belongs to `sbx diagnose`, which checks all of it and
// explains it better; see Diagnose.
// SPEC: _spec/internal/sbx/host-readiness.puml
func linuxPrereqs(p Probe) []Prereq {
	// install.sh checks for mkfs.ext4 and exits 2 before it copies a file, so
	// this one has to be known before the download.
	_, mkfsErr := p.LookPath("mkfs.ext4")
	out := []Prereq{{
		Name:   "e2fsprogs",
		OK:     mkfsErr == nil,
		Blocks: BlocksInstall,
		Detail: "mkfs.ext4 — the release's installer refuses to run without it",
		Fix:    "sudo dnf install e2fsprogs (or sudo apt install e2fsprogs)",
	}}

	if enforcing, known := selinuxEnforcing(p); known && enforcing {
		out = append(out, Prereq{
			Name:   "SELinux",
			OK:     false,
			Blocks: BlocksNothing,
			Detail: "enforcing — the release ships an AppArmor profile and no SELinux policy; " +
				"measured 2026-09-07, the rockylinux8 package did not resolve it either",
			Fix: "if a sandbox dies instantly: `sudo ausearch -m avc -ts recent` for shim denials, " +
				"and `sudo setenforce 0` briefly to confirm the cause before writing a policy module",
		})
	}
	return out
}

func selinuxEnforcing(p Probe) (enforcing, known bool) {
	b, err := p.ReadFile("/sys/fs/selinux/enforce")
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(b)) == "1", true
}

// Blocking is the unmet subset that stops one stage.
func Blocking(in []Prereq, stage Stage) []Prereq {
	var out []Prereq
	for _, c := range in {
		if !c.OK && c.Blocks == stage {
			out = append(out, c)
		}
	}
	return out
}

// CheckNames lists diagnose rows, for an error that has to fit on one line.
func CheckNames(in []Check) []string {
	var out []string
	for _, c := range in {
		out = append(out, c.Name)
	}
	return out
}

// Names lists the checks, for an error that has to fit on one line.
func Names(in []Prereq) []string {
	var out []string
	for _, c := range in {
		out = append(out, c.Name)
	}
	return out
}

// LoginArgs and SetupArgs are the two commands that finish an install.
func LoginArgs() []string { return []string{"login"} }
func SetupArgs() []string { return []string{"setup"} }

// ServerState is the daemon's own answer to "were you reachable", added in
// 0.42.0 precisely so a script need not parse error text: `server.state` is
// "running" or "unavailable".
func ServerState() (string, error) {
	out, err := sh.VersionJSON()
	if err != nil {
		return "", err
	}
	return ParseServerState(out)
}

// ParseServerState reads the verdict out of a `sbx version --json` payload.
func ParseServerState(out []byte) (string, error) {
	var v struct {
		Server struct {
			State string `json:"state"`
		} `json:"server"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("sbx version --json: %w", err)
	}
	if v.Server.State == "" {
		return "", fmt.Errorf("sbx version --json reported no server state")
	}
	return v.Server.State, nil
}

// VersionJSONArgs is named here so the CLI seam and the advice proveo prints
// cannot drift apart.
func VersionJSONArgs() []string { return []string{"version", "--json"} }

// PolicyInitArgs writes a host-wide network baseline.
func PolicyInitArgs(baseline string) []string {
	return []string{"policy", "init", baseline}
}

// DiagnoseArgs asks sbx to report on the host, machine-readably.
func DiagnoseArgs() []string { return []string{"diagnose", "--json"} }

// Check is one row of `sbx diagnose`.
//
// proveo does not second-guess these. sbx owns the question of whether this
// host can run a sandbox — it knows about virtualisation, its own daemon, its
// own storage and its own version skew — and it answers with a remediation
// proveo would only paraphrase worse.
// SPEC: _spec/internal/sbx/host-readiness.puml
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | fail | skip
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

func (c Check) Failed() bool { return c.Status == "fail" }

// ParseDiagnose reads a `sbx diagnose --json` payload.
func ParseDiagnose(b []byte) ([]Check, error) {
	var out struct {
		Checks []Check `json:"checks"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("sbx diagnose --json: %w", err)
	}
	if len(out.Checks) == 0 {
		return nil, fmt.Errorf("sbx diagnose --json reported no checks")
	}
	return out.Checks, nil
}

// FailedChecks is the subset the operator has to act on.
func FailedChecks(in []Check) []Check {
	var out []Check
	for _, c := range in {
		if c.Failed() {
			out = append(out, c)
		}
	}
	return out
}

// DiagnoseAt runs diagnose against ONE sbx binary, named by path — the prefix
// is not on PATH yet when init asks.
//
// A non-zero exit is expected whenever a check fails, so the output is parsed
// regardless and the error only matters when there is nothing to parse.
func DiagnoseAt(bin string) ([]Check, error) {
	out, err := boundedCombined(bin, DiagnoseArgs()...)
	checks, perr := ParseDiagnose(out)
	if perr == nil {
		return checks, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, perr
}

// VersionAt reads the version of ONE sbx binary, named by path.
func VersionAt(bin string) (string, error) {
	out, err := boundedCombined(bin, "version")
	if err != nil {
		return "", err
	}
	m := verLine.FindStringSubmatch(string(out))
	if m == nil {
		return "", fmt.Errorf("no version in %q", strings.TrimSpace(string(out)))
	}
	return m[1] + "." + m[2] + "." + m[3], nil
}
