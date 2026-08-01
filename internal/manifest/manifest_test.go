package manifest

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		want    Manifest
	}{
		{
			name: "valid multi-image",
			yaml: "name: claudecode\ndescription: d\negress: true\nstability: candidate\nimages:\n  claudecode: proveo/claudecode:latest\n  claudecode-solo: proveo/claudecode-solo:latest\n",
			want: Manifest{Name: "claudecode", Description: "d", Egress: true, Stability: "candidate",
				Images: map[string]string{"claudecode": "proveo/claudecode:latest", "claudecode-solo": "proveo/claudecode-solo:latest"}, Dir: "dir"},
		},
		{name: "missing name", yaml: "images:\n  x: y\n", wantErr: true},
		{name: "no images", yaml: "name: x\n", wantErr: true},
		{name: "bad stability", yaml: "name: x\nstability: bogus\nimages:\n  x: y\n", wantErr: true},
		{name: "bad layout", yaml: "name: x\nimages:\n  x: y\nworkspace:\n  layout: bogus\n", wantErr: true},
		{name: "bad gitMode", yaml: "name: x\nimages:\n  x: y\nworkspace:\n  gitMode: bogus\n", wantErr: true},
		{name: "bad mode", yaml: "name: x\nimages:\n  x: y\nworkspace:\n  mode: bogus\n", wantErr: true},
		{
			name: "workspace round-trip",
			yaml: "name: cecli\nimages:\n  cecli: img\nworkspace:\n  layout: app\n  configDir: .cecli\n  gitMode: ro\n  output: true\n  mode: ro\n",
			want: Manifest{Name: "cecli", Images: map[string]string{"cecli": "img"},
				Workspace: Workspace{Layout: "app", ConfigDir: ".cecli", GitMode: "ro", Output: true, Mode: "ro"}, Dir: "dir"},
		},
		{
			name: "env round-trip",
			yaml: "name: cursor\nimages:\n  cursor: img\nenv:\n  - name: CURSOR_API_KEY\n    description: Cursor API key\n    secret: true\n",
			want: Manifest{Name: "cursor", Images: map[string]string{"cursor": "img"},
				Env: []EnvVar{{Name: "CURSOR_API_KEY", Description: "Cursor API key", Secret: true}}, Dir: "dir"},
		},
		{
			name: "subscription round-trip",
			yaml: "name: claudecode\nsubscription: true\nimages:\n  claudecode: img\n",
			want: Manifest{Name: "claudecode", Subscription: true,
				Images: map[string]string{"claudecode": "img"}, Dir: "dir"},
		},
		{name: "env entry without a name", yaml: "name: x\nimages:\n  x: y\nenv:\n  - description: d\n", wantErr: true},
		{name: "duplicate env entry", yaml: "name: x\nimages:\n  x: y\nenv:\n  - name: A\n  - name: A\n", wantErr: true},
		{
			name: "home mounts round-trip",
			yaml: "name: cursor\nimages:\n  cursor: img\nhome:\n  enabled: true\n  mounts:\n    - host: .cursor\n      container: /proveo-home/.cursor\n      mode: rw\n      deny: [auth.json]\n",
			want: Manifest{Name: "cursor", Images: map[string]string{"cursor": "img"},
				Home: Home{Enabled: true, Mounts: []HomeMount{{Host: ".cursor", Container: "/proveo-home/.cursor", Mode: "rw", Deny: []string{"auth.json"}}}}, Dir: "dir"},
		},
		{name: "home enabled no mounts", yaml: "name: x\nimages:\n  x: y\nhome:\n  enabled: true\n", wantErr: true},
		{name: "home abs host", yaml: "name: x\nimages:\n  x: y\nhome:\n  enabled: true\n  mounts:\n    - host: /etc/passwd\n      container: /proveo-home/x\n", wantErr: true},
		{name: "home relative container", yaml: "name: x\nimages:\n  x: y\nhome:\n  enabled: true\n  mounts:\n    - host: .x\n      container: relative\n", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse([]byte(tc.yaml), "dir")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Parse(%q) err = %v, wantErr = %v", tc.name, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Parse(%q) mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestLoadAndTargets(t *testing.T) {
	t.Parallel()
	defs := t.TempDir()
	write := func(dir, body string) {
		t.Helper()
		d := filepath.Join(defs, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, Filename), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cursor", "name: cursor\nimages:\n  cursor: proveo/cursor:latest\n")
	write("claudecode", "name: claudecode\nimages:\n  claudecode: proveo/claudecode:latest\n  claudecode-solo: proveo/claudecode-solo:latest\n")

	ms, err := Load(defs)
	if err != nil {
		t.Fatalf("Load(%s): %v", defs, err)
	}
	gotNames := []string{ms[0].Name, ms[1].Name}
	if diff := cmp.Diff([]string{"claudecode", "cursor"}, gotNames); diff != "" {
		t.Errorf("Load names mismatch (sorted) (-want +got):\n%s", diff)
	}

	targets, err := Targets(ms)
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	want := map[string]string{
		"cursor":          "proveo/cursor:latest",
		"claudecode":      "proveo/claudecode:latest",
		"claudecode-solo": "proveo/claudecode-solo:latest",
	}
	if diff := cmp.Diff(want, targets); diff != "" {
		t.Errorf("Targets mismatch (-want +got):\n%s", diff)
	}
}

func TestMissingEnv(t *testing.T) {
	t.Parallel()
	m := Manifest{Env: []EnvVar{
		{Name: "CURSOR_API_KEY", Secret: true},
		{Name: "CURSOR_TEAM_ID"},
	}}
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{name: "all missing", env: nil, want: []string{"CURSOR_API_KEY", "CURSOR_TEAM_ID"}},
		{name: "one present", env: map[string]string{"CURSOR_API_KEY": "sk"}, want: []string{"CURSOR_TEAM_ID"}},
		{name: "whitespace counts as missing", env: map[string]string{"CURSOR_API_KEY": "  ", "CURSOR_TEAM_ID": "t"}, want: []string{"CURSOR_API_KEY"}},
		{name: "none missing", env: map[string]string{"CURSOR_API_KEY": "sk", "CURSOR_TEAM_ID": "t"}, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, e := range m.MissingEnv(func(k string) string { return tc.env[k] }) {
				got = append(got, e.Name)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("MissingEnv(env=%v) mismatch (-want +got):\n%s", tc.env, diff)
			}
		})
	}
}

