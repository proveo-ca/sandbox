// SPEC: _spec/_devops/agent-update-prompt.puml
// Package agentupdate offers a newer agent for the image a run settled on, and applies the answer.
package agentupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/ui"
)

// EnvSwitch updates without asking when on, and turns the check off when off.
const EnvSwitch = "PROVEO_AGENT_UPDATE"

const (
	freshFor   = 24 * time.Hour // how long one registry and upstream answer is reused
	retryAfter = time.Hour      // how soon a check that reached nothing runs again
)

// Image is what the check reads from one image's config.
type Image struct {
	ID      string
	Created time.Time
	Version string // the agent release the image carries
	Base    string // the image id an overlay was built on; "" on a built image
	User    string
}

// Answer is what the operator does about an offer.
type Answer int

const (
	Unasked Answer = iota // no terminal took the question
	Later
	Update
	Skip
)

// Offer is one update the operator can take.
type Offer struct {
	Target   string   // the harness
	From, To string   // agent releases
	Steps    []string // what Update runs, in order
}

// Facts are what PlanFor decides from.
type Facts struct {
	Eco      string
	Base     Image  // the tag the run resolved
	Carried  string // the release the run launches: an overlay's, else Base.Version
	Remote   *Image // the published image; nil when unknown
	Upstream string // the agent's current release; "" when unknown
	Overlay  bool   // the agent can be installed over a built image
}

// Plan is what an accepted update runs.
type Plan struct {
	Pull    bool
	Overlay string // release to install over the base; "" for none
	Target  string // release the run carries afterwards
	Behind  string // upstream release neither step reaches; "" for none
}

// Offered reports whether the plan changes the agent the run carries.
func (p Plan) Offered() bool { return p.Pull || p.Overlay != "" }

// PlanFor is the shortest way from the carried release to the newest one reachable.
func PlanFor(f Facts) Plan {
	var p Plan
	after := f.Base.Version
	if f.Remote != nil && f.Remote.Created.After(f.Base.Created) && imagebuild.Newer(f.Eco, f.Remote.Version, after) {
		p.Pull, after = true, f.Remote.Version
	}
	if imagebuild.Newer(f.Eco, f.Upstream, after) {
		if f.Overlay {
			p.Overlay, after = f.Upstream, f.Upstream
		} else {
			p.Behind = f.Upstream
		}
	}
	p.Target = after
	if !imagebuild.Newer(f.Eco, p.Target, f.Carried) {
		p.Pull, p.Overlay = false, ""
	}
	return p
}

// Checker holds everything a check reads from and does to its surroundings.
type Checker struct {
	Local     func(ref string) (Image, bool)
	Published func(ref string) (Image, error)
	Overlays  func(repo string) []string
	Upstream  func(pin imagebuild.Pin) (string, error)
	Pull      func(ref string) error
	Build     func(target, base string, img Image, version, ref string) error
	Remove    func(ref string) error
	Ask       func(Offer) Answer
	Getenv    func(string) string
	Now       func() time.Time
	StatePath string
	UI        *ui.Printer
}

type published struct {
	Created time.Time `json:"created"`
	Version string    `json:"version"`
}

type entry struct {
	Checked  time.Time  `json:"checked"`
	Remote   *published `json:"remote,omitempty"`
	Upstream string     `json:"upstream,omitempty"`
	Later    time.Time  `json:"later"`
	Skip     string     `json:"skip,omitempty"`
	Noticed  string     `json:"noticed,omitempty"`
}

type state struct {
	Images map[string]*entry `json:"images"`
}

func load(path string) state {
	var st state
	if body, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(body, &st)
	}
	if st.Images == nil {
		st.Images = map[string]*entry{}
	}
	return st
}

