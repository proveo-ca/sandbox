// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/_devops/agent-update-prompt.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/agentupdate"
	"github.com/proveo-ca/proveo/internal/choiceui"
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
	Check  func(string) error
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
		var cachedErr error
		if pv.Present(d.Name) {
			cachedErr = d.check(d.Name)
			if cachedErr == nil {
				continue
			}
		}
		pv.UI.Cloudf("pulling image: %s", d.Name)
		pullErr := pv.Pull(d.Name)
		if pullErr == nil {
			if err := d.check(d.Name); err != nil {
				return built, err
			}
			continue
		}
		if d.Target == "" {
			return built, errors.Join(cachedErr, fmt.Errorf("image unavailable: %s (pull failed: %w)", d.Name, pullErr))
		}
		tag := buildTag(d.Name)
		pv.UI.Warnf("pull failed for %s — it can be built locally instead", d.Name)
		if !pv.Confirm(fmt.Sprintf("%s is not available. Build %s from source now (tag %s)?", d.Name, d.Target, tag)) {
			return built, fmt.Errorf("image unavailable: %s — pull failed and build declined; re-run and accept the build, or set PROVEO_AUTO_PROVISION=1 to build without prompting", d.Name)
		}
		pv.UI.Appf("building %s", d.Target)
		ref, err := pv.Build(d.Target, tag)
		if err != nil {
			return built, fmt.Errorf("build failed for %s: %w", d.Name, err)
		}
		if err := d.check(ref); err != nil {
			return built, err
		}
		built[d.Name] = ref
	}
	return built, nil
}

func (d imageDep) check(ref string) error {
	if d.Check == nil {
		return nil
	}
	return d.Check(ref)
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
func preflightImages(man manifest.Manifest, request run.ImageRequest) (string, error) {
	if err := ensureDockerUsable(); err != nil {
		return request.Ref, err
	}
	defs := sourceDefsDir()
	dep := imageDep{Name: request.Ref, Target: buildTarget(defs, man, request.Ref)}
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
	tag := maintain.RefTag(request.Ref)
	preferLocal := request.Browser && !request.Explicit && (tag == maintain.LocalTag || tag == maintain.PublishTag)
	var browserCheck func(string) error
	if request.Browser {
		browserCheck = newBrowserImageCheck()
	}
	if preferLocal {
		dep.Check = browserCheck
	}
	agentImage, err := pv.ensureAgentImage(dep, preferLocal)
	if err != nil {
		return request.Ref, err
	}
	settled := settleAgent(man.Name, agentImage)
	if browserCheck != nil {
		if err := browserCheck(settled); err != nil {
			if preferLocal && settled != agentImage && browserCheck(agentImage) == nil {
				pv.UI.Warnf("%v. Using %s", err, agentImage)
				return agentImage, nil
			}
			return settled, err
		}
	}
	return settled, nil
}

func (pv provisioner) ensureAgentImage(dep imageDep, preferLocal bool) (string, error) {
	var localErr error
	if preferLocal {
		repo := imagebuild.RefRepo(dep.Name)
		local := repo + ":" + maintain.LocalTag
		dep.Name = repo + ":" + maintain.PublishTag
		if pv.Present(local) {
			localErr = dep.check(local)
			if localErr == nil {
				return local, nil
			}
			pv.UI.Warnf("%v. Trying %s", localErr, dep.Name)
		}
	}
	built, err := pv.Ensure([]imageDep{dep})
	if err != nil {
		return "", errors.Join(localErr, err)
	}
	if ref, ok := built[dep.Name]; ok {
		return ref, nil
	}
	return dep.Name, nil
}

func newBrowserImageCheck() func(string) error {
	var checkedID string
	return func(ref string) error {
		out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", ref).CombinedOutput()
		if err != nil {
			return fmt.Errorf("inspect browser image %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
		}
		id := strings.TrimSpace(string(out))
		if id != "" && id == checkedID {
			return nil
		}
		out, err = exec.Command("docker", "run", "--rm", "--pull=never", "--network", "none",
			"--entrypoint", "/usr/local/bin/proveo-entrypoint", id, "browser", "check").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s failed the isolated browser check: %w: %s", ref, err, strings.TrimSpace(string(out)))
		}
		checkedID = id
		return nil
	}
}

// settleAgent is the image the run launches after the agent-update check.
var settleAgent = func(target, image string) string {
	c := agentupdate.New(filepath.Join(run.StateDir(), "agent-update.json"))
	c.Ask = askAgentUpdate
	return c.Settle(target, image)
}

var onTerminal = func() bool { return agentsettings.IsReaderTTY(os.Stdin) && agentsettings.IsWriterTTY(os.Stdout) }

// attended reports whether an operator is at this run's terminal to answer a question.
var attended = func() bool {
	return os.Getenv("PROVEO_SCHEDULE") == "" && agentsettings.WizardEnabled() && onTerminal()
}

// askAgentUpdate puts an offer to the operator's terminal; Unasked when nobody is there to answer.
func askAgentUpdate(o agentupdate.Offer) agentupdate.Answer {
	if !attended() {
		return agentupdate.Unasked
	}
	answer, asked := askAgentUpdateDialog(o.Target, o.From, o.To, o.Steps)
	switch {
	case !asked:
		return agentupdate.Unasked
	case answer == choiceui.AgentUpdateNow:
		return agentupdate.Update
	case answer == choiceui.AgentUpdateSkip:
		return agentupdate.Skip
	}
	return agentupdate.Later
}

var askAgentUpdateDialog = choiceui.AskAgentUpdate

func provisionConfirm(question string) bool {
	switch v := os.Getenv("PROVEO_AUTO_PROVISION"); {
	case envflag.On(v):
		return true
	case envflag.Off(v):
		return false
	}
	if !agentsettings.IsStdinTTY() || !agentsettings.WizardEnabled() {
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
