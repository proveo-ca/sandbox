// SPEC: _spec/internal/sbx/sbx-kit-v3.puml (UPSTREAM KITS)
package sbx

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed third_party/*.yaml third_party/*.dockerfile third_party/*.md
var upstreamFS embed.FS

// UpstreamKitsRef is the published kits source the vendored files were read from.
const UpstreamKitsRef = "docker/sbx-kits-contrib@8b0844a0b35585ec09a2536c3c38b9cf84980a0e"

// upstreamAgent maps a proveo harness to the published kit it copies.
var upstreamAgent = map[string]string{
	"claudecode": "claude",
	"codex":      "codex",
	"cursor":     "cursor",
	"opencode":   "opencode",
}

// upstreamLayoutEnv names recipe ENV that describes the upstream image's install layout, not agent behaviour.
var upstreamLayoutEnv = []string{"PATH", "CODEX_INSTALL_DIR"}

// upstreamSkippedHook is the hook proveo replaces with accept_workspace_trust (P6).
const upstreamSkippedHook = "Seed Claude bypass/trust flags"

const MCPGatewayEnv = "MCP_GATEWAY_URL"

type UpstreamOptions struct {
	MCPGateway bool
}

// UpstreamKit is what proveo copies from a published kit: capabilities, launch and behaviour env.
type UpstreamKit struct {
	Agent        string
	Capabilities []CapabilityV3
	Launch       []string
	Env          []string
	Files        map[string][]byte
}

// UpstreamKitFor returns the published kit target copies; ok is false for a harness with none.
func UpstreamKitFor(target string, o UpstreamOptions) (k UpstreamKit, ok bool, err error) {
	agent := upstreamAgent[target]
	if agent == "" {
		return UpstreamKit{}, false, nil
	}
	desc, err := upstreamFS.ReadFile("third_party/" + agent + ".yaml")
	if err != nil {
		return UpstreamKit{}, false, err
	}
	recipe, err := upstreamFS.ReadFile("third_party/" + agent + ".dockerfile")
	if err != nil {
		return UpstreamKit{}, false, err
	}
	caps, err := upstreamCapabilities(desc, o)
	if err != nil {
		return UpstreamKit{}, false, fmt.Errorf("upstream kit %s: %w", agent, err)
	}
	launch, env, err := upstreamRecipe(string(recipe))
	if err != nil {
		return UpstreamKit{}, false, fmt.Errorf("upstream kit %s: %w", agent, err)
	}
	files, err := upstreamFiles(caps)
	if err != nil {
		return UpstreamKit{}, false, fmt.Errorf("upstream kit %s: %w", agent, err)
	}
	return UpstreamKit{Agent: agent, Capabilities: caps, Launch: launch, Env: env, Files: files}, true, nil
}

// upstreamFiles loads every kit-relative contentFile a copied capability names; the kit build reads it beside the descriptor.
func upstreamFiles(caps []CapabilityV3) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, c := range caps {
		m, _ := c.Config.(map[string]any)
		rel, _ := m["contentFile"].(string)
		if !strings.HasPrefix(rel, "./") {
			continue
		}
		name := path.Base(rel)
		b, err := upstreamFS.ReadFile("third_party/" + name)
		if err != nil {
			return nil, fmt.Errorf("%s names %s, which is not vendored: %w", c.Type, rel, err)
		}
		out[name] = b
	}
	return out, nil
}

func upstreamCapabilities(desc []byte, o UpstreamOptions) ([]CapabilityV3, error) {
	var doc struct {
		Capabilities []map[string]any `yaml:"capabilities"`
	}
	if err := yaml.Unmarshal(desc, &doc); err != nil {
		return nil, err
	}
	var out []CapabilityV3
	for _, c := range doc.Capabilities {
		typ, _ := c["type"].(string)
		if typ == CapSbx {
			continue
		}
		cfg := c["config"]
		if typ == CapLifecycle {
			cfg = keepHooks(cfg, o)
		}
		opt, _ := c["optional"].(bool)
		out = append(out, CapabilityV3{Type: typ, Optional: opt, Config: cfg})
	}
	return out, nil
}

func keepHooks(cfg any, o UpstreamOptions) any {
	m, ok := cfg.(map[string]any)
	if !ok {
		return cfg
	}
	out := map[string]any{}
	for phase, v := range m {
		hooks, ok := v.([]any)
		if !ok || (phase != "install" && phase != "startup") {
			out[phase] = v
			continue
		}
		var kept []any
		for _, h := range hooks {
			hm, _ := h.(map[string]any)
			if skipHook(hm, o) {
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) > 0 {
			out[phase] = kept
		}
	}
	return out
}

func skipHook(h map[string]any, o UpstreamOptions) bool {
	desc, _ := h["description"].(string)
	if strings.HasPrefix(desc, upstreamSkippedHook) {
		return true
	}
	if o.MCPGateway {
		return false
	}
	cmd, _ := h["command"].(string)
	if strings.Contains(cmd, MCPGatewayEnv) {
		return true
	}
	env, _ := h["env"].([]any)
	return slices.ContainsFunc(env, func(e any) bool { return e == MCPGatewayEnv })
}

// upstreamRecipe reads the launch (ENTRYPOINT, argv0 reduced to its PATH name) and behaviour ENV from a recipe.
func upstreamRecipe(recipe string) (launch, env []string, err error) {
	for _, ins := range dockerInstructions(recipe) {
		word, rest, _ := strings.Cut(ins, " ")
		switch strings.ToUpper(word) {
		case "ENTRYPOINT":
			if err := json.Unmarshal([]byte(strings.TrimSpace(rest)), &launch); err != nil {
				return nil, nil, fmt.Errorf("ENTRYPOINT %q is not exec form: %w", rest, err)
			}
		case "ENV":
			for kv := range strings.FieldsSeq(rest) {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || slices.Contains(upstreamLayoutEnv, k) {
					continue
				}
				env = append(env, k+"="+strings.Trim(v, `"`))
			}
		}
	}
	if len(launch) == 0 {
		return nil, nil, fmt.Errorf("recipe declares no ENTRYPOINT")
	}
	launch[0] = path.Base(launch[0])
	return launch, env, nil
}

// dockerInstructions joins backslash-continued lines and drops comments and blanks.
func dockerInstructions(recipe string) []string {
	var out []string
	var cur strings.Builder
	sc := bufio.NewScanner(strings.NewReader(recipe))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if cur.Len() == 0 && (line == "" || strings.HasPrefix(line, "#")) {
			continue
		}
		if cont, ok := strings.CutSuffix(line, `\`); ok {
			cur.WriteString(strings.TrimSpace(cont))
			cur.WriteByte(' ')
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
	}
	return out
}
