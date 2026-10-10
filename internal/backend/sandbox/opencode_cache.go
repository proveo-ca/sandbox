// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

type opencodeCacheOwner struct {
	Identity string `json:"cache_owner"`
}

var cacheOwnerIdentity = regexp.MustCompile(`^[1-9][0-9]*:[0-9]+$`)

func opencodeCacheTarget(target string) bool {
	return target == "opencode" || target == "opencode-browser"
}

func cacheOwnerPath(name string) string {
	return filepath.Join(receiptDir(), name+".opencode-cache.json")
}

func openCodeEngineActive(name string) bool {
	if !sbx.Running(name) {
		return true
	}
	var owner opencodeCacheOwner
	data, err := os.ReadFile(cacheOwnerPath(name))
	if err != nil || json.Unmarshal(data, &owner) != nil || !cacheOwnerIdentity.MatchString(owner.Identity) {
		return true
	}
	_, err = cacheCommand(name, "--retire-safe", "--proveo-cache-owner="+owner.Identity)
	return err != nil
}

func cacheRealPath(path string) string {
	path, _ = filepath.Abs(path)
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return resolved
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}

func openCodePreparationSource(extra []string) (runner.Mount, bool, error) {
	if len(extra) == 0 || extra[0] != "--proveo-prepare" {
		return runner.Mount{}, false, nil
	}
	for index, arg := range extra {
		if arg != "--source" {
			continue
		}
		if index+1 == len(extra) || !filepath.IsAbs(extra[index+1]) {
			return runner.Mount{}, false, fmt.Errorf("OpenCode preparation requires an absolute --source directory")
		}
		source := filepath.Clean(extra[index+1])
		info, err := os.Lstat(source)
		if err != nil {
			return runner.Mount{}, false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return runner.Mount{}, false, fmt.Errorf("OpenCode preparation source must be a directory, not a symlink")
		}
		return runner.Mount{Host: source, ReadOnly: true}, true, nil
	}
	return runner.Mount{}, false, nil
}

func prepareOpenCodeSource(in Input, access HomeAccess, source runner.Mount) error {
	for _, mount := range access.Mounts {
		if cacheRealPath(mount.Host) == cacheRealPath(source.Host) && cacheRealPath(source.Host) == cacheRealPath(filepath.Join(in.HomeRoot, "opencode", "v2", "share")) {
			return nil
		}
	}
	maintenance := in
	maintenance.HomeAccess = access
	maintenance.HomeAccess.Mounts = append(append([]runner.Mount(nil), access.Mounts...), source)
	maintenance.EgDir = filepath.Join(in.EgDir, "source-preparation")
	cfg, kit, _ := Spec(maintenance)
	cfg.Name += "-prepare"
	if err := validateCacheAuthority(maintenance, cfg); err != nil {
		return err
	}
	dir, err := sbx.WriteKit(cfg.KitDir, kit)
	if err != nil {
		return err
	}
	cfg.KitDir = dir
	if err := sbx.EnsureTemplate(cfg.Image, ui.Appf); err != nil {
		return err
	}
	if sbx.Exists(cfg.Name) {
		return fmt.Errorf("source preparation engine %s already exists; preserve and inspect its state before retrying", cfg.Name)
	}
	create := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...)
	create.Stdout, create.Stderr = os.Stdout, os.Stderr
	if err := create.Run(); err != nil {
		return err
	}
	owner, err := openCodeCacheOwner(cfg.Name, cacheCommand)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	out, err := cacheCommandContext(ctx, cfg.Name, "--prepare", "--source", source.Host, "--proveo-cache-owner="+owner)
	if err != nil {
		return fmt.Errorf("source preparation engine retained for recovery (%s): %w: %s", cfg.Name, err, strings.TrimSpace(out))
	}
	if _, err := cacheCommand(cfg.Name, "--retire-safe", "--proveo-cache-owner="+owner); err != nil {
		return fmt.Errorf("source preparation left unpublished state in %s: %w", cfg.Name, err)
	}
	if out, err := SbxRun(sbx.RemoveArgs(cfg.Name)...); err != nil {
		return fmt.Errorf("remove completed source preparation engine: %w: %s", err, out)
	}
	_ = os.Remove(cacheOwnerPath(cfg.Name))
	return nil
}

func cacheCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cacheCommandContext(ctx, name, args...)
}

