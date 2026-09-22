// SPEC: _spec/_devops/buildx-driver-selection.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const hubDNSErr = `ERROR: failed to solve: docker/sandbox-templates:shell-docker-0.5.0: failed to resolve source metadata for docker.io/docker/sandbox-templates:shell-docker-0.5.0: failed to do request: Head "https://registry-1.docker.io/v2/docker/sandbox-templates/manifests/shell-docker-0.5.0": dial tcp: lookup registry-1.docker.io: Temporary failure in name resolution
`

const fakeBuildxDocker = `#!/usr/bin/env bash
set -euo pipefail
log="${FAKE_DOCKER_LOG:?}"
printf '%s\n' "$*" >>"$log"
case "$1 $2" in
  "buildx version") exit 0 ;;
  "context show") printf 'default\n'; exit 0 ;;
  "buildx inspect")
    printf 'Driver: docker\nStatus: running\n'
    exit 0
    ;;
  "buildx build")
    for a in "$@"; do
      if [[ "$a" == "--pull=false" ]]; then
        printf 'loaded from local FROM\n'
        exit 0
      fi
    done
    printf '%s' "$FAKE_BUILDX_ERR" >&2
    exit 1
    ;;
esac
exit 0
`

func sourceDockerBuild(t *testing.T, env []string, stdin, script string) (stdout, stderr string, err error) {
	t.Helper()
	lib := filepath.Join(repoRoot(t), "defs", "lib", "docker-build.sh")
	cmd := exec.Command("bash", "-c", "set -euo pipefail; source \"$1\"; "+script, "bash", lib)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

func TestRegistryDNSErrorDetector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		hit  bool
	}{
		{name: "hub lookup", in: hubDNSErr, hit: true},
		{name: "no such host", in: "dial tcp: lookup registry-1.docker.io: no such host\n", hit: true},
		{name: "unrelated", in: "ERROR: failed to solve: base name should not be blank\n", hit: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := sourceDockerBuild(t, nil, c.in, "proveo_docker_is_registry_dns_error")
			if c.hit && err != nil {
				t.Fatalf("wanted a DNS hit, got %v", err)
			}
			if !c.hit && err == nil {
				t.Fatal("unrelated build error matched as registry DNS")
			}
		})
	}
}

func TestDockerPullFlagsFromEnv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		env    string
		want   string
		wantOK bool
	}{
		{env: "auto", want: "", wantOK: true},
		{env: "0", want: "--pull=false", wantOK: true},
		{env: "never", want: "--pull=false", wantOK: true},
		{env: "1", want: "--pull=true", wantOK: true},
		{env: "always", want: "--pull=true", wantOK: true},
		{env: "maybe", want: "", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			t.Parallel()
			out, errb, err := sourceDockerBuild(t,
				[]string{"PROVEO_DOCKER_PULL=" + c.env},
				"",
				"proveo_docker_pull_flags")
			got := strings.TrimSpace(out)
			if c.wantOK {
				if err != nil {
					t.Fatalf("proveo_docker_pull_flags: %v (%s)", err, errb)
				}
				if got != c.want {
					t.Fatalf("flags = %q, want %q", got, c.want)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid PROVEO_DOCKER_PULL succeeded")
			}
			if !strings.Contains(errb, "PROVEO_DOCKER_PULL") {
				t.Fatalf("stderr %q should name the env", errb)
			}
		})
	}
}

func TestLoadDoesNotRetryOnUnrelatedBuildError(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "docker.log")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeBuildxDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := sourceDockerBuild(t, []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_LOG=" + logPath,
		"FAKE_BUILDX_ERR=ERROR: failed to solve: base name should not be blank\n",
	}, "", `proveo_docker_build -t proveo/base:local /tmp`)
	if err == nil {
		t.Fatal("blank FROM must still fail")
	}
	body, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	calls := string(body)
	if strings.Contains(calls, "--pull=false") {
		t.Fatalf("non-DNS failure must not retry with --pull=false\n%s", calls)
	}
	builds := 0
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "buildx build ") {
			builds++
		}
	}
	if builds != 1 {
		t.Fatalf("buildx build calls = %d, want 1\n%s", builds, calls)
	}
}

func TestLoadPullNeverSkipsTheRegistryHEAD(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "docker.log")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeBuildxDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := sourceDockerBuild(t, []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_LOG=" + logPath,
		"FAKE_BUILDX_ERR=" + hubDNSErr,
		"PROVEO_DOCKER_PULL=0",
	}, "", `proveo_docker_build -t proveo/base:local /tmp`)
	if err != nil {
		t.Fatalf("PROVEO_DOCKER_PULL=0 should --pull=false on the first invoke: %v", err)
	}
	body, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	calls := string(body)
	builds := 0
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "buildx build ") {
			builds++
		}
	}
	if builds != 1 {
		t.Fatalf("buildx build calls = %d, want 1\n%s", builds, calls)
	}
	if !strings.Contains(calls, "--pull=false") {
		t.Fatalf("PROVEO_DOCKER_PULL=0 must pass --pull=false\n%s", calls)
	}
}

func TestLoadRetriesWithoutPullOnHubDNS(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "docker.log")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeBuildxDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	out, errb, err := sourceDockerBuild(t, []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_LOG=" + logPath,
		"FAKE_BUILDX_ERR=" + hubDNSErr,
	}, "", `proveo_docker_build -t proveo/base:local /tmp`)
	if err != nil {
		t.Fatalf("retry should succeed with --pull=false: %v\n%s%s", err, out, errb)
	}
	body, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	calls := string(body)
	builds := 0
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "buildx build ") {
			builds++
		}
	}
	if builds != 2 {
		t.Fatalf("buildx build calls = %d, want 2 (fail then --pull=false)\n%s", builds, calls)
	}
	if !strings.Contains(calls, "--pull=false") {
		t.Fatalf("second build must pass --pull=false\n%s", calls)
	}
	combined := out + errb
	if !strings.Contains(combined, "retrying with --pull=false") {
		t.Fatalf("wanted a retry notice, got:\n%s", combined)
	}
}

func TestRegistryDNSHelpNamesTheEscapeHatches(t *testing.T) {
	t.Parallel()
	out, errb, err := sourceDockerBuild(t, nil, "", "proveo_docker_registry_dns_help")
	if err != nil {
		t.Fatalf("%v (%s)", err, errb)
	}
	got := out + errb
	for _, want := range []string{
		"registry-1.docker.io",
		"PROVEO_DOCKER_PULL=0",
		"docker pull docker/sandbox-templates:shell-docker-0.5.0",
		"docker buildx rm proveo-multiarch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("help missing %q\n%s", want, got)
		}
	}
}
