// SPEC: _spec/internal/engine/container-engine.puml
//
// SPEC: _spec/internal/engine/container-engine.puml
package engine

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// Kind identifies a container engine implementation.
type Kind string

const (
	Unknown        Kind = "unknown"
	DockerDesktop  Kind = "docker-desktop"
	OrbStack       Kind = "orbstack"
	Colima         Kind = "colima"
	RancherDesktop Kind = "rancher-desktop"
	Podman         Kind = "podman"
	Lima           Kind = "lima"
	DockerEngine   Kind = "docker-engine"
)

var (
	lookPath = exec.LookPath
	goos     = runtime.GOOS
	getenv   = os.Getenv
	output   = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
)

// Info is what proveo knows about the engine backing the docker CLI.
type Info struct {
	Kind     Kind   // classified implementation
	Context  string // active docker context name ("" when DOCKER_HOST wins)
	Endpoint string // resolved docker endpoint URI
	Version  string // engine's own version, else the docker server version
	Running  bool   // daemon answered `docker info`
	HasCLI   bool   // a docker CLI resolved on PATH
}

type profile struct {
	name       string // display name
	start      string // command that brings the engine up
	versionCLI string // engine-owned CLI that reports its own version
	sockets    []string
	contexts   []string
	infoOS     []string // `docker info` .OperatingSystem / .Name markers
}

var profiles = []struct {
	kind Kind
	profile
}{
	{OrbStack, profile{
		name: "OrbStack", start: "open -a OrbStack", versionCLI: "orb",
		sockets:  []string{".orbstack/"},
		contexts: []string{"orbstack"},
		infoOS:   []string{"orbstack"},
	}},
	{DockerDesktop, profile{
		name: "Docker Desktop", start: "open -a Docker",
		sockets:  []string{".docker/run/docker.sock"},
		contexts: []string{"desktop-linux", "desktop-windows"},
		infoOS:   []string{"docker desktop"},
	}},
	{Colima, profile{
		name: "Colima", start: "colima start", versionCLI: "colima",
		sockets:  []string{".colima/"},
		contexts: []string{"colima"},
		infoOS:   []string{"colima"},
	}},
	{RancherDesktop, profile{
		name: "Rancher Desktop", start: "open -a 'Rancher Desktop'", versionCLI: "rdctl",
		sockets:  []string{".rd/"},
		contexts: []string{"rancher-desktop"},
		infoOS:   []string{"rancher desktop"},
	}},
	{Podman, profile{
		name: "Podman", start: "podman machine start", versionCLI: "podman",
		sockets:  []string{"podman"},
		contexts: []string{"podman"},
		infoOS:   []string{"podman"},
	}},
	{Lima, profile{
		name: "Lima", start: "limactl start", versionCLI: "limactl",
		sockets:  []string{".lima/"},
		contexts: []string{"lima"},
		infoOS:   []string{"lima"},
	}},
}

func profileFor(k Kind) profile {
	for _, p := range profiles {
		if p.kind == k {
			return p.profile
		}
	}
	return profile{}
}

func DetectOffline() Info {
	in := Info{Kind: Unknown}
	if _, err := lookPath("docker"); err != nil {
		return in
	}
	in.HasCLI = true
	in.Context, in.Endpoint = activeContext()
	in.Kind = classify(in.Context, in.Endpoint)
	in.Version = engineVersion(in.Kind)
	return in
}

func Detect() Info {
	in := DetectOffline()
	if !in.HasCLI {
		return in
	}
	if osName, daemonName, ver, ok := dockerInfo(); ok {
		in.Running = true
		if in.Version == "" {
			in.Version = ver
		}
		if k := classifyInfo(osName, daemonName); k != Unknown && k != in.Kind {
			in.Kind = k
			if v := engineVersion(in.Kind); v != "" {
				in.Version = v
			}
		}
	}
	if in.Kind == Unknown && in.Running {
		in.Kind = DockerEngine
	}
	return in
}

func activeContext() (name, endpoint string) {
	if h := strings.TrimSpace(getenv("DOCKER_HOST")); h != "" {
		return "", h
	}
	out, err := output("docker", "context", "inspect", "--format", "{{.Name}}\t{{.Endpoints.docker.Host}}")
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(string(out)), ""
	}
	return parts[0], parts[1]
}

func classify(context, endpoint string) Kind {
	ep, ctx := strings.ToLower(endpoint), strings.ToLower(context)
	for _, p := range profiles {
		for _, s := range p.sockets {
			if strings.Contains(ep, s) {
				return p.kind
			}
		}
	}
	for _, p := range profiles {
		for _, c := range p.contexts {
			if strings.HasPrefix(ctx, c) {
				return p.kind
			}
		}
	}
	if goos == "linux" && strings.Contains(ep, "/var/run/docker.sock") {
		return DockerEngine
	}
	return Unknown
}

func classifyInfo(operatingSystem, name string) Kind {
	hay := strings.ToLower(operatingSystem + " " + name)
	for _, p := range profiles {
		for _, m := range p.infoOS {
			if strings.Contains(hay, m) {
				return p.kind
			}
		}
	}
	return Unknown
}

func dockerInfo() (operatingSystem, name, version string, ok bool) {
	out, err := output("docker", "info", "--format", "{{json .}}")
	if err != nil {
		return "", "", "", false
	}
	var d struct {
		Name            string `json:"Name"`
		ServerVersion   string `json:"ServerVersion"`
		OperatingSystem string `json:"OperatingSystem"`
	}
	if err := json.Unmarshal(out, &d); err != nil {
		return "", "", "", false
	}
	return d.OperatingSystem, d.Name, d.ServerVersion, true
}

var semverish = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

func engineVersion(k Kind) string {
	cli := profileFor(k).versionCLI
	if cli == "" {
		return ""
	}
	if _, err := lookPath(cli); err != nil {
		return ""
	}
	out, err := output(cli, "version")
	if err != nil {
		return ""
	}
	return semverish.FindString(string(out))
}

// Name is the engine's display name.
func (i Info) Name() string {
	if n := profileFor(i.Kind).name; n != "" {
		return n
	}
	if !i.HasCLI {
		return "no docker CLI"
	}
	if i.Kind == DockerEngine {
		return "Docker Engine"
	}
	return "unknown engine"
}

// Label renders the engine as "<name> <version>".
func (i Info) Label() string {
	if i.Version == "" {
		return i.Name()
	}
	return i.Name() + " " + i.Version
}

// Isolation describes the boundary this engine gives a workload.
func (i Info) Isolation() string {
	return "shared-kernel; not a microVM boundary"
}

// StartHint is the command that brings this engine up, empty when unknown.
func (i Info) StartHint() string {
	return profileFor(i.Kind).start
}