func cacheCommandContext(ctx context.Context, name string, args ...string) (string, error) {
	command := append([]string{"exec", "-w", "/", name, "--", "/usr/bin/python3", "-B", "-I", "-S", "/usr/local/bin/proveo-opencode-runtime"}, args...)
	cmd := exec.CommandContext(ctx, sbx.Binary, command...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func openCodeCacheOwner(name string, run func(string, ...string) (string, error)) (string, error) {
	args := []string{"--bootstrap-cache"}
	var previous opencodeCacheOwner
	if data, err := os.ReadFile(cacheOwnerPath(name)); err == nil && json.Unmarshal(data, &previous) == nil && cacheOwnerIdentity.MatchString(previous.Identity) {
		args = append(args, "--proveo-cache-owner="+previous.Identity)
	}
	out, err := run(name, args...)
	if err != nil {
		return "", fmt.Errorf("OpenCode cache bootstrap: %w: %s", err, strings.TrimSpace(out))
	}
	var owner opencodeCacheOwner
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &owner) != nil || !cacheOwnerIdentity.MatchString(owner.Identity) {
		return "", fmt.Errorf("OpenCode cache bootstrap returned invalid owner identity")
	}
	if err := os.MkdirAll(filepath.Dir(cacheOwnerPath(name)), 0o700); err != nil {
		return "", err
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(cacheOwnerPath(name), data, 0o600); err != nil {
		return "", err
	}
	return owner.Identity, nil
}

func validateCacheAuthority(in Input, cfg sbx.RunConfig) error {
	for _, mount := range cfg.Mounts {
		if !mount.ReadOnly && within(cacheRealPath(cacheOwnerPath(cfg.Name)), cacheRealPath(mount.Host)) && !(cfg.Clone && cacheRealPath(mount.Host) == cacheRealPath(in.RepoRoot)) {
			return fmt.Errorf("OpenCode cache authority would be inside an agent-writable mount; narrow the workspace or move the host cache directory")
		}
	}
	return nil
}

func openCodeHistory(home string) (v2Database bool, legacy []string) {
	if info, err := os.Stat(filepath.Join(home, "opencode", "v2", "share", "opencode.db")); err == nil && !info.IsDir() {
		v2Database = true
	}
	for _, rel := range []string{"opencode/share", ".local/share/opencode"} {
		path := filepath.Join(home, rel)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			legacy = append(legacy, rel)
		}
	}
	return v2Database, legacy
}

func stripOpenCodeMaintenance(command []string) []string {
	if len(command) == 0 || (command[0] != "--proveo-prepare" && command[0] != "--proveo-recover") {
		return command
	}
	var out []string
	for i := 1; i < len(command); i++ {
		switch command[i] {
		case "--source":
			i++
		case "--empty":
		default:
			out = append(out, command[i])
		}
	}
	return out
}

var headedConfirm = func() bool {
	if os.Getenv("PROVEO_SCHEDULE") != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_WIZARD"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return agentio.IsReaderTTY(os.Stdin) && agentio.IsWriterTTY(os.Stdout)
}

func openScreen() (tcell.Screen, error) {
	screen, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}
	if err := screen.Init(); err != nil {
		return nil, err
	}
	return screen, nil
}

var confirmSandboxReset = func(name string) bool {
	if !headedConfirm() {
		return false
	}
	screen, err := openScreen()
	if err != nil {
		return false
	}
	defer screen.Fini()
	return choiceui.ConfirmSandboxReset(screen, name)
}

var confirmEmptyPrepare = func(stores []string) bool {
	if !headedConfirm() {
		return false
	}
	screen, err := openScreen()
	if err != nil {
		return false
	}
	defer screen.Fini()
	return choiceui.ConfirmEmptyPrepare(screen, stores)
}

func createOpenCodeEngine(cfg sbx.RunConfig) error {
	cmd := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	_ = os.Remove(cacheOwnerPath(cfg.Name))
	return nil
}

func removeOpenCodeEngine(name string) error {
	ui.Hostf("sbx rm --force %s — deleting the session", name)
	if out, err := SbxRun(sbx.RemoveArgs(name)...); err != nil && !sbx.NotFound(out) {
		return fmt.Errorf("remove %s: %w: %s", name, err, strings.TrimSpace(out))
	}
	_ = os.Remove(cacheOwnerPath(name))
	_ = os.Remove(receiptPath(name))
	ui.Notef("the next launch creates a new sandbox named %s. This session does not persist", name)
	return nil
}

func openCodeBootstrapFailure(err error, name string, removed bool) error {
	if removed {
		return fmt.Errorf("%w. The previous sandbox was removed. The next proveo run creates a new sandbox named %s. This session does not persist", err, name)
	}
	return fmt.Errorf("%w. This sandbox stays. The next proveo run reattaches to it. sbx rm --force %s deletes the session; the next run creates a new sandbox with this same name, and this session does not persist", err, name)
}