func (st state) save(path string) {
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, append(body, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// PublishedRef is the registry image ref is checked against; "" for a ref a digest pins.
func PublishedRef(ref string) string {
	if strings.Contains(ref, "@") {
		return ""
	}
	if tag := imagebuild.RefTag(ref); tag == "local" || tag == "latest" {
		return imagebuild.RefRepo(ref) + ":latest"
	}
	return ref
}

// Settle returns the image the run launches: ref, the overlay built on it, or what an accepted update made.
func (c Checker) Settle(target, ref string) string {
	base, ok := c.Local(ref)
	if !ok {
		return ref
	}
	run, carried := c.overlayOf(ref, base)
	_, pin, ok := imagebuild.AgentSpec(target)
	mode := c.Getenv(EnvSwitch)
	remoteRef := PublishedRef(ref)
	if !ok || base.Version == "" || remoteRef == "" || envflag.Off(mode) {
		return run
	}
	repo, now := imagebuild.RefRepo(ref), c.Now()
	st := load(c.StatePath)
	e := st.Images[repo]
	if e == nil {
		e = &entry{}
		st.Images[repo] = e
	}
	if now.Before(e.Later) {
		return run
	}
	if now.Sub(e.Checked) >= freshFor {
		c.refresh(e, pin, remoteRef, now)
		st.save(c.StatePath)
	}
	f := Facts{Eco: pin.Eco, Base: base, Carried: carried, Upstream: e.Upstream, Overlay: pin.Overlay != ""}
	if e.Remote != nil {
		f.Remote = &Image{Created: e.Remote.Created, Version: e.Remote.Version}
	}
	plan := PlanFor(f)
	if plan.Behind != "" && e.Noticed != plan.Behind {
		c.UI.Notef("%s %s is out — %s carries %s until its image is republished", target, plan.Behind, repo, plan.Target)
		e.Noticed = plan.Behind
		st.save(c.StatePath)
	}
	if !plan.Offered() || e.Skip == plan.Target {
		return run
	}
	offer := Offer{Target: target, From: carried, To: plan.Target, Steps: steps(plan, f, ref, remoteRef)}
	answer := Update
	if !envflag.On(mode) {
		answer = c.Ask(offer)
	}
	switch answer {
	case Update:
		return c.apply(target, ref, run, base, pin.Eco, plan, remoteRef)
	case Later:
		e.Later = now.Add(freshFor)
	case Skip:
		e.Skip = plan.Target
	default:
		c.UI.Warnf("%s %s → %s is available — answer it in a terminal, or set %s=1 to update without asking",
			target, offer.From, offer.To, EnvSwitch)
		return run
	}
	st.save(c.StatePath)
	return run
}

// steps names what an accepted plan runs, in order.
func steps(p Plan, f Facts, ref, remoteRef string) []string {
	var out []string
	base := ref
	if p.Pull {
		out, base = append(out, fmt.Sprintf("docker pull %s (%s)", remoteRef, f.Remote.Version)), remoteRef
	}
	if p.Overlay != "" {
		out = append(out, fmt.Sprintf("install %s on top as %s", p.Overlay, imagebuild.OverlayRef(base, p.Overlay)))
	}
	return out
}

// refresh asks the registry and the agent's release source at once and records what answered.
func (c Checker) refresh(e *entry, pin imagebuild.Pin, remoteRef string, now time.Time) {
	var (
		wg     sync.WaitGroup
		img    Image
		imgErr error
		up     string
		upErr  error
	)
	wg.Add(2)
	go func() { defer wg.Done(); img, imgErr = c.Published(remoteRef) }()
	go func() { defer wg.Done(); up, upErr = c.Upstream(pin) }()
	wg.Wait()
	e.Checked = now
	if imgErr == nil {
		e.Remote = &published{Created: img.Created, Version: img.Version}
	} else {
		ui.Logf("agent update: %s is not readable from its registry: %v", remoteRef, imgErr)
	}
	if upErr == nil && up != "" {
		e.Upstream = up
	} else {
		ui.Logf("agent update: no upstream release for %s: %v", pin.Pkg, upErr)
	}
	if imgErr != nil && (upErr != nil || up == "") {
		e.Checked = now.Add(retryAfter - freshFor)
	}
}

// overlayOf is the overlay built on base with its release, else ref with base's; overlays of another base are removed.
func (c Checker) overlayOf(ref string, base Image) (run, carried string) {
	run, carried = ref, base.Version
	var newest time.Time
	for _, o := range c.Overlays(imagebuild.RefRepo(ref)) {
		img, ok := c.Local(o)
		switch {
		case !ok || img.Base == "":
		case img.Base != base.ID:
			ui.Logf("agent update: removing %s, built on an image %s no longer names", o, ref)
			_ = c.Remove(o)
		case run == ref || img.Created.After(newest):
			run, carried, newest = o, img.Version, img.Created
		}
	}
	return run, carried
}

// apply runs plan and returns the image it produced, or the image the run already had when a step fails.
func (c Checker) apply(target, ref, run string, base Image, eco string, plan Plan, remoteRef string) string {
	baseRef := ref
	if plan.Pull {
		c.UI.Cloudf("pulling image: %s", remoteRef)
		if err := c.Pull(remoteRef); err != nil {
			c.UI.Warnf("agent update: pulling %s failed (%v) — running the image already here", remoteRef, err)
			return run
		}
		img, ok := c.Local(remoteRef)
		if !ok {
			return run
		}
		baseRef, base, run = remoteRef, img, remoteRef
	}
	keep := ""
	if imagebuild.Newer(eco, plan.Overlay, base.Version) {
		over := imagebuild.OverlayRef(baseRef, plan.Overlay)
		c.UI.Appf("installing %s %s over %s", target, plan.Overlay, baseRef)
		if err := c.Build(target, baseRef, base, plan.Overlay, over); err != nil {
			c.UI.Warnf("agent update: installing %s %s failed (%v) — running %s", target, plan.Overlay, err, run)
			return run
		}
		run, keep = over, over
	}
	for _, o := range c.Overlays(imagebuild.RefRepo(ref)) {
		if img, ok := c.Local(o); ok && img.Base != "" && o != keep {
			_ = c.Remove(o)
		}
	}
	return run
}
