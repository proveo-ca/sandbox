// SPEC: _spec/defs/opencode/native-v2-integration.puml
package contract_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

func TestOpenCodePresentationDockerPreferences(t *testing.T) {
	t.Parallel()
	body := readRepoFile(t, "defs/opencode/Dockerfile")
	matches := regexp.MustCompile(`(?m)^\s*(?:ENV\s+)?OPENCODE_CLI_CONFIG_CONTENT='([^']+)'\s*(?:\\)?$`).FindAllStringSubmatch(body, -1)
	if len(matches) != 1 {
		t.Fatalf("expected one inline CLI preference ENV declaration, got %d", len(matches))
	}
	var got, want map[string]any
	if err := json.Unmarshal([]byte(matches[0][1]), &got); err != nil {
		t.Fatalf("Docker CLI preferences must decode as JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "e2e/testdata/opencode_presentation.json")), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker presentation/YOLO preferences = %v, want %v", got, want)
	}
}

func TestOpenCodePresentationNativeRunArguments(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	cmd := exec.Command(python, "-B", "-m", "unittest", "-v", "test_opencode_runtime.NativeCommandAutoTests")
	cmd.Dir = filepath.Join(repoRoot(t), "packages/lib")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native run auto-injection contract: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}