func prepareOpenCodeLaunch(in Input, cfg sbx.RunConfig, exists func(string) bool, run func(context.Context, string, ...string) (string, error)) (sbx.RunConfig, error) {
	if !opencodeCacheTarget(in.Target) || in.Shell {
		return cfg, nil
	}
	if err := validateCacheAuthority(in, cfg); err != nil {
		return cfg, err
	}
	var owner string
	removed := false
	for attempt := 0; attempt < 2; attempt++ {
		if !exists(cfg.Name) {
			if err := createOpenCodeEngine(cfg); err != nil {
				return cfg, err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		call := func(name string, args ...string) (string, error) { return run(ctx, name, args...) }
		var err error
		owner, err = openCodeCacheOwner(cfg.Name, call)
		cancel()
		if err == nil {
			break
		}
		if attempt == 0 && exists(cfg.Name) && confirmSandboxReset(cfg.Name) {
			if err := removeOpenCodeEngine(cfg.Name); err != nil {
				return cfg, err
			}
			removed = true
			continue
		}
		return cfg, openCodeBootstrapFailure(err, cfg.Name, removed)
	}
	recovering := len(cfg.Command) > 0 && cfg.Command[0] == "--proveo-recover"
	preparing := len(cfg.Command) > 0 && cfg.Command[0] == "--proveo-prepare"
	flag := "--proveo-cache-owner=" + owner
	if recovering {
		ui.Appf("OpenCode recover: 1/2 reconcile the retained history")
		recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 15*time.Minute)
		out, gateErr := run(recoverCtx, cfg.Name, "--recover", flag)
		cancelRecover()
		if gateErr != nil {
			var exit *exec.ExitError
			if errors.As(gateErr, &exit) && exit.ExitCode() == 75 {
				return cfg, backend.ExitError{Code: 75}
			}
			return cfg, fmt.Errorf("OpenCode recover failed: %w: %s", gateErr, strings.TrimSpace(out))
		}
		ui.Appf("OpenCode recover: 2/2 start the agent on the recovered cache")
		cfg.Command = stripOpenCodeMaintenance(cfg.Command)
	} else {
		checkCtx, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
		out, gateErr := run(checkCtx, cfg.Name, "--check-prepared", flag)
		cancelCheck()
		if gateErr != nil {
			var exit *exec.ExitError
			if errors.As(gateErr, &exit) && exit.ExitCode() == 75 {
				return cfg, backend.ExitError{Code: 75}
			}
			v2, legacy := openCodeHistory(in.HomeRoot)
			explicitEmpty := false
			for _, arg := range cfg.Command {
				if arg == "--empty" {
					explicitEmpty = true
				}
			}
			prepareEmpty := explicitEmpty || !v2
			if !v2 && len(legacy) > 0 && !explicitEmpty && !confirmEmptyPrepare(legacy) {
				return cfg, fmt.Errorf("OpenCode history is not prepared: %s. This sandbox stays. The next proveo run reattaches and asks again. An empty V2 prepare does not import %s, and the prepared cache does not survive sbx rm", strings.TrimSpace(out), strings.Join(legacy, ", "))
			}
			if v2 && !explicitEmpty && !preparing {
				return cfg, fmt.Errorf("OpenCode history is not prepared: %s; run proveo run %s -- --proveo-prepare with an explicitly mounted V2 source", strings.TrimSpace(out), in.Target)
			}
			if v2 && !explicitEmpty {
				prepareEmpty = false
			}
			what := "the mounted V2 database"
			if prepareEmpty {
				what = "an empty V2 database"
			}
			ui.Appf("OpenCode prepare: 1/3 initialize %s", what)
			prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), 15*time.Minute)
			args := []string{"--prepare", flag}
			if prepareEmpty {
				args = append(args, "--empty")
			}
			out, gateErr = run(prepareCtx, cfg.Name, args...)
			cancelPrepare()
			if gateErr != nil {
				if errors.As(gateErr, &exit) && exit.ExitCode() == 75 {
					return cfg, backend.ExitError{Code: 75}
				}
				return cfg, fmt.Errorf("OpenCode prepare failed: %w: %s", gateErr, strings.TrimSpace(out))
			}
			ui.Appf("OpenCode prepare: 2/3 published a credential-free checkpoint")
			ui.Appf("OpenCode prepare: 3/3 start the agent on that cache")
			if preparing {
				cfg.Command = stripOpenCodeMaintenance(cfg.Command)
			}
		} else if preparing {
			cfg.Command = stripOpenCodeMaintenance(cfg.Command)
		}
	}
	cfg.Command = append([]string{flag}, cfg.Command...)
	seedCtx, cancelSeed := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSeed()
	seed := exec.CommandContext(seedCtx, sbx.Binary, "exec", "-w", "/", cfg.Name, "--", "sh", "-c",
		`rm -f /dev/shm/proveo-instructions-seeded /dev/shm/proveo-seed-done; nohup env PROVEO_OPENCODE_CACHE_OWNER="$1" /usr/local/bin/proveo-seed opencode >/dev/shm/proveo-opencode-seed.log 2>&1 </dev/null &`, "proveo-cache-seed", owner)
	if seedOut, err := seed.CombinedOutput(); err != nil {
		return cfg, fmt.Errorf("OpenCode instruction seed: %w: %s", err, strings.TrimSpace(string(seedOut)))
	}
	if deadline, enabled := seedCtx.Deadline(); enabled {
		remaining := time.Until(deadline).Seconds()
		if remaining <= 0 {
			return cfg, fmt.Errorf("OpenCode prepared startup exceeded its shared five-second budget")
		}
		cfg.Command = append([]string{fmt.Sprintf("--proveo-startup-deadline=%.6f", float64(deadline.UnixNano())/1e9)}, cfg.Command...)
	}
	ui.Appf("OpenCode: prepared V2 history on retained engine %s", cfg.Name)
	return sbx.Reattach(cfg), nil
}
