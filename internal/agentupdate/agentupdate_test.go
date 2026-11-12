// SPEC: _spec/_devops/agent-update-prompt.puml
package agentupdate

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/ui"
)

var day0 = time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)

func at(days int) time.Time { return day0.AddDate(0, 0, days) }

func TestPlanForTakesTheShortestWayToTheNewestRelease(t *testing.T) {
	t.Parallel()
	local := Image{ID: "sha256:base", Created: at(-14), Version: "2.1.312"}
	for _, c := range []struct {
		name string
		f    Facts
		want Plan
	}{
		{"the registry carries a newer agent: pull",
			Facts{Eco: "npm", Base: local, Carried: "2.1.312", Remote: &Image{Created: at(0), Version: "2.1.322"}, Upstream: "2.1.322", Overlay: true},
			Plan{Pull: true, Target: "2.1.322"}},
		{"upstream is ahead of the registry too: pull, then install on top",
			Facts{Eco: "npm", Base: local, Carried: "2.1.312", Remote: &Image{Created: at(0), Version: "2.1.322"}, Upstream: "2.1.330", Overlay: true},
			Plan{Pull: true, Overlay: "2.1.330", Target: "2.1.330"}},
		{"the registry holds what is here: install on top",
			Facts{Eco: "npm", Base: local, Carried: "2.1.312", Remote: &Image{Created: at(-14), Version: "2.1.312"}, Upstream: "2.1.330", Overlay: true},
			Plan{Overlay: "2.1.330", Target: "2.1.330"}},
		{"the registry is unreachable and upstream answers: install on top",
			Facts{Eco: "npm", Base: local, Carried: "2.1.312", Upstream: "2.1.330", Overlay: true},
			Plan{Overlay: "2.1.330", Target: "2.1.330"}},
		{"nothing answered: nothing offered",
			Facts{Eco: "npm", Base: local, Carried: "2.1.312", Overlay: true},
			Plan{Target: "2.1.312"}},
		{"everything agrees: nothing offered",
			Facts{Eco: "npm", Base: local, Carried: "2.1.312", Remote: &Image{Created: at(-14), Version: "2.1.312"}, Upstream: "2.1.312", Overlay: true},
			Plan{Target: "2.1.312"}},
		{"a republished image with the same agent is not an agent update",
			Facts{Eco: "npm", Base: local, Carried: "2.1.312", Remote: &Image{Created: at(0), Version: "2.1.312"}, Upstream: "2.1.312", Overlay: true},
			Plan{Target: "2.1.312"}},
		{"a local build newer than the registry is not pulled over",
			Facts{Eco: "npm", Base: Image{Created: at(1), Version: "2.1.322"}, Carried: "2.1.322", Remote: &Image{Created: at(0), Version: "2.1.322"}, Upstream: "2.1.322", Overlay: true},
			Plan{Target: "2.1.322"}},
		{"an overlay already carries what a pull would bring",
			Facts{Eco: "npm", Base: local, Carried: "2.1.322", Remote: &Image{Created: at(0), Version: "2.1.322"}, Upstream: "2.1.322", Overlay: true},
			Plan{Target: "2.1.322"}},
		{"an overlay is behind a newer release: pull and install again",
			Facts{Eco: "npm", Base: local, Carried: "2.1.322", Remote: &Image{Created: at(0), Version: "2.1.322"}, Upstream: "2.1.330", Overlay: true},
			Plan{Pull: true, Overlay: "2.1.330", Target: "2.1.330"}},
		{"an agent that is its whole image waits for the registry",
			Facts{Eco: "github", Base: Image{Created: at(-14), Version: "v0.23.1"}, Carried: "v0.23.1", Remote: &Image{Created: at(-14), Version: "v0.23.1"}, Upstream: "v0.24.0"},
			Plan{Target: "v0.23.1", Behind: "v0.24.0"}},
		{"and is pulled once the registry has it",
			Facts{Eco: "github", Base: Image{Created: at(-14), Version: "v2026.9.24"}, Carried: "v2026.9.24", Remote: &Image{Created: at(0), Version: "v0.23.1"}, Upstream: "v0.23.1"},
			Plan{Pull: true, Target: "v0.23.1"}},
	} {
		if got := PlanFor(c.f); got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestPublishedRefIsTheRegistryImageARunIsCheckedAgainst(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string]string{
		"proveo/claudecode:latest":              "proveo/claudecode:latest",
		"proveo/claudecode:local":               "proveo/claudecode:latest",
		"proveo/claudecode":                     "proveo/claudecode:latest",
		"registry.example:5000/team/codex:1.2":  "registry.example:5000/team/codex:1.2",
		"proveo/claudecode@sha256:0123456789ab": "",
	} {
		if got := PublishedRef(ref); got != want {
			t.Errorf("PublishedRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

// host is a fake docker store, registry and release source.
type host struct {
	images    map[string]Image
	registry  map[string]Image
	upstream  string
	offline   bool
	pullErr   error
	buildErr  error
	asked     []Offer
	answer    Answer
	lookups   atomic.Int64
	pulled    []string
	built     []string
	removed   []string
	env       map[string]string
	now       time.Time
	statePath string
	out       strings.Builder
}

func newHost(t *testing.T) *host {
	t.Helper()
	return &host{
		images: map[string]Image{
			"proveo/claudecode:latest": {ID: "sha256:old", Created: at(-14), Version: "2.1.312", User: "claude"},
		},
		registry:  map[string]Image{"proveo/claudecode:latest": {Created: at(0), Version: "2.1.322"}},
		upstream:  "2.1.322",
		env:       map[string]string{},
		now:       at(0).Add(time.Hour),
		statePath: filepath.Join(t.TempDir(), "state", "agent-update.json"),
	}
}

func (h *host) checker() Checker {
	return Checker{
		Local: func(ref string) (Image, bool) { img, ok := h.images[ref]; return img, ok },
		Published: func(ref string) (Image, error) {
			h.lookups.Add(1)
			img, ok := h.registry[ref]
			if h.offline || !ok {
				return Image{}, errors.New("no such host")
			}
			return img, nil
		},
		Overlays: func(repo string) []string {
			var out []string
			for ref := range h.images {
				if strings.HasPrefix(ref, repo+":"+imagebuild.OverlayTagPrefix) {
					out = append(out, ref)
				}
			}
			slices.Sort(out)
			return out
		},
		Upstream: func(imagebuild.Pin) (string, error) {
			h.lookups.Add(1)
			if h.offline {
				return "", errors.New("no such host")
			}
			return h.upstream, nil
		},
		Pull: func(ref string) error {
			h.pulled = append(h.pulled, ref)
			if h.pullErr != nil {
				return h.pullErr
			}
			img := h.registry[ref]
			img.ID, img.User = "sha256:new", "claude"
			h.images[ref] = img
			return nil
		},
		Build: func(target, base string, img Image, version, ref string) error {
			h.built = append(h.built, target+" "+base+" "+img.ID+" "+img.User+" "+version+" "+ref)
			if h.buildErr != nil {
				return h.buildErr
			}
			h.images[ref] = Image{ID: "sha256:over-" + version, Created: h.now, Version: version, Base: img.ID, User: img.User}
			return nil
		},
		Remove:    func(ref string) error { h.removed = append(h.removed, ref); delete(h.images, ref); return nil },
		Ask:       func(o Offer) Answer { h.asked = append(h.asked, o); return h.answer },
		Getenv:    func(k string) string { return h.env[k] },
		Now:       func() time.Time { return h.now },
		StatePath: h.statePath,
		UI:        ui.New(&h.out),
	}
}

func (h *host) settle(t *testing.T, ref string) string {
	t.Helper()
	return h.checker().Settle("claudecode", ref)
}

func TestAnAcceptedUpdatePullsThePublishedImage(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.answer = Update
	if got := h.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" {
		t.Fatalf("Settle = %q, want the pulled tag", got)
	}
	want := Offer{Target: "claudecode", From: "2.1.312", To: "2.1.322", Steps: []string{"docker pull proveo/claudecode:latest (2.1.322)"}}
	if len(h.asked) != 1 || h.asked[0].Target != want.Target || h.asked[0].From != want.From || h.asked[0].To != want.To ||
		!slices.Equal(h.asked[0].Steps, want.Steps) {
		t.Errorf("asked %+v, want %+v", h.asked, want)
	}
	if !slices.Equal(h.pulled, []string{"proveo/claudecode:latest"}) || len(h.built) != 0 {
		t.Errorf("pulled %q built %q; want one pull and no build", h.pulled, h.built)
	}
	if h.images["proveo/claudecode:latest"].Version != "2.1.322" {
		t.Errorf("the store still holds %+v", h.images["proveo/claudecode:latest"])
	}
	h.asked = nil
	if got := h.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || len(h.asked) != 0 {
		t.Errorf("an up-to-date image asked again: %q, %+v", got, h.asked)
	}
}

func TestTheGapToUpstreamIsOneLayerOverTheImage(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.upstream, h.answer = "2.1.330", Update
	got := h.settle(t, "proveo/claudecode:latest")
	if got != "proveo/claudecode:agent-2.1.330" {
		t.Fatalf("Settle = %q, want the overlay", got)
	}
	wantSteps := []string{"docker pull proveo/claudecode:latest (2.1.322)", "install 2.1.330 on top as proveo/claudecode:agent-2.1.330"}
	if len(h.asked) != 1 || !slices.Equal(h.asked[0].Steps, wantSteps) || h.asked[0].To != "2.1.330" {
		t.Errorf("asked %+v, want steps %q to 2.1.330", h.asked, wantSteps)
	}
	if want := []string{"claudecode proveo/claudecode:latest sha256:new claude 2.1.330 proveo/claudecode:agent-2.1.330"}; !slices.Equal(h.built, want) {
		t.Errorf("built %q, want %q — the overlay goes on the PULLED image, as its user", h.built, want)
	}

	h.asked = nil
	if again := h.settle(t, "proveo/claudecode:latest"); again != got || len(h.asked) != 0 || len(h.built) != 1 {
		t.Errorf("the next run must launch the overlay unasked: %q, asked %+v, built %q", again, h.asked, h.built)
	}

	h.now = h.now.Add(25 * time.Hour)
	h.upstream = "2.1.331"
	if next := h.settle(t, "proveo/claudecode:latest"); next != "proveo/claudecode:agent-2.1.331" {
		t.Fatalf("a later release: Settle = %q", next)
	}
	if len(h.asked) != 1 || h.asked[0].From != "2.1.330" || !slices.Equal(h.asked[0].Steps, []string{"install 2.1.331 on top as proveo/claudecode:agent-2.1.331"}) {
		t.Errorf("asked %+v; want 2.1.330 → 2.1.331 with one install step", h.asked)
	}
	if !strings.HasPrefix(h.built[1], "claudecode proveo/claudecode:latest sha256:new ") {
		t.Errorf("the second overlay was built on %q, want the base — overlays never stack", h.built[1])
	}
	if !slices.Equal(h.removed, []string{"proveo/claudecode:agent-2.1.330"}) {
		t.Errorf("removed %q, want the superseded overlay", h.removed)
	}
}

func TestAnOverlayOfAnotherBaseIsDroppedAndForeignTagsAreLeftAlone(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.registry["proveo/claudecode:latest"] = Image{Created: at(-14), Version: "2.1.312"}
	h.upstream = "2.1.312"
	h.images["proveo/claudecode:agent-2.1.300"] = Image{ID: "sha256:stale", Created: at(-20), Version: "2.1.300", Base: "sha256:gone"}
	h.images["proveo/claudecode:agent-mine"] = Image{ID: "sha256:theirs", Created: at(-1), Version: "9.9.9"}
	if got := h.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" {
		t.Errorf("Settle = %q, want the base: neither tag is an overlay of it", got)
	}
	if !slices.Equal(h.removed, []string{"proveo/claudecode:agent-2.1.300"}) {
		t.Errorf("removed %q; want only the overlay proveo built on a base that is gone", h.removed)
	}
}

func TestLaterIsQuietForADayAndSkipUntilTheNextRelease(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.answer = Later
	if got := h.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || len(h.pulled) != 0 {
		t.Fatalf("Later ran something: %q pulled %q", got, h.pulled)
	}
	lookups := h.lookups.Load()
	h.now = h.now.Add(23 * time.Hour)
	h.settle(t, "proveo/claudecode:latest")
	if len(h.asked) != 1 || h.lookups.Load() != lookups {
		t.Errorf("within the day: asked %d times, %d new lookups; want silence", len(h.asked), h.lookups.Load()-lookups)
	}
	h.now = h.now.Add(2 * time.Hour)
	h.answer = Skip
	h.settle(t, "proveo/claudecode:latest")
	if len(h.asked) != 2 {
		t.Fatalf("after the day: asked %d times, want 2", len(h.asked))
	}
	h.now = h.now.Add(72 * time.Hour)
	h.settle(t, "proveo/claudecode:latest")
	if len(h.asked) != 2 {
		t.Errorf("a skipped release asked again (%d questions)", len(h.asked))
	}
	h.now = h.now.Add(25 * time.Hour)
	h.upstream = "2.1.323"
	h.settle(t, "proveo/claudecode:latest")
	if len(h.asked) != 3 || h.asked[2].To != "2.1.323" {
		t.Errorf("a release after the skipped one must ask: %+v", h.asked)
	}
}

func TestOneAnswerIsReusedForADayAndAnUnreachableNetworkStaysSilent(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.registry["proveo/claudecode:latest"] = Image{Created: at(-14), Version: "2.1.312"}
	h.upstream = "2.1.312"
	h.settle(t, "proveo/claudecode:latest")
	if h.lookups.Load() != 2 {
		t.Fatalf("the first run made %d lookups, want the registry and the release source", h.lookups.Load())
	}
	h.settle(t, "proveo/claudecode:latest")
	h.now = h.now.Add(23 * time.Hour)
	h.settle(t, "proveo/claudecode:latest")
	if h.lookups.Load() != 2 {
		t.Errorf("%d lookups inside one day, want 2", h.lookups.Load())
	}

	off := newHost(t)
	off.offline = true
	if got := off.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || len(off.asked) != 0 || off.out.Len() != 0 {
		t.Errorf("offline: %q, asked %+v, printed %q; want the image and silence", got, off.asked, off.out.String())
	}
	off.now = off.now.Add(30 * time.Minute)
	off.settle(t, "proveo/claudecode:latest")
	if off.lookups.Load() != 2 {
		t.Errorf("offline: %d lookups within the hour, want 2", off.lookups.Load())
	}
	off.now = off.now.Add(time.Hour)
	off.offline, off.answer = false, Later
	off.settle(t, "proveo/claudecode:latest")
	if len(off.asked) != 1 {
		t.Errorf("back online after an hour: asked %d times, want 1", len(off.asked))
	}
}

func TestWithoutATerminalTheRunWarnsOnceAndKeepsItsImage(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.answer = Unasked
	if got := h.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || len(h.pulled) != 0 {
		t.Fatalf("an unanswered offer changed the run: %q pulled %q", got, h.pulled)
	}
	for _, want := range []string{"claudecode 2.1.312 → 2.1.322 is available", EnvSwitch + "=1"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("the warning lacks %q:\n%s", want, h.out.String())
		}
	}
	if n := strings.Count(strings.TrimSpace(h.out.String()), "\n"); n != 0 {
		t.Errorf("the warning is %d lines, want one:\n%s", n+1, h.out.String())
	}
}

func TestTheSwitchUpdatesWithoutAskingOrTurnsTheCheckOff(t *testing.T) {
	t.Parallel()
	on := newHost(t)
	on.env[EnvSwitch] = "1"
	if got := on.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || len(on.asked) != 0 || len(on.pulled) != 1 {
		t.Errorf("%s=1: %q asked %+v pulled %q; want a pull with no question", EnvSwitch, got, on.asked, on.pulled)
	}
	off := newHost(t)
	off.env[EnvSwitch] = "0"
	off.images["proveo/claudecode:agent-2.1.320"] = Image{ID: "sha256:o", Created: at(-1), Version: "2.1.320", Base: "sha256:old"}
	if got := off.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:agent-2.1.320" || off.lookups.Load() != 0 || len(off.asked) != 0 {
		t.Errorf("%s=0: %q with %d lookups; want the overlay already built and no check", EnvSwitch, got, off.lookups.Load())
	}
}

func TestAFailedStepKeepsTheRunOnWhatItHas(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.answer, h.pullErr = Update, errors.New("toomanyrequests")
	if got := h.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || !strings.Contains(h.out.String(), "toomanyrequests") {
		t.Errorf("a failed pull: %q, printed %q", got, h.out.String())
	}
	b := newHost(t)
	b.upstream, b.answer, b.buildErr = "2.1.330", Update, errors.New("npm ERR! 404")
	if got := b.settle(t, "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || b.images[got].Version != "2.1.322" {
		t.Errorf("a failed install must leave the pulled image: %q %+v", got, b.images[got])
	}
	if !strings.Contains(b.out.String(), "npm ERR! 404") {
		t.Errorf("the failure is not named:\n%s", b.out.String())
	}
}

func TestALocalBuildOlderThanTheRegistryMovesToThePublishedTag(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.images["proveo/claudecode:local"] = Image{ID: "sha256:mine", Created: at(-3), Version: "2.1.318", User: "claude"}
	h.answer = Update
	if got := h.settle(t, "proveo/claudecode:local"); got != "proveo/claudecode:latest" {
		t.Errorf("Settle = %q, want the published tag the pull filled", got)
	}
	if len(h.asked) != 1 || h.asked[0].From != "2.1.318" {
		t.Errorf("asked %+v, want from 2.1.318", h.asked)
	}
}

func TestImagesTheCheckCannotReadAreLeftAlone(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.images["proveo/claudecode:latest"] = Image{ID: "sha256:old", Created: at(-14)}
	h.images["proveo/claudecode@sha256:0123"] = Image{ID: "sha256:old", Created: at(-14), Version: "2.1.312"}
	for _, ref := range []string{"proveo/claudecode:latest", "proveo/claudecode@sha256:0123", "proveo/absent:latest"} {
		if got := h.settle(t, ref); got != ref || h.lookups.Load() != 0 {
			t.Errorf("%s: Settle = %q with %d lookups; want it untouched and unchecked", ref, got, h.lookups.Load())
		}
	}
	if got := h.checker().Settle("base-node", "proveo/claudecode:latest"); got != "proveo/claudecode:latest" || h.lookups.Load() != 0 {
		t.Errorf("a target with no agent pin was checked: %q, %d lookups", got, h.lookups.Load())
	}
}

func TestAnAgentThatIsItsWholeImageGetsOneNoticePerRelease(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	h.images["proveo/hermes:latest"] = Image{ID: "sha256:h", Created: at(-14), Version: "v0.23.1"}
	h.registry["proveo/hermes:latest"] = Image{Created: at(-14), Version: "v0.23.1"}
	h.upstream = "v0.24.0"
	c := h.checker()
	for range 3 {
		if got := c.Settle("hermes", "proveo/hermes:latest"); got != "proveo/hermes:latest" {
			t.Fatalf("Settle = %q", got)
		}
	}
	want := "hermes v0.24.0 is out — proveo/hermes carries v0.23.1 until its image is republished"
	if n := strings.Count(h.out.String(), want); n != 1 || len(h.asked) != 0 || len(h.built) != 0 {
		t.Errorf("printed the notice %d times, asked %+v, built %q; want one notice and no offer:\n%s", n, h.asked, h.built, h.out.String())
	}
}

func TestTheStateFileSurvivesBeingUnreadable(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	if err := os.MkdirAll(filepath.Dir(h.statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.answer = Later
	h.settle(t, "proveo/claudecode:latest")
	if len(h.asked) != 1 {
		t.Fatalf("a corrupt state file stopped the check: asked %d times", len(h.asked))
	}
	body, err := os.ReadFile(h.statePath)
	if err != nil || !strings.Contains(string(body), `"proveo/claudecode"`) {
		t.Errorf("the state was not rewritten: %v\n%s", err, body)
	}
}

func TestTheDockerDocumentsParse(t *testing.T) {
	t.Parallel()
	local, err := parseLocal([]byte(`{"Id":"sha256:a892","Created":"2026-11-12T12:25:54.943564142-07:00",
		"Config":{"User":"claude","Labels":{"proveo.agent":"@anthropic-ai/claude-code","proveo.agent.version":"2.1.322","proveo.agent.overlay.base":"sha256:b"}}}`))
	if err != nil || local.ID != "sha256:a892" || local.Version != "2.1.322" || local.Base != "sha256:b" || local.User != "claude" ||
		!local.Created.Equal(time.Date(2026, 11, 12, 19, 25, 54, 943564142, time.UTC)) {
		t.Errorf("parseLocal = %+v, %v", local, err)
	}
	index, err := os.ReadFile(filepath.Join("testdata", "imagetools_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	for platform, minute := range map[string]int{"linux/arm64": 56, "linux/amd64": 54, "linux/riscv64": 54} {
		img, err := parsePublished(index, platform)
		if err != nil || img.Version != "2.1.322" || img.Created.Minute() != minute {
			t.Errorf("parsePublished(%s) = %+v, %v; want 2.1.322 created at :%d", platform, img, err, minute)
		}
	}
	single, err := parsePublished([]byte(`{"created":"2026-11-10T01:42:00Z","architecture":"arm64","config":{"Labels":{"proveo.agent.version":"v0.23.1"}}}`), "linux/arm64")
	if err != nil || single.Version != "v0.23.1" {
		t.Errorf("a single-platform image: %+v, %v", single, err)
	}
	if _, err := parsePublished([]byte(`{}`), "linux/arm64"); err == nil {
		t.Error("an empty registry answer parsed")
	}
}
