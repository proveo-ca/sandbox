// SPEC: _spec/internal/egress/teardown-and-signals.puml,
// _spec/internal/egress/teardown-and-signals.puml Package dockeregress is the
// docker+egress backend: Assemble PLANS a run and Exec EXECUTES one — the same
// split as internal/backend/sandbox, which is what lets cmd/proveo SELECT a
// backend instead of branching on a bool that six other places also read.
//
// SPEC: _spec/internal/egress/teardown-and-signals.puml, _spec/internal/egress/teardown-and-signals.puml
package dockeregress

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/reviewgate"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/ui"
)

func ReviewSupported(getenv func(string) string) (ok bool, why string) {
	if runtime.GOOS != "linux" {
		return false, "linux only"
	}
	if h := strings.TrimSpace(getenv("DOCKER_HOST")); h != "" && !strings.HasPrefix(h, "unix://") {
		return false, "needs a local docker daemon"
	}
	return true, ""
}

func NeedsLifecycle(p egress.Plan) bool {
	return len(p.Networks) > 0 || len(p.Sidecars) > 0
}

// Input is the fully-resolved, side-effect-free input to Assemble.
type Input struct {
	Target, Image, AuthVar              string
	Mode, Credentials                   string
	LocalModel, DataDir                 string
	Shell                               bool
	Extra                               []string
	Sid, EgDir                          string
	UID, GID                            string
	ModelsDir, BrokerFile               string
	HostOllama, OllamaGPU               bool
	HostBridge                          bool // a `proveo run` relay is listening on the host (Claude in Chrome bridge)
	Mounts                              []runner.Mount
	Workdir                             string
	Env                                 []string // declared env var names to forward (bare -e)
	ChildEnv                            []string // "KEY=VALUE" for the docker process, never the argv
	ProviderDomains                     string
	SquidImage, ProxyImage, OllamaImage string
	PidsLimit                           int      // host/tier-resolved --pids-limit
	ReviewSocket                        string   // review tier: host path of the consent gate socket
	Providers                           []string // every provider the broker holds a route for
	WriteHosts                          []string // endpoints of every provider the allowlist admits
	ProviderHosts                       []string // endpoints the DLP treats as on-provider
}

func Assemble(in Input) (egress.Plan, runner.Config, error) {
	plan, err := egress.BuildPlan(egress.Options{
		Mode: in.Mode, Credentials: in.Credentials,
		SessionID: in.Sid, AgentName: in.Target, UID: in.UID, GID: in.GID,
		LocalModel: in.LocalModel, ModelsDir: in.ModelsDir, Providers: in.Providers, BrokerEnvFile: in.BrokerFile,
		HostOllama: in.HostOllama, OllamaGPU: in.OllamaGPU,
		HostBridge:      in.HostBridge,
		ProviderDomains: in.ProviderDomains,
		ReviewSocket:    in.ReviewSocket,
		AuthVar:         in.AuthVar,
		WriteHosts:      in.WriteHosts,
		ProviderHosts:   in.ProviderHosts,
		ConfDir:         filepath.Join(in.EgDir, "mitmproxy", "confdir"),
		FlowsDir:        filepath.Join(in.EgDir, "mitmproxy", "flows"),
		SquidConfigDir:  filepath.Join(in.EgDir, "squid", "config"),
		SquidLogDir:     filepath.Join(in.EgDir, "squid", "logs"),
		SquidImage:      in.SquidImage, ProxyImage: in.ProxyImage, OllamaImage: in.OllamaImage,
	})
	if err != nil {
		return egress.Plan{}, runner.Config{}, err
	}
	agent := runner.Config{
		Interactive: true, Remove: true, User: in.UID + ":" + in.GID,
		Mounts:    in.Mounts,
		Workdir:   in.Workdir,
		Env:       in.Env,
		ChildEnv:  in.ChildEnv,
		ExtraArgs: plan.AgentArgs, Image: in.Image, Command: in.Extra,
		PidsLimit: in.PidsLimit,
	}
	if in.DataDir != "" {
		agent.Mounts = append(agent.Mounts, runner.Mount{Host: in.DataDir, Container: "/workspace/data", ReadOnly: true})
	}
	if in.Shell {
		agent.Entrypoint = "bash" // open a shell instead of launching the agent
	}
	return plan, agent, nil
}

