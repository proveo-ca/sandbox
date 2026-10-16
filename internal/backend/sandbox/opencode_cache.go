// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/choiceui"
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

func cacheCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cacheCommandContext(ctx, name, args...)
}

func openCodeCommandError(action string, err error, out string) error {
	detail := strings.TrimSpace(out)
	status := strings.TrimSpace(err.Error())
	if detail == "" {
		return fmt.Errorf("OpenCode %s failed: %s", action, status)
	}
	if status == "" {
		return fmt.Errorf("OpenCode %s failed: %s", action, detail)
	}
	return fmt.Errorf("OpenCode %s failed: %s: %s", action, status, detail)
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
	owner, ok := cacheOwnerFromOutput(out)
	if !ok {
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

func cacheOwnerFromOutput(out string) (opencodeCacheOwner, bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var owner opencodeCacheOwner
		if json.Unmarshal([]byte(line), &owner) == nil && cacheOwnerIdentity.MatchString(owner.Identity) {
			return owner, true
		}
	}
	return opencodeCacheOwner{}, false
}

func adoptLegacyOpenCodeDatabase(home string) (bool, error) {
	destDir := filepath.Join(home, "opencode", "v2", "share")
	dest := filepath.Join(destDir, "opencode.db")
	if _, err := os.Lstat(dest); err == nil {
		return false, nil
	}
	var src string
	for _, rel := range []string{"opencode/share/opencode.db", ".local/share/opencode/opencode.db"} {
		candidate := filepath.Join(home, filepath.FromSlash(rel))
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		src = candidate
		break
	}
	if src == "" {
		return false, nil
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return false, err
	}
	if err := copyRegularFile(src, dest); err != nil {
		return false, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		side := src + suffix
		info, err := os.Lstat(side)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := copyRegularFile(side, dest+suffix); err != nil {
			return false, err
		}
	}
	return true, nil
}

func copyRegularFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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

var headedConfirm = func() bool {
	if os.Getenv("PROVEO_SCHEDULE") != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_WIZARD"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return agentTerminal()
}

// agentTerminal reports whether this run can hand the agent a terminal.
var agentTerminal = func() bool {
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

// seedOpenCode re-runs the seed in the background, bound to the engine's cache owner.
var seedOpenCode = func(ctx context.Context, name, owner string) ([]byte, error) {
	return exec.CommandContext(ctx, sbx.Binary, "exec", "-w", "/", name, "--", "sh", "-c",
		`rm -f /dev/shm/proveo-instructions-seeded /dev/shm/proveo-seed-done; nohup env PROVEO_OPENCODE_CACHE_OWNER="$1" /usr/local/bin/proveo-seed opencode >/dev/shm/proveo-opencode-seed.log 2>&1 </dev/null &`, "proveo-cache-seed", owner).CombinedOutput()
}

var createOpenCodeEngine = func(cfg sbx.RunConfig) error {
	cmd := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	_ = os.Remove(cacheOwnerPath(cfg.Name))
	return nil
}

// retainedRefusal is the runtime's prepare refusal for engine-local history no live keeper vouches for.
// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
const retainedRefusal = "unattested retained state requires explicit recovery"

// pendingRefusal is the runtime's prepare refusal for an interrupted or other-major journal.
const pendingRefusal = "history has pending private state"

// unpreparedRefusal is the runtime's check refusal for a keeper pending recovery.
const unpreparedRefusal = "OpenCode history is not prepared in this engine"

// openCodeJournalName is the durable store's lifecycle journal, beside its lifecycle lock.
const openCodeJournalName = ".proveo-opencode-runtime.lock.journal"

var journalPhaseWord = regexp.MustCompile(`^[a-z]{1,24}$`)

// openCodeJournal is the engine and phase the V2 store's journal last recorded; empty when unreadable.
func openCodeJournal(home string) (engine, phase string) {
	path := filepath.Join(home, "opencode", "v2", "share", openCodeJournalName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	var record struct {
		Engine string `json:"engine"`
		Phase  string `json:"phase"`
	}
	if json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&record) != nil {
		return "", ""
	}
	if !journalPhaseWord.MatchString(record.Phase) {
		record.Phase = ""
	}
	return record.Engine, record.Phase
}

var chooseRetainedRecovery = func(name, phase string) choiceui.RetainedRecovery {
	if !headedConfirm() {
		return choiceui.RetainedRecover
	}
	screen, err := openScreen()
	if err != nil {
		return choiceui.RetainedStop
	}
	defer screen.Fini()
	return choiceui.ChooseRetainedRecovery(screen, name, phase)
}

func retainedCommands(target, name string) string {
	return fmt.Sprintf("The next `proveo run %s` asks again, or %s", target, resetCommand(name))
}

func resetCommand(name string) string {
	return fmt.Sprintf("delete it with `sbx rm --force %s`: the next run then prepares from the proveo home, and sessions never published there are lost", name)
}

// recoverRetainedOpenCode handles engine-local history its keeper no longer vouches for: it recovers
// at once when this engine's journal says its last run ended prepared or published, asks first
// otherwise, and never deletes without a headed yes. reset reports that the engine was removed.
func recoverRetainedOpenCode(in Input, name, flag string, run func(context.Context, string, ...string) (string, error)) (reset bool, err error) {
	engine, phase := openCodeJournal(in.HomeRoot)
	recorded := phase
	if recorded == "" {
		recorded = "none"
	}
	switch {
	case engine != "" && engine != name && phase != "prepared" && phase != "published":
		return false, fmt.Errorf("OpenCode engine %s holds this proveo home's history (journal phase %s); %s keeps its retained history. Run again when that session ends", engine, recorded, name)
	case engine != "" && engine != name:
		ui.Warnf("OpenCode engine %s published to this proveo home after %s's last run; %s's retained copy is stale and cannot be recovered", engine, name, name)
		if confirmSandboxReset(name) {
			return true, removeOpenCodeEngine(name)
		}
		return false, fmt.Errorf("OpenCode history in %s is stale: %s published to this proveo home since. This sandbox stays. To continue, %s", name, engine, resetCommand(name))
	case phase == "prepared" || phase == "published":
		ui.Appf("OpenCode: re-verifying the history %s kept from its last run (journal phase %s)", name, phase)
	default:
		switch chooseRetainedRecovery(name, recorded) {
		case choiceui.RetainedReset:
			return true, removeOpenCodeEngine(name)
		case choiceui.RetainedStop:
			return false, fmt.Errorf("OpenCode history in %s needs recovery (journal phase %s). This sandbox stays. %s", name, recorded, retainedCommands(in.Target, name))
		}
		ui.Appf("OpenCode: recovering the history %s kept from its last run (journal phase %s)", name, recorded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	out, runErr := run(ctx, name, "--recover", flag)
	timedOut := ctx.Err() != nil
	cancel()
	if runErr == nil {
		ui.Appf("OpenCode recover: retained history re-verified; starting the agent on it")
		return false, nil
	}
	failure := openCodeCommandError("recover", runErr, out)
	if timedOut || strings.Contains(out, "owns this history store") || strings.Contains(out, "still owns the cache") {
		return false, fmt.Errorf("%w. This sandbox stays. Run again when the other session or the recovery ends", failure)
	}
	ui.Warnf("%v", failure)
	if confirmSandboxReset(name) {
		return true, removeOpenCodeEngine(name)
	}
	return false, fmt.Errorf("%w. This sandbox stays. To continue, %s", failure, resetCommand(name))
}

var removeOpenCodeEngine = func(name string) error {
	ui.Hostf("sbx rm --force %s — deleting the session", name)
	if out, err := SbxRun(sbx.RemoveArgs(name)...); err != nil && !sbx.NotFound(out) {
		return fmt.Errorf("remove %s: %w: %s", name, err, strings.TrimSpace(out))
	}
	_ = os.Remove(cacheOwnerPath(name))
	ui.Notef("this launch creates a new sandbox named %s. The removed session does not persist", name)
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
	flag := "--proveo-cache-owner=" + owner
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
	out, gateErr := run(checkCtx, cfg.Name, "--check-prepared", flag)
	cancelCheck()
	if gateErr != nil {
		var exit *exec.ExitError
		if errors.As(gateErr, &exit) && exit.ExitCode() == 75 && !strings.Contains(out, unpreparedRefusal) {
			return cfg, openCodeCommandError("history check", gateErr, out)
		}
		adopted, adoptErr := adoptLegacyOpenCodeDatabase(in.HomeRoot)
		if adoptErr != nil {
			return cfg, adoptErr
		}
		if adopted {
			ui.Appf("OpenCode: copied the existing database into the V2 store")
		}
		v2, legacy := openCodeHistory(in.HomeRoot)
		if !v2 && len(legacy) > 0 && !confirmEmptyPrepare(legacy) {
			return cfg, fmt.Errorf("OpenCode history is not prepared: %s. This sandbox stays. The next proveo run reattaches and asks again. An empty V2 prepare does not import %s, and the prepared cache does not survive sbx rm", strings.TrimSpace(out), strings.Join(legacy, ", "))
		}
		what := "the mounted V2 database"
		args := []string{"--prepare", flag}
		if !v2 {
			what = "an empty V2 database"
			args = append(args, "--empty")
		}
		ui.Appf("OpenCode prepare: 1/3 initialize %s", what)
		prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), 15*time.Minute)
		out, gateErr = run(prepareCtx, cfg.Name, args...)
		cancelPrepare()
		switch {
		case gateErr == nil:
			ui.Appf("OpenCode prepare: 2/3 published a credential-free checkpoint")
			ui.Appf("OpenCode prepare: 3/3 start the agent on that cache")
		case !strings.Contains(out, retainedRefusal) && !strings.Contains(out, pendingRefusal):
			return cfg, openCodeCommandError("prepare", gateErr, out)
		default:
			reset, err := recoverRetainedOpenCode(in, cfg.Name, flag, run)
			if err != nil {
				return cfg, err
			}
			if reset {
				return prepareOpenCodeLaunch(in, cfg, exists, run)
			}
		}
	}
	cfg.Command = append([]string{flag}, cfg.Command...)
	seedCtx, cancelSeed := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSeed()
	if seedOut, err := seedOpenCode(seedCtx, cfg.Name, owner); err != nil {
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
