// SPEC: _spec/internal/runner/hardened-run-argv.puml
//
// SPEC: _spec/internal/runner/hardened-run-argv.puml
package runner

import "fmt"

// Mount is a bind mount.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

type Config struct {
	Name        string   // container name (optional)
	User        string   // "uid:gid"; empty => runtime default (caller should set)
	Interactive bool     // add -it
	Remove      bool     // add --rm
	Tmpfs       []string // e.g. "/tmp:noexec,nosuid,size=100m"
	Mounts      []Mount
	Env         []string // "KEY=VALUE", or bare "KEY" to forward the client env value (keeps secrets off the argv)
	// SPEC: _spec/internal/secretref/secret-references.puml
	ChildEnv   []string
	Workdir    string   // container working dir (-w), e.g. a monorepo sub-scope
	Entrypoint string   // override the image entrypoint (--entrypoint), e.g. "bash" for --shell
	ExtraArgs  []string // pass-through (e.g. egress agent args, --network)
	Image      string
	Command    []string // args after the image
	PidsLimit  int      // --pids-limit; <=0 => ResolvePidsLimit(DetectHost(), IsBrowserImage(Image), …)
}

var hardeningStatic = []string{
	"--cap-drop=ALL",
	"--security-opt=no-new-privileges:true",
}

func DockerRunArgs(cfg Config) []string {
	args := []string{"run"}
	if cfg.Interactive {
		args = append(args, "-it")
	}
	if cfg.Remove {
		args = append(args, "--rm")
	}
	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}
	args = append(args, Hardening(resolvePids(cfg))...)
	for _, t := range cfg.Tmpfs {
		args = append(args, "--tmpfs", t)
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
	if cfg.Entrypoint != "" {
		args = append(args, "--entrypoint", cfg.Entrypoint)
	}
	args = append(args, cfg.ExtraArgs...)
	if cfg.Image != "" {
		args = append(args, cfg.Image)
	}
	args = append(args, cfg.Command...)
	return args
}

func resolvePids(cfg Config) int {
	if cfg.PidsLimit > 0 {
		return cfg.PidsLimit
	}
	return ResolvePidsLimit(DetectHost(cfg.Image), IsBrowserImage(cfg.Image), 0, false)
}

func Hardening(pids int) []string {
	if pids < 1 {
		pids = ResolvePidsLimit(DetectHost(), false, 0, false)
	}
	out := append([]string(nil), hardeningStatic...)
	return append(out, fmt.Sprintf("--pids-limit=%d", pids))
}
