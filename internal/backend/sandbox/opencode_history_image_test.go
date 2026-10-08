//go:build image

// SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/internal/sbx/ide-attach.puml
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestOpenCodeLegacyHistoryThroughActualNarrowImageMounts(t *testing.T) {
	image := os.Getenv("PROVEO_E2E_OPENCODE_IMAGE")
	if image == "" {
		image = "proveo/opencode:local"
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skip("OpenCode image unavailable")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	legacy := filepath.Join(root, ".local/share/opencode")
	setup := exec.Command("python3", "-B", "-c", `import sys,sqlite3
from pathlib import Path
sys.path.insert(0,sys.argv[1]+"/packages/lib")
from test_opencode_credentials import SCHEMA
p=Path(sys.argv[2]);p.mkdir(parents=True)
db=sqlite3.connect(p/"opencode.db");db.executescript(SCHEMA)
db.execute("INSERT INTO credential VALUES ('fixture','fixture','fixture','SYNTHETIC_OLD_DOCKER_AUTH',NULL,NULL,1,1,2)");db.commit();db.close()
`, repo, legacy)
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("create synthetic history: %v\n%s", err, out)
	}
	home := manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{
		{Host: "opencode/config", Container: "/proveo-home/.config/opencode"},
		{Host: "opencode/share", Container: "/proveo-home/.local/share/opencode"},
	}}
	access, err := PrepareHomeAccess(root, t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--rm", "--network", "none", "--entrypoint", "python3", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--mount", "type=bind,source=" + repo + ",target=/fixture,readonly"}
	for _, mount := range access.Mounts {
		args = append(args, "--mount", "type=bind,source="+mount.Host+",target="+mount.Host)
	}
	args = append(args, image, "-B", "-c", `import sys,os,sqlite3,subprocess
from pathlib import Path
root=Path(sys.argv[1]);legacy=Path(sys.argv[2])
assert legacy.is_dir()
env={"PATH":os.environ["PATH"],"HOME":"/tmp/guest","PROVEO_HOME":"/tmp/guest","PROVEO_STATE_HOME":str(root),
"PROVEO_CONFIG_DIRS":"opencode/share|.local/share/opencode|auth.json","PROVEO_OPENCODE_LEGACY_DATA":str(legacy),
"PROVEO_OPENCODE_CREDENTIAL_HELPER":"/fixture/packages/lib/opencode-credentials.py","PYTHONDONTWRITEBYTECODE":"1",
"PYTHONPATH":"/fixture/packages/lib","OPENAI_API_KEY":"SYNTHETIC_ENV_KEY","PROVEO_RUNTIME_EVENTS":"/tmp/events.jsonl"}
Path("/tmp/guest").mkdir()
native=Path("/tmp/native-fixture")
native.write_text("#!/usr/bin/env python3\n"+Path("/fixture/packages/lib/opencode-runtime-fixture.py").read_text());native.chmod(0o700)
run=subprocess.run([sys.executable,"-B","/fixture/packages/lib/proveo-opencode-runtime",str(native),"append"],env=env,capture_output=True,text=True)
assert run.returncode==0,run.stderr
sys.path.insert(0,"/fixture/packages/lib")
from test_opencode_credentials import credentials
canonical=root/"opencode/share"
db=sqlite3.connect(canonical/"opencode.db")
assert db.execute("SELECT count(*) FROM credential").fetchone()==(0,)
assert db.execute("SELECT count(*) FROM session_message").fetchone()==(2,)
db.close()
`, root, access.LegacyOpenCode)
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("generated narrow mount plan cannot reach legacy history: %v\n%s", err, out)
	}
}
