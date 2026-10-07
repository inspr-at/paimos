// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"fmt"
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
var Registry = makeRegistry()

func makeRegistry() []Permission {
	groups := []struct{ group, actions string }{
		{"nodes", "read write delete move restore configure"},
		{"recurrences", "manage"},
		{"delivery", "read manage"},
		{"delivery_queue", "read manage claim"},
		{"delivery_reviews", "read manage claim report"},
		{"reviewpolicy", "read manage"},
		{"rules", "read write publish"},
		{"kinds", "read manage"}, {"tags", "read write manage"},
		{"relations", "read write delete"}, {"comments", "read write delete"},
		{"attachments", "read write delete"}, {"knowledge", "read write delete"},
		{"journey", "read act manage"}, {"requirements", "read write agree"},
		{"releases", "read write deploy"}, {"intake", "read write decide"},
		{"stage_handoffs", "read write decide"}, {"harness", "read write worker control manage recover force_stop watch"},
		{"work_orders", "read write assign"}, {"runs", "read write control claim"},
		{"run", "create read claim telemetry"}, {"account", "read manage route probe"},
		{"questions", "ask read decide"}, {"approvals", "read request propose decide decide_high revoke"}, {"inbox", "read send manage receipt"},
		{"stage", "prepare deploy verify apply"},
		{"chat", "read send bind receive"},
		{"model_prefs", "manage"}, {"models", "read manage resolve report refresh"}, {"plugins", "read manage invoke"},
		{"imports", "read manage"}, {"views", "read write share"},
		{"events", "read undo undo_other"}, {"search", "read"}, {"outcome", "read write"},
		{"hours", "read write approve"}, {"quotes", "read write issue accept delete manage portal_read portal_accept"},
		{"crm", "read write manage"}, {"cost_units", "read write manage"},
		{"project_groups", "read write"}, {"profile", "read write manage portal_read portal_write"},
		{"settings", "read manage"}, {"members", "read manage"},
		{"roles", "read manage"}, {"keys", "read manage"},
		{"audit", "read"}, {"authz", "read"},
	}
	out := make([]Permission, 0, 130)
	for _, g := range groups {
		for _, action := range strings.Fields(g.actions) {
			risk := "medium"
			if action == "read" || strings.HasSuffix(action, "_read") || action == "resolve" {
				risk = "low"
			}
			if action == "watch" || action == "publish" || action == "delete" || action == "deploy" || action == "apply" || action == "decide" || action == "decide_high" || action == "manage" || action == "issue" || action == "approve" || action == "undo" || action == "undo_other" || action == "control" || action == "configure" || action == "revoke" || action == "recover" || action == "force_stop" {
				risk = "high"
			}
			at := []string{"workspace", "project"}
			switch g.group {
			case "kinds", "models", "plugins", "imports", "profile", "settings", "members", "roles", "keys", "audit", "authz":
				at = []string{"workspace"}
			}
			key := g.group + "." + action
			out = append(out, Permission{Key: key, Group: groupLabel(g.group), Description: fmt.Sprintf("%s %s", strings.Title(strings.ReplaceAll(action, "_", " ")), strings.ReplaceAll(g.group, "_", " ")), Risk: risk, GrantableAt: at, AgentGrantable: agentGrantable(key)})
		}
	}
	out = append(out, Permission{Key: "account.overview.read", Group: "Account", Description: "Read all enrolled account capacity values", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true})
	out = append(out, Permission{Key: "ownership.transfer", Group: "Ownership", Description: "Transfer workspace ownership", Risk: "high", GrantableAt: []string{"workspace"}, AgentGrantable: false})
	out = append(out, Permission{Key: "agents.plan.read", Group: "Agents", Description: "Read the person's agent start plan and running counts", Risk: "low", GrantableAt: []string{"workspace"}, AgentGrantable: true})
	for i := range out {
		out[i].OwnerWorkstationGrantable = out[i].AgentGrantable || OwnerWorkstationPermission(out[i].Key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// agentGrantable is false for human governance, approval decisions and the
// customer portal. Those permissions never belong on an agent key.
func agentGrantable(key string) bool {
	switch key {
	case "chat.bind", "model_prefs.manage", "harness.watch", "rules.publish", "harness.force_stop", "harness.recover", "members.manage", "roles.manage", "keys.manage", "keys.read", "settings.manage", "audit.read",
		"questions.decide", "approvals.decide", "approvals.decide_high",
		"profile.portal_read", "profile.portal_write", "quotes.portal_read", "quotes.portal_accept":
		return false
	default:
		return true
	}
}

func groupLabel(resource string) string {
	switch resource {
	case "crm":
		return "CRM"
	case "authz":
		return "Access"
	case "keys":
		return "API keys"
	default:
		return strings.Title(strings.ReplaceAll(resource, "_", " "))
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
	case "delivery_reviews", "delivery_queue", "delivery", "nodes", "kinds", "tags", "relations", "comments", "attachments", "knowledge", "journey", "requirements", "releases", "intake", "stage_handoffs", "harness", "work_orders", "runs", "run", "approvals", "inbox", "models", "views", "events", "search", "hours", "quotes", "crm", "cost_units", "project_groups", "profile", "outcome", "reviewpolicy":
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
