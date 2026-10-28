//go:build e2e

// SPEC: _spec/internal/sbx/credential-resolution.puml (R11, R12)

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
	"github.com/proveo-ca/proveo/internal/secretref"
)

const refProbeService = "proveo-e2e-ref"

// TestAReferenceReachesTheSandboxAsAScopedSbxSource stores a cmd: reference the way a run does and reads the header sbx's proxy injects.
func TestAReferenceReachesTheSandboxAsAScopedSbxSource(t *testing.T) {
	requireSbx(t)
	binds, err := sbx.LoadBindings(sbx.BindingsPath(os.Getenv))
	if err != nil || !binds[refProbeService].APIKey {
		t.Skipf("approve the probe service once: under `bindings:` in %s add `%s: {apiKey: {domains: [httpbin.org]}}`",
			sbx.BindingsPath(os.Getenv), refProbeService)
	}
	src := sbx.ResolveImageSource(harnessImage(t, "codex"), imagebuild.New("").HostBuilder)
	sweepSandboxesAfter(t)
	name := fmt.Sprintf("proveo-e2e-ref-%d", time.Now().UnixNano())
	const want = "e2e-ref-value"

	flags, stdin := secretref.SbxSource("E2E_REF_KEY", "cmd:printf "+want, os.Getenv)
	if err := sbx.SecretSetSource(refProbeService, name, flags, stdin); err != nil {
		t.Fatalf("sbx secret set %s --sandbox %s %v: %v", refProbeService, name, flags, err)
	}
	t.Cleanup(func() {
		_ = exec.Command(sbx.Binary, "secret", "rm", refProbeService, "--sandbox", name, "-f").Run()
	})

	caps, err := sbx.WithRuntimeAllow([]sbx.CapabilityV3{
		{Type: sbx.CapSbx},
		{Type: sbx.CapCredential, Optional: true, Config: sbx.CredentialV3{Service: refProbeService, Phase: "runtime",
			APIKey: &sbx.APIKeyV3{Name: "E2E_REF_KEY", ProxyManaged: true,
				Inject: []sbx.CredInjectV3{{Domain: "httpbin.org", Header: "Authorization", Format: "Bearer %s"}}}}},
	}, []string{"httpbin.org"})
	if err != nil {
		t.Fatal(err)
	}
	kit, err := sbx.WriteWorkloadV3(filepath.Join(t.TempDir(), name),
		sbx.WorkloadV3{Capabilities: caps, Version: "0.0.1"},
		sbx.RecipeV3{From: src.From(), Entrypoint: []string{"bash"}}, nil)
	if err != nil {
		t.Fatalf("WriteWorkloadV3: %v", err)
	}
	t.Cleanup(func() { _, _ = removeSandbox(t, name) })
	c := exec.Command(sbx.Binary, sbx.CreateArgs(sbx.RunConfig{Name: name, KitDir: kit})...)
	c.Env = sbx.CreateEnv(os.Environ(), sbx.RunConfig{HostEnv: src.BuildEnv()})
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("sbx create %s: %v\n%s", name, err, out)
	}

	out, _ := sbxExecStatus(t, name, `printf 'env=%s\n' "$E2E_REF_KEY"; curl -s --max-time 20 https://httpbin.org/headers`, 2*time.Minute)
	if !strings.Contains(out, `"Bearer `+want+`"`) {
		t.Errorf("httpbin saw no Authorization carrying the source's value; sbx did not resolve the reference:\n%s", out)
	}
	if strings.Contains(out, "env="+want) {
		t.Errorf("the resolved value reached the sandbox env; it must hold only sbx's sentinel:\n%s", out)
	}

	if _, err := removeSandbox(t, name); err != nil {
		t.Fatalf("sbx rm %s: %v", name, err)
	}
	ls, _ := exec.Command(sbx.Binary, "secret", "ls").CombinedOutput()
	for _, line := range strings.Split(string(ls), "\n") {
		if strings.HasPrefix(line, name+" ") {
			t.Errorf("a secret scoped to %s outlived the sandbox (P0 M5 measured it leaves with it): %s", name, line)
		}
	}
}