func captureSidecarLogs(r egress.ExecRunner, egDir string, plan egress.Plan) {
	for name, file := range map[string]string{
		plan.ProxyContainer:  "inspector.log",
		plan.SquidContainer:  "squid.log",
		plan.OllamaContainer: "ollama.log",
	} {
		if strings.TrimSpace(name) == "" {
			continue
		}
		out, err := exec.Command("docker", "logs", name).CombinedOutput()
		if err != nil && len(out) == 0 {
			continue
		}
		_ = os.WriteFile(filepath.Join(egDir, file), out, 0o600)
	}
}

func Exec(cfgFS fs.FS, plan egress.Plan, agent runner.Config, egDir string, providers []string, reviewProxy *ptyproxy.Proxy) error {
	r := egress.ExecRunner{Stderr: true}
	rq := egress.ExecRunner{}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			captureSidecarLogs(rq, egDir, plan)
			plan.Teardown(rq)
			_ = os.RemoveAll(filepath.Join(egDir, "inject"))
		})
	}
	defer cleanup()
	stopSig := OnSignalCleanup(cleanup)
	defer stopSig()

	if plan.UsesSquid {
		squidCfg := filepath.Join(egDir, "squid", "config")
		if err := egress.StageSquidConfig(cfgFS, squidCfg, providers, os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS")); err != nil {
			return err
		}
		logs := filepath.Join(egDir, "squid", "logs")
		if err := os.MkdirAll(logs, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(logs, 0o777); err != nil {
			return err
		}
	}
	if plan.CAWaitPath != "" {
		for _, d := range []string{filepath.Join(egDir, "mitmproxy", "confdir"), filepath.Join(egDir, "mitmproxy", "flows")} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
	}

	if err := plan.Apply(r); err != nil {
		return err
	}
	if plan.SquidContainer != "" {
		if err := egress.WaitSquidReady(rq, plan.SquidContainer, 30*time.Second); err != nil {
			return fmt.Errorf("squid upstream not ready: %w", err)
		}
	}
	if plan.CAWaitPath != "" {
		if err := waitForFile(plan.CAWaitPath, 20*time.Second); err != nil {
			return fmt.Errorf("inspector CA not ready: %w", err)
		}
	}
	if plan.OllamaContainer != "" {
		if err := egress.WaitOllamaReady(rq, plan.OllamaContainer, 60*time.Second); err != nil {
			return fmt.Errorf("ollama sidecar not ready: %w", err)
		}
	}
	return ExecAgentWithProxy(agent, reviewProxy)
}

func ExecAgentWithProxy(agent runner.Config, proxy *ptyproxy.Proxy) error {
	c := exec.Command("docker", runner.DockerRunArgs(agent)...)
	if len(agent.ChildEnv) > 0 {
		c.Env = append(os.Environ(), agent.ChildEnv...)
	}
	var err error
	if proxy != nil {
		err = proxy.Run(c)
	} else {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = c.Run()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return backend.ExitError{Code: ee.ExitCode()}
	}
	return err
}

func StartReviewGate(mode, egDir string, consent func(host, port string) bool) (*reviewgate.Gate, func()) {
	if mode != "review" || consent == nil {
		return nil, func() {}
	}
	gate := reviewgate.New(consent)
	dir := filepath.Join(egDir, "review")
	if err := gate.Listen(dir); err != nil {
		ui.Warnf("%v — connections will be denied", err)
		return nil, func() {}
	}
	return gate, func() {
		_ = gate.Close()
		if d := gate.Decisions(); len(d) > 0 {
			var allowed, denied int
			for _, v := range d {
				if v == reviewgate.Allow {
					allowed++
				} else {
					denied++
				}
			}
			ui.Hostf("review: %d host(s) allowed, %d denied", allowed, denied)
		}
	}
}

func OnSignalCleanup(cleanup func()) (stop func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		if _, ok := <-sigs; ok {
			cleanup()
			os.Exit(130) // 128 + SIGINT
		}
	}()
	return func() { signal.Stop(sigs) }
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
