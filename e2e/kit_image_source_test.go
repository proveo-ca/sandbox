//go:build e2e

// SPEC: _spec/internal/sbx/kit-image-source.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// containerBuilder is a running docker-container buildx builder: one that cannot read the host store.
const containerBuilder = "proveo-multiarch"

// TestAHostOnlyImageBuildsWhileAContainerBuilderIsActive builds FROM an image only the host engine
// holds while the inherited BUILDX_BUILDER names a docker-container builder.
func TestAHostOnlyImageBuildsWhileAContainerBuilderIsActive(t *testing.T) {
	requireSbx(t)
	base := harnessImage(t, "codex")
	if out, err := exec.Command("docker", "buildx", "inspect", containerBuilder).CombinedOutput(); err != nil ||
		!strings.Contains(string(out), "docker-container") {
		t.Skipf("no %s docker-container builder to stand in for the active one (`mise run deploy` creates it)", containerBuilder)
	}
	hostOnly := fmt.Sprintf("proveo-e2e/hostonly-%d:local", time.Now().UnixNano())
	if out, err := exec.Command("docker", "tag", base, hostOnly).CombinedOutput(); err != nil {
		t.Fatalf("docker tag %s %s: %v\n%s", base, hostOnly, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", hostOnly).Run() })

	src := sbx.ResolveImageSource(hostOnly, imagebuild.New("").HostBuilder)
	if !src.Host {
		t.Fatalf("ResolveImageSource(%q) = %+v; the tag was just made on the host", hostOnly, src)
	}
	if src.Builder == "" {
		t.Skipf("no docker-driver buildx builder on this host reads its store: %+v", src)
	}
	ambient := append(os.Environ(), "BUILDX_BUILDER="+containerBuilder)

	t.Run("the source's builder and digest build it", func(t *testing.T) {
		out, err := createProbe(t, src.From(), sbx.CreateEnv(ambient, sbx.RunConfig{HostEnv: src.BuildEnv()}))
		if err != nil {
			t.Errorf("sbx create FROM %s with HostEnv %q under BUILDX_BUILDER=%s: %v\n%s",
				src.From(), src.BuildEnv(), containerBuilder, err, out)
		}
	})
	if src.Digest == "" {
		return
	}
	t.Run("the pinned digest refuses a builder that cannot see the host store", func(t *testing.T) {
		out, err := createProbe(t, src.From(), ambient)
		if err == nil {
			t.Errorf("sbx create FROM %s built under the container builder alone; want a loud failure, never a silent docker.io image:\n%s",
				src.From(), out)
		}
	})
}

// createProbe runs `sbx create` on a minimal workload FROM from, with env on the sbx process.
func createProbe(t *testing.T, from string, env []string) (string, error) {
	t.Helper()
	sweepSandboxesAfter(t)
	name := fmt.Sprintf("probe-src-%d", time.Now().UnixNano())
	kit, err := sbx.WriteWorkloadV3(filepath.Join(t.TempDir(), name), sbx.WorkloadV3{
		Capabilities: []sbx.CapabilityV3{{Type: sbx.CapSbx}},
		Version:      "0.0.1",
	}, sbx.RecipeV3{From: from, Entrypoint: []string{"bash"}}, nil)
	if err != nil {
		t.Fatalf("WriteWorkloadV3: %v", err)
	}
	t.Cleanup(func() { _, _ = removeSandbox(t, name) })
	c := exec.Command(sbx.Binary, sbx.CreateArgs(sbx.RunConfig{Name: name, KitDir: kit})...)
	c.Env = env
	out, err := c.CombinedOutput()
	return string(out), err
}
