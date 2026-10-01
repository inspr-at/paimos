// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/scopecode"
)

// These are HTTP call patterns, not permission names. Transcript tests execute
// the real verbs and compare their requests to this table, including the key
// alias fallback. Permissions always come from the server's route map.
var commandScopeRoutes = map[string][]string{
	"issue get":     {"GET /api/nodes", "GET /api/node-keys/{key}", "GET /api/kinds", "GET /api/nodes/{nodeId}/activity"},
	"issue list":    {"GET /api/kinds", "GET /api/nodes"},
	"issue create":  {"GET /api/kinds", "GET /api/nodes", "GET /api/node-keys/{key}", "POST /api/nodes"},
	"issue update":  {"GET /api/nodes", "GET /api/node-keys/{key}", "GET /api/kinds", "PATCH /api/nodes/{nodeId}"},
	"issue comment": {"GET /api/nodes", "GET /api/node-keys/{key}", "GET /api/kinds", "POST /api/nodes/{nodeId}/comments"},
	"issue search":  {"GET /api/kinds", "GET /api/nodes", "GET /api/search"},
	"search":        {"GET /api/kinds", "GET /api/nodes", "GET /api/search"},
}

type neededScope struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Label string `json:"label"`
}

func scopesForCommands(commands []string) ([]neededScope, error) {
	keys := map[string]bool{}
	for _, command := range commands {
		for _, pattern := range commandScopeRoutes[command] {
			permission, ok := authz.PermissionForPattern(pattern)
			if !ok || strings.Contains(permission, "|") {
				return nil, fmt.Errorf("scope requirements unavailable for %s", pattern)
			}
			if permission == authz.PublicRoute {
				continue
			}
			keys[permission] = true
		}
	}
	out := []neededScope{}
	for key := range keys {
		permission, ok := authz.Lookup(key)
		if !ok || !permission.AgentGrantable {
			return nil, fmt.Errorf("%s is unavailable to agent keys", key)
		}
		out = append(out, neededScope{ID: key, Group: permission.Group, Label: authz.PermissionLabel(key)})
	}
	slices.SortFunc(out, func(a, b neededScope) int {
		if group := strings.Compare(a.Group, b.Group); group != 0 {
			return group
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func scopeCommands(args []string) ([]string, error) {
	if len(args) == 0 {
		return []string{"issue get", "issue create", "issue update", "issue comment", "issue search"}, nil
	}
	words := strings.Fields(strings.Join(args, " "))
	commands := []string{}
	for len(words) > 0 {
		if words[0] == "aeon" || words[0] == "paimos" {
			words = words[1:]
			continue
		}
		root := words[0]
		words = words[1:]
		if root == "issue" {
			if len(words) == 0 {
				return nil, usagef("issue needs a verb: get/create/update/comment/list/search")
			}
			for _, verb := range strings.Split(strings.ReplaceAll(words[0], ",", "/"), "/") {
				commands = append(commands, root+" "+verb)
			}
			words = words[1:]
		} else {
			commands = append(commands, root)
		}
	}
	if len(commands) == 0 {
		return nil, usagef("specify a supported CLI command")
	}
	for _, command := range commands {
		if _, ok := commandScopeRoutes[command]; !ok {
			return nil, usagef("scope requirements for %q are not available; supported: issue get/create/update/comment/list/search, search", command)
		}
	}
	return commands, nil
}

func (rt *runtime) cmdScopes() *Command {
	return &Command{Name: "scopes", Short: "Propose agent key scopes without granting access", Use: "scopes needed [command…]", subs: []*Command{
		{Name: "needed", Short: "Print scopes derived from CLI calls and the server route map", Use: "scopes needed [issue get/create/update/comment/search]", maxArgs: -1,
			Long: "Runs offline. Default: ticket get, create, update, comment and search. Pass quoted commands or repeat command words, e.g. issue get issue comment. Ordinary issue updates are covered; moving projects needs separate nodes.move permission. Unsupported commands and flags are refused rather than guessed. Pasting the code replaces the dialog selection; review it before saving.",
			run: func(args []string) error {
				commands, err := scopeCommands(args)
				if err != nil {
					return err
				}
				scopes, err := scopesForCommands(commands)
				if err != nil {
					return err
				}
				ids := make([]string, 0, len(scopes))
				for _, scope := range scopes {
					ids = append(ids, scope.ID)
				}
				code, err := scopecode.Encode(ids)
				if err != nil {
					return err
				}
				if rt.jsonOut {
					return rt.printJSON(struct {
						Code     string        `json:"code"`
						Commands []string      `json:"commands"`
						Scopes   []neededScope `json:"scopes"`
					}{code, commands, scopes})
				}
				if _, err := fmt.Fprintln(rt.stdout, code); err != nil {
					return err
				}
				for _, scope := range scopes {
					if _, err := fmt.Fprintf(rt.stdout, "  %s → %s → %s\n", scope.Group, scope.Label, scope.ID); err != nil {
						return err
					}
				}
				_, err = fmt.Fprintln(rt.stdout, "Paste into New key, Change scopes or Rotate; review before confirming. A scope code grants no access.")
				return err
			}},
	}}
}
