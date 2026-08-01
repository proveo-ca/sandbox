// Package clean is the pure decision core for `proveo clean`: given an inventory
// of proveo-created Docker resources + on-disk egress state, it decides what to
// remove for a routine reclaim vs a --deep reset, without ever touching live
// runs (unless --force). cmd/proveo gathers the inventory and executes the plan.
//
// Two tiers:
//   - routine: leaked per-run ephemera — egress session containers/networks,
//     leaked DinD sidecars, and egress state dirs (which hold the injected
//     broker.env secret; wiping them is a security win after a crashed run).
//   - --deep: routine + the reusable proveo/* images. Upstream sidecar bases
//     (squid/ollama/docker:dind) are left — shared and cheap to re-pull.
//
// SPEC: _spec/internal/clean/clean-lifecycle.puml
package clean

// Container is a proveo-managed container. Session is the egress session id
// ("" for a DinD sidecar, which is not session-labeled).
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

// Inventory is everything cmd/proveo found that clean might act on.
type Inventory struct {
	Egress    []Container // containers labeled proveo.egress.session
	Dind      []Container // proveo-dind-* sidecars
	Networks  []Net       // networks labeled proveo.egress.session
	StateDirs []string    // session ids present under <stateDir>/egress/
	Images    []string    // proveo/* image refs (populated only for --deep)
}

// Options tunes the plan.
type Options struct {
	Deep  bool // also remove proveo/* images
	Force bool // also remove resources that look live
}

// Plan is what to remove. StateDirs are session ids (cmd/proveo maps them to
// <stateDir>/egress/<sid>). SkippedLive lists resources left because they look
// live (a running container, or a network with endpoints) and --force was off.
type Plan struct {
	Containers  []string
	Networks    []string
	StateDirs   []string
	Images      []string
	SkippedLive []string
}

// BuildPlan decides the removal set. A session is "live" when any of its egress
// containers is running; its networks and state dir are then preserved. DinD
// sidecars have no session, so a running one is treated as live on its own.
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
	for _, c := range inv.Dind {
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
	return p
}