func TestTargetsRejectsDuplicate(t *testing.T) {
	t.Parallel()
	ms := []Manifest{
		{Name: "a", Images: map[string]string{"dup": "img-a"}},
		{Name: "b", Images: map[string]string{"dup": "img-b"}},
	}
	if _, err := Targets(ms); err == nil {
		t.Fatal("Targets with duplicate target = nil error, want error")
	}
}

// The real repo manifests must load and validate — guards the Plan-2 invariant
// that every harness is registered by exactly one manifest.
func TestRepoManifestsValid(t *testing.T) {
	t.Parallel()
	defs := repoDefsDir(t)
	ms, err := Load(defs)
	if err != nil {
		t.Fatalf("Load(%s): %v", defs, err)
	}
	if len(ms) == 0 {
		t.Fatalf("no manifests found under %s", defs)
	}
	if _, err := Targets(ms); err != nil {
		t.Errorf("repo manifests have conflicting targets: %v", err)
	}
}

func repoDefsDir(t *testing.T) string {
	t.Helper()
	// test runs in the package dir: internal/manifest -> repo root is ../../
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "defs")
}

// T5: LoadFS is the shipped path (the //go:embed glob defs/*/harness.manifest).
// A drift in that glob or the parse would break `proveo ls`/`run` in the
// binary with no unit failure — so exercise it against an fstest.MapFS.
func TestLoadFS(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"defs/alpha/harness.manifest":    {Data: []byte("name: alpha\nimages:\n  alpha: img/alpha:latest\nworkspace:\n  layout: app\n  mode: ro\n")},
		"defs/beta/harness.manifest":     {Data: []byte("name: beta\nimages:\n  beta: img/beta:latest\n")},
		"defs/alpha/README.md":           {Data: []byte("ignored")},
		"defs/nested/x/harness.manifest": {Data: []byte("name: nested\nimages:\n  nested: img\n")}, // wrong depth, must not match
	}
	ms, err := LoadFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("want 2 manifests (defs/*/harness.manifest only), got %d: %+v", len(ms), ms)
	}
	if ms[0].Name != "alpha" || ms[1].Name != "beta" { // sorted by name
		t.Errorf("names/order = %q,%q, want alpha,beta", ms[0].Name, ms[1].Name)
	}
	if ms[0].Workspace.Layout != "app" || ms[0].Workspace.Mode != "ro" {
		t.Errorf("workspace not parsed via LoadFS: %+v", ms[0].Workspace)
	}
	if _, err := Targets(ms); err != nil {
		t.Errorf("Targets over LoadFS output: %v", err)
	}
}

func TestLoadFSInvalidManifestErrors(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"defs/broken/harness.manifest": {Data: []byte("name: broken\n")}, // no images
	}
	if _, err := LoadFS(fsys); err == nil {
		t.Error("LoadFS must surface a validation error from a bad manifest")
	}
}

func TestConfigPassthroughValidation(t *testing.T) {
	t.Parallel()
	base := Manifest{Name: "x", Images: map[string]string{"x": "proveo/x:latest"}}

	ok := base
	ok.Config = []string{"ARCHITECT_MODEL", "MY_HARNESS_THEME"}
	if err := ok.Validate(); err != nil {
		t.Errorf("plain config passthrough must validate: %v", err)
	}

	empty := base
	empty.Config = []string{"  "}
	if err := empty.Validate(); err == nil {
		t.Error("an empty config entry must be rejected")
	}

	leak := base
	leak.Env = []EnvVar{{Name: "SOME_TOKEN", Secret: true}}
	leak.Config = []string{"SOME_TOKEN"}
	if err := leak.Validate(); err == nil {
		t.Error("a declared secret must not be forwardable as a config passthrough")
	}
}
