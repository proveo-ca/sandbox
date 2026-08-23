// SPEC: _spec/internal/engine/container-engine.puml
package engine

import (
	"fmt"
	"strings"
	"testing"
)

// stub swaps every seam for the duration of one test.
func stub(t *testing.T, os string, path map[string]bool, env map[string]string, out map[string]string) {
	t.Helper()
	oldLook, oldGoos, oldEnv, oldOut := lookPath, goos, getenv, output
	t.Cleanup(func() { lookPath, goos, getenv, output = oldLook, oldGoos, oldEnv, oldOut })

	goos = os
	lookPath = func(f string) (string, error) {
		if path[f] {
			return "/usr/bin/" + f, nil
		}
		return "", fmt.Errorf("%s: not found", f)
	}
	getenv = func(k string) string { return env[k] }
	output = func(name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		for k, v := range out {
			if strings.HasPrefix(key, k) {
				return []byte(v), nil
			}
		}
		return nil, fmt.Errorf("no stub for %q", key)
	}
}

// ctxOut is what `docker context inspect --format ...` returns.
func ctxOut(name, endpoint string) string { return name + "\t" + endpoint + "\n" }

// infoOut is what `docker info --format {{json .}}` returns.
func infoOut(operatingSystem, name, version string) string {
	return fmt.Sprintf(`{"Name":%q,"ServerVersion":%q,"OperatingSystem":%q}`, name, version, operatingSystem)
}

func TestDetectClassifiesEnginesBySocketAndInfo(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		context     string
		infoOS      string
		infoName    string
		wantKind    Kind
		wantName    string
		wantStart   string
		wantRunning bool
	}{
		{
			name: "orbstack", context: "orbstack", endpoint: "unix:///Users/u/.orbstack/run/docker.sock",
			infoOS: "OrbStack", infoName: "orbstack",
			wantKind: OrbStack, wantName: "OrbStack", wantStart: "open -a OrbStack", wantRunning: true,
		},
		{
			name: "docker desktop", context: "desktop-linux", endpoint: "unix:///Users/u/.docker/run/docker.sock",
			infoOS: "Docker Desktop", infoName: "docker-desktop",
			wantKind: DockerDesktop, wantName: "Docker Desktop", wantStart: "open -a Docker", wantRunning: true,
		},
		{
			name: "colima", context: "colima", endpoint: "unix:///Users/u/.colima/default/docker.sock",
			infoOS: "Ubuntu 24.04.1 LTS", infoName: "colima",
			wantKind: Colima, wantName: "Colima", wantStart: "colima start", wantRunning: true,
		},
		{
			name: "rancher desktop", context: "rancher-desktop", endpoint: "unix:///Users/u/.rd/docker.sock",
			infoOS: "Rancher Desktop WSL Distribution", infoName: "lima-rancher-desktop",
			wantKind: RancherDesktop, wantName: "Rancher Desktop", wantStart: "open -a 'Rancher Desktop'", wantRunning: true,
		},
		{
			name: "podman machine", context: "podman-machine-default", endpoint: "unix:///Users/u/.local/share/containers/podman/machine/podman.sock",
			infoOS: "fedora", infoName: "podman-machine-default",
			wantKind: Podman, wantName: "Podman", wantStart: "podman machine start", wantRunning: true,
		},
		{
			name: "lima", context: "lima-default", endpoint: "unix:///Users/u/.lima/default/sock/docker.sock",
			infoOS: "Ubuntu 24.04", infoName: "lima-default",
			wantKind: Lima, wantName: "Lima", wantStart: "limactl start", wantRunning: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub(t, "darwin", map[string]bool{"docker": true}, nil, map[string]string{
				"docker context inspect": ctxOut(tc.context, tc.endpoint),
				"docker info":            infoOut(tc.infoOS, tc.infoName, "29.4.0"),
			})
			got := Detect()
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", got.Name(), tc.wantName)
			}
			if got.StartHint() != tc.wantStart {
				t.Errorf("StartHint() = %q, want %q", got.StartHint(), tc.wantStart)
			}
			if got.Running != tc.wantRunning {
				t.Errorf("Running = %v, want %v", got.Running, tc.wantRunning)
			}
		})
	}
}

func TestDetectNamesEngineWhenDaemonIsDown(t *testing.T) {
	stub(t, "darwin", map[string]bool{"docker": true, "orb": true}, nil, map[string]string{
		"docker context inspect": ctxOut("orbstack", "unix:///Users/u/.orbstack/run/docker.sock"),
		// no `docker info` stub: the daemon does not answer
	})
	got := Detect()
	if got.Kind != OrbStack {
		t.Fatalf("Kind = %q, want %q", got.Kind, OrbStack)
	}
	if got.Running {
		t.Error("Running = true, want false when the daemon does not answer")
	}
	if got.StartHint() != "open -a OrbStack" {
		t.Errorf("StartHint() = %q, want the OrbStack start command", got.StartHint())
	}
}

func TestDetectIgnoresNonActiveContexts(t *testing.T) {
	stub(t, "darwin", map[string]bool{"docker": true}, nil, map[string]string{
		"docker context inspect": ctxOut("orbstack", "unix:///Users/u/.orbstack/run/docker.sock"),
		"docker info":            infoOut("OrbStack", "orbstack", "29.4.0"),
	})
	if got := Detect(); got.Kind != OrbStack {
		t.Errorf("Kind = %q, want %q (a stale desktop-linux context must not win)", got.Kind, OrbStack)
	}
}

