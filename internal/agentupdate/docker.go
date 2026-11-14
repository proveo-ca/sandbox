// SPEC: _spec/_devops/agent-update-prompt.puml
package agentupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	registryTimeout = 6 * time.Second
	upstreamTimeout = 4 * time.Second
)

// New is a Checker wired to the host's docker CLI and the agents' release sources; Ask answers Unasked.
func New(statePath string) Checker {
	b := imagebuild.New("")
	b.Out, b.Fetch = os.Stderr, imagebuild.FetchWithin(upstreamTimeout)
	return Checker{
		Local:     localImage,
		Published: func(ref string) (Image, error) { return publishedImage(ref, imagebuild.HostPlatform(runtime.GOARCH)) },
		Overlays:  overlayTags,
		Upstream:  func(pin imagebuild.Pin) (string, error) { return b.Upstream(pin.Eco, b.PinPkg(pin)) },
		Pull: func(ref string) error {
			c := exec.Command("docker", "pull", ref)
			c.Stdout, c.Stderr = os.Stderr, os.Stderr
			return c.Run()
		},
		Build: func(target, base string, img Image, version, ref string) error {
			return b.Overlay(target, base, img.ID, img.User, version, ref)
		},
		Remove:    func(ref string) error { return exec.Command("docker", "image", "rm", ref).Run() },
		Ask:       func(Offer) Answer { return Unasked },
		Getenv:    os.Getenv,
		Now:       time.Now,
		StatePath: statePath,
		UI:        ui.Default,
	}
}

type imageConfig struct {
	User   string
	Labels map[string]string
}

func (c imageConfig) image(id, created string) (Image, error) {
	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Image{}, fmt.Errorf("image created %q: %w", created, err)
	}
	return Image{ID: id, Created: at, User: c.User,
		Version: c.Labels[imagebuild.AgentVersionLabel], Base: c.Labels[imagebuild.OverlayBaseLabel]}, nil
}

// parseLocal reads one `docker image inspect --format '{{json .}}'` document.
func parseLocal(body []byte) (Image, error) {
	var doc struct {
		ID      string `json:"Id"`
		Created string
		Config  imageConfig
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Image{}, err
	}
	return doc.Config.image(doc.ID, doc.Created)
}

// parsePublished reads `docker buildx imagetools inspect --format '{{json .Image}}'`: one config, or one per platform.
func parsePublished(body []byte, platform string) (Image, error) {
	type config struct {
		Created string      `json:"created"`
		Config  imageConfig `json:"config"`
	}
	var byPlatform map[string]json.RawMessage
	if err := json.Unmarshal(body, &byPlatform); err != nil {
		return Image{}, err
	}
	doc := json.RawMessage(body)
	if _, single := byPlatform["config"]; !single {
		keys := make([]string, 0, len(byPlatform))
		for k := range byPlatform {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return Image{}, errors.New("the registry named no image config")
		}
		pick := keys[0]
		if _, ok := byPlatform[platform]; ok {
			pick = platform
		}
		doc = byPlatform[pick]
	}
	var c config
	if err := json.Unmarshal(doc, &c); err != nil {
		return Image{}, err
	}
	return c.Config.image("", c.Created)
}

func localImage(ref string) (Image, bool) {
	out, err := exec.Command("docker", "image", "inspect", ref, "--format", "{{json .}}").Output()
	if err != nil {
		return Image{}, false
	}
	img, err := parseLocal(out)
	return img, err == nil
}

func publishedImage(ref, platform string) (Image, error) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", ref, "--format", "{{json .Image}}").Output()
	if err != nil {
		return Image{}, err
	}
	return parsePublished(out, platform)
}

func overlayTags(repo string) []string {
	out, err := exec.Command("docker", "image", "ls", "--filter", "reference="+repo+":"+imagebuild.OverlayTagPrefix+"*",
		"--format", "{{.Repository}}:{{.Tag}}").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
