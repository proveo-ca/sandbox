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

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/engine"
	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

type imageDep struct {
	Name   string
	Target string // imagebuild target that can build Name; "" means pull-only
}

type provisioner struct {
	Present func(image string) bool
	Pull    func(image string) error
	Build   func(target, tag string) (string, error)
	Confirm func(question string) bool
	UI      *ui.Printer
}

// buildTag is the tag a fallback build writes: :latest means published, so a
// local build of it lands on :local, which ResolveImage already prefers.
func buildTag(image string) string {
	if tag := imagebuild.RefTag(image); tag != maintain.PublishTag {
		return tag
	}
	return maintain.LocalTag
}

// Ensure makes every dep runnable and returns the images it built in place of
// one that could not be pulled.
func (pv provisioner) Ensure(deps []imageDep) (map[string]string, error) {
	built := map[string]string{}
	seen := map[string]bool{}
	for _, d := range deps {
		if d.Name == "" || seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		if pv.Present(d.Name) {
			continue
		}
		pv.UI.Cloudf("pulling image: %s", d.Name)
		pullErr := pv.Pull(d.Name)
		if pullErr == nil {
			continue
		}
		if d.Target == "" {
			return built, fmt.Errorf("image unavailable: %s (pull failed: %w)", d.Name, pullErr)
		}
		tag := buildTag(d.Name)
		pv.UI.Warnf("pull failed for %s — it can be built locally instead", d.Name)
		if !pv.Confirm(fmt.Sprintf("%s is not available. Build %s from source now (tag %s)?", d.Name, d.Target, tag)) {
			return built, fmt.Errorf("image unavailable: %s — pull failed and build declined; run `proveo build %s --tag %s`, or set PROVEO_AUTO_PROVISION=1 to build without prompting", d.Name, d.Target, tag)
		}
		pv.UI.Appf("building %s", d.Target)
		ref, err := pv.Build(d.Target, tag)
		if err != nil {
			return built, fmt.Errorf("build failed for %s: %w", d.Name, err)
		}
		built[d.Name] = ref
	}
	return built, nil
}

var (
	preflightLookPath = exec.LookPath
	preflightGOOS     = runtime.GOOS
	preflightInfo     = func() error {
		_, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Output()
		return err
	}
	preflightEngine = engine.DetectOffline
)

func ensureDockerUsable() error {
	if _, err := preflightLookPath("docker"); err != nil {
		if preflightGOOS == "darwin" {
			return errors.New("docker CLI not found on PATH — OrbStack users: run 'orb' once to link the docker CLI (or check ~/.orbstack/bin); Colima/Podman/Rancher Desktop users: check that engine's shell setup; Docker Desktop users: install via the app, then retry")
		}
		return errors.New("docker CLI not found on PATH — install docker or add its bin dir to PATH")
	}
	if err := preflightInfo(); err != nil {
		eng := preflightEngine()
		if start := eng.StartHint(); start != "" {
			return fmt.Errorf("docker daemon unreachable — start %s (`%s`), wait for it to come up, then retry", eng.Name(), start)
		}
		if preflightGOOS == "darwin" {
			return errors.New("docker daemon unreachable — start your container engine (OrbStack: `open -a OrbStack`, Colima: `colima start`, Podman: `podman machine start`, or Docker Desktop), wait for it to come up, then retry")
		}
		return fmt.Errorf("docker daemon unreachable (`docker info` failed): %w", err)
	}
	return nil
}

// preflightImages makes the agent image present locally: sbx loads its template from the local store.
func preflightImages(man manifest.Manifest, agentImage string) (string, error) {
	if err := ensureDockerUsable(); err != nil {
		return agentImage, err
	}
	defs := sourceDefsDir()
	deps := []imageDep{{Name: agentImage, Target: buildTarget(defs, man, agentImage)}}
	pv := provisioner{
		Present: func(img string) bool {
			return exec.Command("docker", "image", "inspect", img).Run() == nil
		},
		Pull: func(img string) error {
			c := exec.Command("docker", "pull", img)
			c.Stdout, c.Stderr = os.Stderr, os.Stderr
			return c.Run()
		},
		Build: func(target, tag string) (string, error) {
			b := imagebuild.New(filepath.Dir(defs))
			b.Out = os.Stderr
			if err := b.BuildTarget(target, tag, imagebuild.Options{}); err != nil {
				return "", err
			}
			return b.TargetRef(target, tag)
		},
		Confirm: provisionConfirm,
		UI:      ui.Default,
	}
	built, err := pv.Ensure(deps)
	if err != nil {
		return agentImage, err
	}
	if ref, ok := built[agentImage]; ok {
		return ref, nil
	}
	return agentImage, nil
}

func provisionConfirm(question string) bool {
	switch v := os.Getenv("PROVEO_AUTO_PROVISION"); {
	case envflag.On(v):
		return true
	case envflag.Off(v):
		return false
	}
	if !agentio.IsStdinTTY() || !agentio.WizardEnabled() {
		return false
	}
	return promptYesNo(question, true, os.Stdin, os.Stderr)
}

func sourceDefsDir() string {
	if d := os.Getenv("PROVEO_DEFS_DIR"); d != "" {
		return d
	}
	root := run.OrWD("")
	if ws := workspace.Resolve(root); ws.IsRepo {
		root = ws.Root
	}
	d := filepath.Join(root, "defs")
	if fileExists(filepath.Join(d, "base", "Dockerfile")) {
		return d
	}
	return ""
}

// buildTarget is the imagebuild target that can build image from source.
func buildTarget(defsDir string, man manifest.Manifest, image string) string {
	base, ok := proveoImageBase(image)
	if defsDir == "" || !ok {
		return ""
	}
	if _, ok := imagebuild.Specs[base]; ok {
		return base
	}
	if _, ok := imagebuild.Specs[man.Name]; ok && man.Name != "" {
		return man.Name
	}
	return ""
}

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
