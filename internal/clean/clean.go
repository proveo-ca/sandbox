// SPEC: _spec/internal/clean/clean-lifecycle.puml
package clean

// Container is a proveo-managed container. Session is the egress session id
// ("" for a legacy DinD sidecar, which was never session-labeled).
type Container struct {
	Name    string
	Session string
	Running bool
}

// Net is a proveo egress network. HasEndpoints is true when something is still
// attached (docker network rm would fail, and it likely belongs to a live run).
type Net struct {
	Name         string
	Session      string
	HasEndpoints bool
}

type ToolDir struct {
	Path  string
	Bytes int64
}

// Inventory is everything cmd/proveo found that clean might act on.
type Inventory struct {
	Egress   []Container // containers labeled proveo.egress.session
	Networks []Net       // networks labeled proveo.egress.session
	// LegacyDind are proveo-dind-* sidecars from BEFORE the privileged sidecar was
	// retired (_spec/_plans/retire-dind.puml). proveo no longer starts one, and the
	// sweep is kept precisely because of that: a --privileged container an older
	// proveo left running is the last thing to strand on an operator's host with
	// nothing left that knows its name.
	LegacyDind []Container
	StateDirs  []string // session ids present under <stateDir>/egress/
	Images     []string // proveo/* image refs (populated only for --deep)
	ToolDirs   []ToolDir
}

// Options tunes the plan.
type Options struct {
	Deep  bool // also remove proveo/* images
	Force bool // also remove resources that look live
	Tools bool
}

type Plan struct {
	Containers  []string
	Networks    []string
	StateDirs   []string
	Images      []string
	ToolDirs    []string
	SkippedLive []string
}

func BuildPlan(inv Inventory, o Options) Plan {
	live := map[string]bool{}
	for _, c := range inv.Egress {
		if c.Running && c.Session != "" {
			live[c.Session] = true
		}
	}

	var p Plan
	sweepContainer := func(c Container) {
		if c.Running && !o.Force {
			p.SkippedLive = append(p.SkippedLive, "container "+c.Name)
			return
		}
		p.Containers = append(p.Containers, c.Name)
	}
	for _, c := range inv.Egress {
		sweepContainer(c)
	}
	for _, c := range inv.LegacyDind {
		sweepContainer(c)
	}

	for _, n := range inv.Networks {
		if (live[n.Session] || n.HasEndpoints) && !o.Force {
			p.SkippedLive = append(p.SkippedLive, "network "+n.Name)
			continue
		}
		p.Networks = append(p.Networks, n.Name)
	}

	for _, sid := range inv.StateDirs {
		if live[sid] && !o.Force {
			p.SkippedLive = append(p.SkippedLive, "state "+sid)
			continue
		}
		p.StateDirs = append(p.StateDirs, sid)
	}

	if o.Deep {
		p.Images = append(p.Images, inv.Images...)
	}

	if o.Tools {
		running := false
		for _, c := range inv.Egress {
			if c.Running {
				running = true
			}
		}
		for _, c := range inv.LegacyDind {
			if c.Running {
				running = true
			}
		}
		for _, d := range inv.ToolDirs {
			if running && !o.Force {
				p.SkippedLive = append(p.SkippedLive, "tools "+d.Path)
				continue
			}
			p.ToolDirs = append(p.ToolDirs, d.Path)
		}
	}
	return p
}
