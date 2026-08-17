// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/_devops/image-lineage-and-publish.puml
// Image provisioning: the preflight installs missing dependencies instead of
// only failing fast. Distribution model: `mise build` builds the proveo/*
// images locally, `mise deploy` pushes them to Docker Hub, and consumers pull
// them — so every missing image is pulled first (the consumer path, same as
// the distributed CLI's ensure_image_available). When the pull fails (offline,
// unpublished tag, maintainer iterating pre-publish) and a source checkout is
// available, the preflight offers to run the def's build.sh instead, gated by
// the same wizard pattern as the missing-env prompt (TTY-only,
// PROVEO_AUTO_PROVISION short-circuit, decline keeps an actionable failure).
// The enforcement images are the egress trust root: production should pin
// digests via the PROVEO_*_IMAGE overrides rather than float on :latest.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// Ensure makes every dep's image available, in order, before any network or
// container exists — a missing image still fails fast, it just tries to
// install first: pull (the consumer path), then a confirmed local build when
// the pull fails and a source checkout is present. Duplicates are checked once.
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

// preflightImages readies every image the run needs: the plan's sidecars plus
// the agent image itself.
func preflightImages(plan egress.Plan, man manifest.Manifest, agentImage string) error {
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

// provisionConfirm decides whether to build a missing proveo/* image:
// PROVEO_AUTO_PROVISION forces yes/no, else ask on a TTY (default yes —
// declining just reproduces the failure the prompt is trying to avoid).
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

// sourceDefsDir locates a defs/ tree the preflight may build from:
// PROVEO_DEFS_DIR (dev iteration), else the enclosing repo checkout when it
// carries the sidecar build scripts. "" means no source tree — pull-only.
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

// harnessBuildScript maps the agent image to its def's build.sh via the
// manifest (the def dir is named after the manifest, ), covering images
// whose name differs from the def (e.g. proveo/cecli-node -> defs/cecli).
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