func TestDetectFallsBackToDaemonSelfDescription(t *testing.T) {
	stub(t, "linux", map[string]bool{"docker": true}, map[string]string{
		"DOCKER_HOST": "tcp://10.0.0.9:2375",
	}, map[string]string{
		"docker info": infoOut("Docker Desktop", "docker-desktop", "29.4.0"),
	})
	got := Detect()
	if got.Context != "" {
		t.Errorf("Context = %q, want empty when DOCKER_HOST wins", got.Context)
	}
	if got.Kind != DockerDesktop {
		t.Errorf("Kind = %q, want %q from `docker info`", got.Kind, DockerDesktop)
	}
}

func TestDetectTreatsUnrecognisedRunningDaemonAsDockerEngine(t *testing.T) {
	stub(t, "linux", map[string]bool{"docker": true}, nil, map[string]string{
		"docker context inspect": ctxOut("default", "unix:///run/user/1000/docker.sock"),
		"docker info":            infoOut("Arch Linux", "archbox", "29.4.0"),
	})
	got := Detect()
	if got.Kind != DockerEngine {
		t.Errorf("Kind = %q, want %q", got.Kind, DockerEngine)
	}
	if got.Name() != "Docker Engine" {
		t.Errorf("Name() = %q, want %q", got.Name(), "Docker Engine")
	}
	if got.StartHint() != "" {
		t.Errorf("StartHint() = %q, want empty rather than a guess", got.StartHint())
	}
}

func TestDetectReportsMissingCLI(t *testing.T) {
	stub(t, "darwin", nil, nil, nil)
	got := Detect()
	if got.HasCLI {
		t.Error("HasCLI = true, want false")
	}
	if got.Kind != Unknown {
		t.Errorf("Kind = %q, want %q", got.Kind, Unknown)
	}
	if got.Name() != "no docker CLI" {
		t.Errorf("Name() = %q, want %q", got.Name(), "no docker CLI")
	}
}

func TestEngineVersionPrefersTheEngineCLI(t *testing.T) {
	stub(t, "darwin", map[string]bool{"docker": true, "orb": true}, nil, map[string]string{
		"docker context inspect": ctxOut("orbstack", "unix:///Users/u/.orbstack/run/docker.sock"),
		"docker info":            infoOut("OrbStack", "orbstack", "29.4.0"),
		"orb version":            "Version: 2.2.3 (2020300)\nCommit: c83556b (v2.2.3)\n",
	})
	got := Detect()
	if got.Version != "2.2.3" {
		t.Errorf("Version = %q, want the OrbStack app version 2.2.3", got.Version)
	}
	if got.Label() != "OrbStack 2.2.3" {
		t.Errorf("Label() = %q, want %q", got.Label(), "OrbStack 2.2.3")
	}
}

func TestLabelFallsBackToServerVersionWithoutEngineCLI(t *testing.T) {
	stub(t, "darwin", map[string]bool{"docker": true}, nil, map[string]string{
		"docker context inspect": ctxOut("orbstack", "unix:///Users/u/.orbstack/run/docker.sock"),
		"docker info":            infoOut("OrbStack", "orbstack", "29.4.0"),
	})
	if got := Detect().Label(); got != "OrbStack 29.4.0" {
		t.Errorf("Label() = %q, want %q", got, "OrbStack 29.4.0")
	}
}

func TestIsolationIsSharedKernelForEveryEngine(t *testing.T) {
	for _, k := range []Kind{OrbStack, DockerDesktop, Colima, RancherDesktop, Podman, Lima, DockerEngine, Unknown} {
		if got := (Info{Kind: k}).Isolation(); !strings.Contains(got, "not a microVM boundary") {
			t.Errorf("Isolation() for %q = %q, want the shared-kernel caveat", k, got)
		}
	}
}

func TestDetectOfflineNeverCallsDockerInfo(t *testing.T) {
	var infoCalls int
	stub(t, "darwin", map[string]bool{"docker": true, "orb": true}, nil, map[string]string{
		"docker context inspect": ctxOut("orbstack", "unix:///Users/u/.orbstack/run/docker.sock"),
		"orb version":            "Version: 2.2.3 (2020300)\n",
	})
	inner := output
	output = func(name string, args ...string) ([]byte, error) {
		if name == "docker" && len(args) > 0 && args[0] == "info" {
			infoCalls++
		}
		return inner(name, args...)
	}

	got := DetectOffline()
	if infoCalls != 0 {
		t.Errorf("DetectOffline() called `docker info` %d times, want 0", infoCalls)
	}
	if got.Kind != OrbStack || got.Label() != "OrbStack 2.2.3" {
		t.Errorf("DetectOffline() = %q/%q, want orbstack/\"OrbStack 2.2.3\"", got.Kind, got.Label())
	}
	if got.Running {
		t.Error("Running = true, want false — DetectOffline never confirms the daemon")
	}
}

func TestDetectRefinedKindStillPrefersEngineCLIVersion(t *testing.T) {
	stub(t, "linux", map[string]bool{"docker": true, "colima": true}, map[string]string{
		"DOCKER_HOST": "tcp://127.0.0.1:2375",
	}, map[string]string{
		"docker info":    infoOut("Ubuntu 24.04", "colima", "29.4.0"),
		"colima version": "colima version 0.8.4\n",
	})
	got := Detect()
	if got.Kind != Colima {
		t.Fatalf("Kind = %q, want %q", got.Kind, Colima)
	}
	if got.Version != "0.8.4" {
		t.Errorf("Version = %q, want the Colima CLI version 0.8.4", got.Version)
	}
}
