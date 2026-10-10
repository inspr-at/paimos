// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"sort"
	"strings"
)

type Permission struct {
	Key                       string   `json:"key"`
	Group                     string   `json:"group"`
	Description               string   `json:"description"`
	Risk                      string   `json:"risk"`
	GrantableAt               []string `json:"grantable_at"`
	AgentGrantable            bool     `json:"agent_grantable"`
	OwnerWorkstationGrantable bool     `json:"owner_workstation_grantable"`
}

// Registry is the versioned permission catalog. Each key is unique and uses
// the same dot notation as agent key scopes.
var Registry = []Permission{}

// All domain files initialize together before another package can use authz.
// Sorting after each insertion keeps lookup independent of initialization order.
func registerPermissions(domain string, permissions []Permission) {
	for _, permission := range permissions {
		if _, exists := Lookup(permission.Key); exists {
			panic("authz: duplicate permission " + permission.Key + " in " + domain)
		}
		Registry = append(Registry, permission)
		sort.Slice(Registry, func(i, j int) bool { return Registry[i].Key < Registry[j].Key })
	}
}

func Lookup(key string) (Permission, bool) {
	i := sort.Search(len(Registry), func(i int) bool { return Registry[i].Key >= key })
	if i < len(Registry) && Registry[i].Key == key {
		return Registry[i], true
	}
	return Permission{}, false
}

var builtinKeys = []string{"owner", "admin", "member", "viewer", "guest", "customer"}

func builtinPermissions(key string) []string {
	out := make([]string, 0, len(Registry))
	for _, p := range Registry {
		// Conversation access is explicit even for workspace owners/admins.
		if p.Key == "harness.watch" {
			continue
		}
		allow := false
		resource, _, _ := strings.Cut(p.Key, ".")
		// Explicit Decision Desk defaults. Existing agent keys gain no scopes;
		// decide is person-only even if a role includes it.
		if resource == "questions" {
			if key == "owner" || key == "admin" || key == "member" || key == "viewer" && p.Key == "questions.read" {
				out = append(out, p.Key)
			}
			continue
		}
		switch key {
		case "owner":
			allow = true
		case "admin":
			allow = p.Key != "ownership.transfer"
		case "member":
			switch resource {
			case "delivery_queue":
				allow = p.Key == "delivery_queue.read"
			case "delivery_ship":
				allow = p.Key == "delivery_ship.read"
			case "delivery_reviews":
				allow = p.Key == "delivery_reviews.read"
			case "recurrences":
				allow = true
			case "rules":
				allow = p.Key != "rules.publish"
			case "kinds", "models", "plugins", "members":
				allow = p.Key == resource+".read"
			case "imports", "settings", "roles", "keys", "audit", "account", "ownership":
			default:
				allow = !strings.HasSuffix(p.Key, ".manage") && p.Key != "nodes.configure" && p.Key != "releases.deploy" && p.Key != "approvals.decide_high" && p.Key != "hours.approve" && p.Key != "quotes.issue" && p.Key != "quotes.accept" && p.Key != "quotes.delete" && p.Key != "quotes.portal_accept" && p.Key != "stage_handoffs.decide" && p.Key != "stage.deploy" && p.Key != "stage.apply" && p.Key != "intake.decide" && p.Key != "project_groups.write" && p.Key != "runs.control" && p.Key != "events.undo_other" && p.Key != "harness.recover" && p.Key != "harness.force_stop"
			}
		case "viewer":
			allow = p.Key == "authz.read" || p.Key == "quotes.portal_read" || (p.Risk == "low" && strings.HasSuffix(p.Key, ".read") && productReadGroup(resource))
		case "guest":
			allow = p.Key == "comments.write" || p.Key == "authz.read" || (p.Risk == "low" && strings.HasSuffix(p.Key, ".read") && guestReadGroup(resource))
		case "customer":
			allow = p.Key == "profile.portal_read" || p.Key == "profile.portal_write" || p.Key == "quotes.portal_read" || p.Key == "quotes.portal_accept" || p.Key == "authz.read"
		}
		if p.Key == "account.overview.read" && (key == "owner" || key == "admin" || key == "member") {
			allow = true
		}
		if p.Key == "agents.plan.read" && key != "customer" {
			allow = true
		}
		if allow {
			out = append(out, p.Key)
		}
	}
	return out
}

func productReadGroup(group string) bool {
	switch group {
	case "delivery_ship", "delivery_reviews", "delivery_queue", "delivery", "nodes", "kinds", "tags", "relations", "comments", "attachments", "knowledge", "journey", "requirements", "releases", "intake", "stage_handoffs", "harness", "work_orders", "runs", "run", "approvals", "inbox", "models", "views", "events", "search", "hours", "quotes", "crm", "cost_units", "project_groups", "profile", "outcome", "reviewpolicy":
		return true
	}
	return false
}

func guestReadGroup(group string) bool {
	switch group {
	case "delivery", "nodes", "kinds", "tags", "relations", "comments", "attachments", "knowledge", "journey", "requirements", "releases", "intake", "views", "events", "search", "profile", "outcome":
		return true
	}
	return false
}

func BuiltinPermissions(key string) ([]string, bool) {
	for _, k := range builtinKeys {
		if k == key {
			return builtinPermissions(key), true
		}
	}
	return nil, false
}
