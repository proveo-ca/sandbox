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

	"github.com/proveo-ca/proveo/internal/backend"
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

func prepareOpenCodeLaunch(in Input, cfg sbx.RunConfig, exists func(string) bool, run func(context.Context, string, ...string) (string, error)) (sbx.RunConfig, error) {
	if !opencodeCacheTarget(in.Target) || in.Shell {
		return cfg, nil
	}
	if err := validateCacheAuthority(in, cfg); err != nil {
		return cfg, err
	}
	if !exists(cfg.Name) {
		cmd := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return cfg, err
		}
		_ = os.Remove(cacheOwnerPath(cfg.Name))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	call := func(name string, args ...string) (string, error) { return run(ctx, name, args...) }
	owner, err := openCodeCacheOwner(cfg.Name, call)
	if err != nil {
		return cfg, err
	}
	maintenance := len(cfg.Command) > 0 && (cfg.Command[0] == "--proveo-prepare" || cfg.Command[0] == "--proveo-recover")
	flag := "--proveo-cache-owner=" + owner
	if !maintenance {
		out, gateErr := call(cfg.Name, "--check-prepared", flag)
		if gateErr != nil {
			share := filepath.Join(in.HomeRoot, "opencode", "v2", "share")
			_, databaseErr := os.Stat(filepath.Join(share, "opencode.db"))
			_, oldErr := os.Stat(filepath.Join(in.HomeRoot, "opencode", "share"))
			_, otherErr := os.Stat(filepath.Join(in.HomeRoot, ".local", "share", "opencode"))
			if os.IsNotExist(databaseErr) && os.IsNotExist(oldErr) && os.IsNotExist(otherErr) {
				out, gateErr = call(cfg.Name, "--prepare", "--empty", flag)
			}
			if gateErr != nil {
				var exit *exec.ExitError
				if errors.As(gateErr, &exit) && exit.ExitCode() == 75 {
					return cfg, backend.ExitError{Code: 75}
				}
				return cfg, fmt.Errorf("OpenCode history is not prepared: %s; run proveo run %s -- --proveo-prepare with an explicitly mounted V2 source", strings.TrimSpace(out), in.Target)
			}
		}
	}
	cfg.Command = append([]string{flag}, cfg.Command...)
	if !maintenance {
		seed := exec.CommandContext(ctx, sbx.Binary, "exec", "-w", "/", cfg.Name, "--", "sh", "-c",
			`rm -f /dev/shm/proveo-instructions-seeded /dev/shm/proveo-seed-done; nohup env PROVEO_OPENCODE_CACHE_OWNER="$1" /usr/local/bin/proveo-seed opencode >/dev/shm/proveo-opencode-seed.log 2>&1 </dev/null &`, "proveo-cache-seed", owner)
		if out, err := seed.CombinedOutput(); err != nil {
			return cfg, fmt.Errorf("OpenCode instruction seed: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if deadline, enabled := ctx.Deadline(); enabled && !maintenance {
		remaining := time.Until(deadline).Seconds()
		if remaining <= 0 {
			return cfg, fmt.Errorf("OpenCode prepared startup exceeded its shared five-second budget")
		}
		cfg.Command = append([]string{fmt.Sprintf("--proveo-startup-deadline=%.6f", float64(deadline.UnixNano())/1e9)}, cfg.Command...)
	}
	ui.Appf("OpenCode: prepared V2 history on retained engine %s", cfg.Name)
	return sbx.Reattach(cfg), nil
}
