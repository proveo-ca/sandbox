//go:build e2e && !windows

// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeSbxPreparedSourceSurvivesOrdinaryReattach(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("host sbx runtime unavailable")
	}
	requireDocker(t)
	image := harnessImageName("opencode")
	if !imageExists(image) {
		t.Skipf("OpenCode image %s unavailable", image)
	}
	home, work, source := t.TempDir(), t.TempDir(), t.TempDir()
	create := exec.Command("docker", "run", "--rm", "--network", "none",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--mount", "type=bind,source="+source+",target=/source",
		"--entrypoint", "bash", image, "-c",
		`HOME=/tmp/native-schema XDG_DATA_HOME=/tmp/native-data XDG_STATE_HOME=/tmp/native-state OPENCODE_DB=/source/opencode.db OPENCODE_DISABLE_MODELS_FETCH=1 "$(cat /usr/local/lib/proveo/opencode.native)" session list --format json --standalone`)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create actual V2 source: %v\n%s", err, out)
	}
	bin := buildProveo(t)
	env := append(os.Environ(), "HOME="+home, "PROVEO_HOME="+home,
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"), "PROVEO_WIZARD=off",
		"PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_LSP_INSTALL=off",
		"PROVEO_AUTO_INSTALL_TOOLS=false", "OPENCODE_DISABLE_MODELS_FETCH=1")
	run := func(native ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		args := append([]string{"run", "opencode", "--yes", "--image", image,
			"--input", work, "--egress-mode", "open", "--"}, native...)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env, cmd.Dir = env, work
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("managed sbx launch %v: %v\n%s", native, err, out)
		}
		return string(out)
	}
	run("--proveo-prepare", "--source", source)
	owners, err := filepath.Glob(filepath.Join(home, "cache/proveo/sbx-sandboxes/*.opencode-cache.json"))
	if err != nil || len(owners) != 1 {
		t.Fatalf("prepared engine owner records: %v %v", owners, err)
	}
	before, err := os.ReadFile(owners[0])
	if err != nil {
		t.Fatal(err)
	}
	engine := strings.TrimSuffix(filepath.Base(owners[0]), ".opencode-cache.json")
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", engine).Run() })
	result := run("api", "POST", "/api/session", "--data", `{"title":"prepared-reuse-fixture","location":{"directory":`+strconv.Quote(work)+`}}`)
	var id string
	for _, line := range strings.Split(result, "\n") {
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &response) == nil && response.Data.ID != "" {
			id = response.Data.ID
		}
	}
	if id == "" {
		t.Fatalf("native session creation missing: %s", result)
	}
	run("api", "GET", "/api/session/"+id)
	after, err := os.ReadFile(owners[0])
	if err != nil || string(after) != string(before) {
		t.Fatalf("ordinary reattach replaced the prepared engine: %v", err)
	}
}
