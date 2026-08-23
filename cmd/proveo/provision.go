// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/_devops/image-lineage-and-publish.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// imageDep is one image the run needs, with its build script when the image is
// a locally-built proveo/* one and a source checkout is present ("" otherwise).
type imageDep struct {
	Name        string
	BuildScript string
}

// provisioner holds the injectable actions so Ensure's decision logic is
// unit-testable without Docker or a terminal.
type provisioner struct {
	Present func(image string) bool
	Pull    func(image string) error
	Build   func(script string) error
	Confirm func(question string) bool
	UI      *ui.Printer
}

func (pv provisioner) Ensure(deps []imageDep) error {
	seen := map[string]bool{}
	for _, d := range deps {
		if d.Name == "" || seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		if pv.Present(d.Name) {
			continue
		}
		pv.UI.Iconf("📥", "pulling image: %s", d.Name)
		pullErr := pv.Pull(d.Name)
		if pullErr == nil {
			continue
		}
		if d.BuildScript == "" {
			return fmt.Errorf("image unavailable: %s (pull failed: %w)", d.Name, pullErr)
		}
		pv.UI.Warnf("pull failed for %s — it can be built locally instead", d.Name)
		if !pv.Confirm(fmt.Sprintf("%s is not available. Build it now via %s?", d.Name, d.BuildScript)) {
			return fmt.Errorf("image unavailable: %s — pull failed and build declined; run %s, or set PROVEO_AUTO_PROVISION=1 to build without prompting", d.Name, d.BuildScript)
		}
		pv.UI.Iconf("🔨", "building %s", d.Name)
		if err := pv.Build(d.BuildScript); err != nil {
			return fmt.Errorf("build failed for %s: %w", d.Name, err)
		}
	}
	return nil
}

// Test seams for the docker preflight.
var (
	preflightLookPath = exec.LookPath
	preflightGOOS     = runtime.GOOS
	preflightInfo     = func() error {
		_, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Output()
		return err
	}
)

// ensureDockerUsable failfasts with an OS-specific hint before any image
// operation, so a mac whose CLI is off-PATH or whose OrbStack/Docker Desktop VM
// is not running gets an actionable message instead of a raw pull failure.
func ensureDockerUsable() error {
	if _, err := preflightLookPath("docker"); err != nil {
		if preflightGOOS == "darwin" {
			return errors.New("docker CLI not found on PATH — OrbStack users: run 'orb' once to link the docker CLI (or check ~/.orbstack/bin), Docker Desktop users: install via the app, then retry")
		}
		return errors.New("docker CLI not found on PATH — install docker or add its bin dir to PATH")
	}
	if err := preflightInfo(); err != nil {
		if preflightGOOS == "darwin" {
			return errors.New("docker daemon unreachable — start OrbStack (`open -a OrbStack`) or Docker Desktop, wait for it to come up, then retry")
		}
		return fmt.Errorf("docker daemon unreachable (`docker info` failed): %w", err)
	}
	return nil
}

// preflightImages readies every image the run needs: the plan's sidecars plus
// the agent image itself.
func preflightImages(plan egress.Plan, man manifest.Manifest, agentImage string) error {
	if err := ensureDockerUsable(); err != nil {
		return err
	}
	defs := sourceDefsDir()
	var deps []imageDep
	for _, img := range plan.Images {
		deps = append(deps, imageDep{Name: img, BuildScript: sidecarBuildScript(defs, img)})
	}
	deps = append(deps, imageDep{Name: agentImage, BuildScript: harnessBuildScript(defs, man, agentImage)})

	quiet := egress.ExecRunner{} // inspect: a non-zero exit IS the answer
	pv := provisioner{
		Present: func(img string) bool {
			_, err := quiet.Run("image", "inspect", img)
			return err == nil
		},
		// Pull/build progress streams to stderr so stdout stays machine-clean.
		Pull: func(img string) error {
			c := exec.Command("docker", "pull", img)
			c.Stdout, c.Stderr = os.Stderr, os.Stderr
			return c.Run()
		},
		Build: func(script string) error {
			c := exec.Command("bash", script)
			c.Dir = filepath.Dir(script)
			c.Stdout, c.Stderr = os.Stderr, os.Stderr
			return c.Run()
		},
		Confirm: provisionConfirm,
		UI:      ui.Default,
	}
	return pv.Ensure(deps)
}

func provisionConfirm(question string) bool {
	switch strings.ToLower(os.Getenv("PROVEO_AUTO_PROVISION")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	if !isStdinTTY() || !wizardEnabled() {
		return false
	}
	return promptYesNo("🔨 "+question, true, os.Stdin, os.Stderr)
}

func sourceDefsDir() string {
	if d := os.Getenv("PROVEO_DEFS_DIR"); d != "" {
		return d
	}
	root := orWD("")
	if ws := workspace.Resolve(root); ws.IsRepo {
		root = ws.Root
	}
	d := filepath.Join(root, "defs")
	if fileExists(filepath.Join(d, "sidecars", "egress-proxy", "build.sh")) {
		return d
	}
	return ""
}

// sidecarBuildScript maps a proveo/* sidecar image to defs/sidecars/<name>/build.sh.
func sidecarBuildScript(defsDir, image string) string {
	base, ok := proveoImageBase(image)
	if defsDir == "" || !ok {
		return ""
	}
	if s := filepath.Join(defsDir, "sidecars", base, "build.sh"); fileExists(s) {
		return s
	}
	return ""
}

func harnessBuildScript(defsDir string, man manifest.Manifest, agentImage string) string {
	if _, ok := proveoImageBase(agentImage); defsDir == "" || man.Name == "" || !ok {
		return ""
	}
	if s := filepath.Join(defsDir, man.Name, "build.sh"); fileExists(s) {
		return s
	}
	return ""
}

// proveoImageBase returns the name segment of a locally-built proveo/* image
// ("proveo/egress-proxy:latest" -> "egress-proxy").
func proveoImageBase(image string) (string, bool) {
	base, ok := strings.CutPrefix(image, "proveo/")
	if !ok {
		return "", false
	}
	base, _, _ = strings.Cut(base, ":")
	return base, base != ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
