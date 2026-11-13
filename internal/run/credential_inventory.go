// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/cmd/proveo/github-access.puml
package run

import (
	"fmt"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

type credentialInventory struct {
	APIKeys, OAuth, SSH []string
	Env                 []string // the names the run's env file sets, A to Z
	EnvFile             string   // the run's env file; empty when it has none
}

func inventoryOf(man manifest.Manifest, lookup func(string) string, store map[string]sbx.StoreEntry, ssh []string) credentialInventory {
	api, oauth := map[string]bool{}, map[string]bool{}
	for service, entry := range store {
		if entry.APIKey {
			api[service] = true
		}
		if entry.OAuth {
			oauth[service] = true
		}
	}
	for _, name := range append(credentials.ProviderKeyVars(man, lookup), credentials.SubscriptionVars(man, lookup)...) {
		service, _ := credentials.StoreName(name)
		if service == "" {
			service = name
		}
		if strings.Contains(name, "OAUTH") {
			oauth[service] = true
		} else {
			api[service] = true
		}
	}
	keys := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for name := range set {
			out = append(out, name)
		}
		slices.Sort(out)
		return out
	}
	return credentialInventory{APIKeys: keys(api), OAuth: keys(oauth), SSH: slices.Clone(ssh)}
}

// withEnvFile is the inventory with the names the env file at path sets.
func (inv credentialInventory) withEnvFile(path string) credentialInventory {
	inv.Env, inv.EnvFile = credentials.EnvFileNames(path), path
	return inv
}

// credentialsRow is the read-only credentials row: API keys, OAuth, SSH and .env, each cell
// opening the list of what it holds.
// SPEC: _spec/internal/choiceui/wireframe.puml
func credentialsRow(items credentialInventory) choiceui.Row {
	r := choiceui.Row{Label: "credentials", Multi: true, Locked: true,
		Options: []string{"API keys", "OAuth", "SSH", ".env"}, Cols: []int{0, 1, 2, 3},
		Help: map[string]string{}, Lists: map[int]*choiceui.More{},
		Reason: "host credential inventory; configure with proveo init or the run's .env; the selected model and kit bindings determine use",
	}
	for i, list := range [][]string{items.APIKeys, items.OAuth, items.SSH, items.Env} {
		r.On = append(r.On, len(list) > 0)
		r.Off = append(r.Off, true)
		r.Help[r.Options[i]] = fmt.Sprintf("%d configured item(s); read-only", len(list))
		if len(list) == 0 {
			continue
		}
		on, help := make([]bool, len(list)), map[string]string{}
		for j, name := range list {
			on[j] = true
			help[name] = "configured on the host; read-only"
		}
		r.Lists[i] = &choiceui.More{Cell: i, Options: slices.Clone(list), On: on, ReadOnly: true, Help: help}
	}
	r.Help[".env"] = envCellHelp(items)
	if env := r.Lists[3]; env != nil {
		env.On, env.Filter, env.Bare = nil, true, true
		for name := range env.Help {
			env.Help[name] = "set in " + ui.Tilde(items.EnvFile) + "; the value is never shown"
		}
	}
	return r
}

// envCellHelp is the .env cell's help line.
func envCellHelp(items credentialInventory) string {
	if items.EnvFile == "" {
		return "no .env file for this run"
	}
	return fmt.Sprintf("%d name(s) in %s; read-only, the values are never shown", len(items.Env), ui.Tilde(items.EnvFile))
}
