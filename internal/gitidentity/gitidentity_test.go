package gitidentity

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestResolveEnvWins(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"GIT_AUTHOR_NAME":  "Env Name",
		"GIT_AUTHOR_EMAIL": "env@test.dev",
	}
	got := Resolve(func(k string) string { return env[k] }, func(string) string {
		t.Fatal("git config should not be consulted when env is set")
		return ""
	})
	want := Identity{Name: "Env Name", Email: "env@test.dev"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Resolve mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveCommitterFallback(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"GIT_COMMITTER_NAME":  "C Name",
		"GIT_COMMITTER_EMAIL": "c@test.dev",
	}
	got := Resolve(func(k string) string { return env[k] }, func(string) string { return "" })
	want := Identity{Name: "C Name", Email: "c@test.dev"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Resolve mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveGitConfig(t *testing.T) {
	t.Parallel()
	cfg := map[string]string{"user.name": "Git User", "user.email": "git@test.dev"}
	got := Resolve(func(string) string { return "" }, func(k string) string { return cfg[k] })
	want := Identity{Name: "Git User", Email: "git@test.dev"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Resolve mismatch (-want +got):\n%s", diff)
	}
}

func TestEnvPairs(t *testing.T) {
	t.Parallel()
	got := Identity{Name: "N", Email: "e@x"}.EnvPairs()
	want := []string{
		"GIT_AUTHOR_NAME=N",
		"GIT_COMMITTER_NAME=N",
		"GIT_AUTHOR_EMAIL=e@x",
		"GIT_COMMITTER_EMAIL=e@x",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("EnvPairs mismatch (-want +got):\n%s", diff)
	}
	if len((Identity{}).EnvPairs()) != 0 {
		t.Fatal("empty identity should yield no env pairs")
	}
	if (Identity{Name: "N"}).Complete() || (Identity{}).Complete() {
		t.Error("a half-set or empty identity is not complete")
	}
	if !(Identity{Name: "N", Email: "e@x"}).Complete() {
		t.Error("both halves set is complete")
	}
}
