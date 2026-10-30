// SPEC: _spec/internal/sbx/kit-image-source.puml
package sbx

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const digestA = "sha256:ed1aa77cdeb03b2d810fd86ca0d15c7b1a44c9c840580b0539dbf66ac710d01f"

func TestResolveImageSource(t *testing.T) {
	cases := []struct {
		name        string
		ref         string
		localID     string
		repoDigests []string
		builder     string
		want        ImageSource
		wantFrom    string
		wantEnv     []string
	}{
		{
			name: "host image with a repo digest pins FROM and the host builder",
			ref:  "proveo/codex:local", localID: "ed1aa77cdeb0",
			repoDigests: []string{"proveo/codex@" + digestA}, builder: "default",
			want:     ImageSource{Ref: "proveo/codex:local", Host: true, Digest: digestA, Builder: "default"},
			wantFrom: "proveo/codex:local@" + digestA,
			wantEnv:  []string{"BUILDX_BUILDER=default"},
		},
		{
			name: "classic store keeps no repo digest: tag FROM, builder still pinned",
			ref:  "proveo/codex:local", localID: "6494909f99bd", builder: "default",
			want:     ImageSource{Ref: "proveo/codex:local", Host: true, Builder: "default"},
			wantFrom: "proveo/codex:local",
			wantEnv:  []string{"BUILDX_BUILDER=default"},
		},
		{
			name: "an image the host lacks is left to the builder to pull",
			ref:  "proveo/codex:latest", builder: "default",
			want:     ImageSource{Ref: "proveo/codex:latest"},
			wantFrom: "proveo/codex:latest",
		},
		{
			name: "no docker-driver builder: digest still pinned, active builder decides",
			ref:  "proveo/codex:local", localID: "ed1aa77cdeb0",
			repoDigests: []string{"proveo/codex@" + digestA},
			want:        ImageSource{Ref: "proveo/codex:local", Host: true, Digest: digestA},
			wantFrom:    "proveo/codex:local@" + digestA,
		},
		{
			name: "a digest recorded under another repository is not this ref's",
			ref:  "proveo-probe/fedora:local", localID: "ed1aa77cdeb0",
			repoDigests: []string{"proveo/opencode-browser@" + digestA}, builder: "default",
			want:     ImageSource{Ref: "proveo-probe/fedora:local", Host: true, Builder: "default"},
			wantFrom: "proveo-probe/fedora:local",
			wantEnv:  []string{"BUILDX_BUILDER=default"},
		},
		{
			name: "an explicit digest reference is used as written",
			ref:  "proveo/codex@" + digestA, localID: "ed1aa77cdeb0",
			repoDigests: []string{"proveo/codex@" + digestA}, builder: "default",
			want:     ImageSource{Ref: "proveo/codex@" + digestA, Host: true, Digest: digestA, Builder: "default"},
			wantFrom: "proveo/codex@" + digestA,
			wantEnv:  []string{"BUILDX_BUILDER=default"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := sh
			t.Cleanup(func() { sh = restore })
			sh.LocalImageID = func(string) string { return tc.localID }
			sh.RepoDigests = func(string) []string { return tc.repoDigests }
			asked := false
			hostBuilder := func() (string, bool) { asked = true; return tc.builder, tc.builder != "" }

			got := ResolveImageSource(tc.ref, hostBuilder)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ResolveImageSource(%q) mismatch (-want +got):\n%s", tc.ref, diff)
			}
			if from := got.From(); from != tc.wantFrom {
				t.Errorf("ResolveImageSource(%q).From() = %q, want %q", tc.ref, from, tc.wantFrom)
			}
			if env := got.BuildEnv(); !slices.Equal(env, tc.wantEnv) {
				t.Errorf("ResolveImageSource(%q).BuildEnv() = %q, want %q", tc.ref, env, tc.wantEnv)
			}
			if asked != got.Host {
				t.Errorf("ResolveImageSource(%q) asked for a host builder = %v, want %v (only a host image needs one)", tc.ref, asked, got.Host)
			}
		})
	}
}

func TestRepoDigestMatchesFamiliarAndQualifiedNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref         string
		repoDigests []string
		want        string
	}{
		{"redis:7.0-alpine", []string{"redis@" + digestA}, digestA},
		{"docker.io/library/redis:7.0-alpine", []string{"redis@" + digestA}, digestA},
		{"localhost:5000/proveo/codex:local", []string{"localhost:5000/proveo/codex@" + digestA}, digestA},
		{"localhost:5000/proveo/codex", []string{"proveo/codex@" + digestA}, ""},
		{"proveo/codex:local", nil, ""},
	}
	for _, tc := range cases {
		if got := RepoDigest(tc.ref, tc.repoDigests); got != tc.want {
			t.Errorf("RepoDigest(%q, %q) = %q, want %q", tc.ref, tc.repoDigests, got, tc.want)
		}
	}
}

func TestCreateCarriesTheKitBuildEnvOnTheProcessNotInTheSandbox(t *testing.T) {
	t.Parallel()
	cfg := RunConfig{Name: "proveo-codex-1", KitDir: "/k/proveo-codex", HostEnv: []string{"BUILDX_BUILDER=default"}}
	for _, a := range CreateArgs(cfg) {
		if a == "BUILDX_BUILDER=default" {
			t.Errorf("CreateArgs(%+v) = %q: the kit build env leaked into the sandbox argv", cfg, CreateArgs(cfg))
		}
	}
	if got, want := CreateLine(cfg), "BUILDX_BUILDER=default sbx "+ShellJoin(CreateArgs(cfg)); got != want {
		t.Errorf("CreateLine(%+v) = %q, want %q", cfg, got, want)
	}
	if got, want := CreateLine(RunConfig{Name: "n", KitDir: "/k/a"}), "sbx "+ShellJoin(CreateArgs(RunConfig{Name: "n", KitDir: "/k/a"})); got != want {
		t.Errorf("CreateLine without HostEnv = %q, want %q", got, want)
	}
	env := CreateEnv([]string{"PATH=/bin", "BUILDX_BUILDER=proveo-multiarch"}, cfg)
	if last := env[len(env)-1]; last != "BUILDX_BUILDER=default" {
		t.Errorf("CreateEnv(...) ends with %q, want HostEnv last so it overrides the inherited builder", last)
	}
	if r := Reattach(cfg); len(r.HostEnv) != 0 {
		t.Errorf("Reattach(%+v).HostEnv = %q; a reattach builds nothing", cfg, r.HostEnv)
	}
}
