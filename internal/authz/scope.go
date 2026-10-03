// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ProjectFilteredRoutes may be authorized by a permission held in any of the
// caller's project bindings (Scope.AnyProject). Each one accesses only rows that
// project row-level security or handler-level authorization confines to the
// caller's visible projects (nodes, relations, events, knowledge, search,
// views and approvals pinned to a project, live agent sessions),
// workspace configuration every reader needs (kinds, the workspace logo that
// /api/me links, read under tenant row-level security), or the caller's own
// profile and preferences. Every other route without a project in its path is
// authorized by the workspace binding alone, so a project-only principal never
// reaches workspace-wide data such as members, quotes, CRM or hours.
var ProjectFilteredRoutes = map[string]bool{
	"GET /api/delivery/adoptions":                 true,
	"GET /api/recurrences":                        true,
	"GET /api/decision-desk":                      true,
	"GET /api/journey/next-actions":               true,
	"GET /api/queue":                              true,
	"GET /api/me/host-labels":                     true,
	"PUT /api/me/host-labels":                     true,
	"GET /api/me/security/session-watching":       true,
	"PUT /api/me/security/session-watching":       true,
	"GET /api/approvals":                          true,
	"GET /api/harness-sessions/live":              true,
	"GET /api/me/agent-pause-settings":            true,
	"PUT /api/me/agent-pause-settings":            true,
	"GET /api/me/leaving-at":                      true,
	"PUT /api/me/leaving-at":                      true,
	"DELETE /api/me/leaving-at":                   true,
	"POST /api/harness-sessions/pause":            true,
	"POST /api/harness-sessions/resume":           true,
	"GET /api/usage/dashboard":                    true,
	"GET /api/settings/status-autopilot":          true,
	"GET /api/status-autopilot/changes":           true,
	"GET /api/status-autopilot/proposals":         true,
	"GET /api/projects":                           true,
	"GET /api/nodes":                              true,
	"GET /api/outcomes":                           true,
	"GET /api/nodes/lookup":                       true,
	"GET /api/nodes/tree":                         true,
	"GET /api/search":                             true,
	"GET /api/events":                             true,
	"GET /api/events/stream":                      true,
	"GET /api/from-classic":                       true,
	"GET /api/knowledge":                          true,
	"GET /api/knowledge/graph":                    true,
	"GET /api/knowledge/learnings":                true,
	"GET /api/knowledge/resolve":                  true,
	"GET /api/tickets/graph":                      true,
	"GET /api/relations":                          true,
	"GET /api/views":                              true,
	"GET /api/views/{viewId}":                     true,
	"GET /api/kinds":                              true,
	"GET /api/status/help":                        true,
	"GET /api/kinds/{kindId}":                     true,
	"GET /api/me":                                 true,
	"GET /api/brand/logo/{variant}":               true,
	"GET /api/me/greeting":                        true,
	"GET /api/me/profile":                         true,
	"PATCH /api/me/profile":                       true,
	"POST /api/me/avatar":                         true,
	"DELETE /api/me/avatar":                       true,
	"GET /api/me/permissions":                     true,
	"GET /api/people/{principalId}/avatar/{size}": true,
	"GET /api/preferences/{key}":                  true,
	"PUT /api/preferences/{key}":                  true,
}

// ProjectDecidedRoutes create project data named in the request body. The
// middleware lets a permission from any project binding through; the handler
// then requires it in the target project (RequireTx with that project), inside
// the transaction that writes. POST /api/nodes/bulk stays workspace-only.
var ProjectDecidedRoutes = map[string]bool{
	"PUT /api/model-preferences/levels/{level}":                  true,
	"DELETE /api/model-preferences/levels/{level}":               true,
	"PUT /api/model-preferences/levels/{level}/rows/{kindId}":    true,
	"DELETE /api/model-preferences/levels/{level}/rows/{kindId}": true,
	"POST /api/work-kinds":                                       true,
	"PATCH /api/work-kinds/{kindId}":                             true,
	"DELETE /api/work-kinds/{kindId}":                            true,
	"POST /api/work-kinds/{kindId}/restore":                      true,
	"POST /api/queue":                                            true,
	"POST /api/queue/reset":                                      true,
	"POST /api/queue/next":                                       true,
	"GET /api/rules/layers":                                      true,
	"POST /api/rules/layers":                                     true,
	"GET /api/rules/sets":                                        true,
	"POST /api/rules/sets":                                       true,
	"GET /api/rules/sets/{setId}":                                true,
	"PUT /api/rules/sets/{setId}/draft":                          true,
	"POST /api/rules/sets/{setId}/publish":                       true,
	"POST /api/rules/sets/{setId}/restore":                       true,
	"GET /api/rules/sets/{setId}/versions":                       true,
	"GET /api/rules/sets/{setId}/versions/{version}":             true,
	"GET /api/rules/merged":                                      true,
	"GET /api/rules/channels":                                    true,
	"GET /api/rules/comparisons":                                 true,
	"POST /api/rules/comparisons":                                true,
	"GET /api/rules/explained":                                   true,
	"PUT /api/rules/sets/{setId}/tldr":                           true,
	"GET /api/rules/budget":                                      true,
	"POST /api/rules/publish":                                    true,
	"POST /api/nodes":                                            true,
	"POST /api/outcomes":                                         true,
	"POST /api/relations":                                        true,
	"POST /api/knowledge":                                        true,
	"POST /api/recurrences":                                      true,
}

// Product release notes are the same for everyone; they are not tenant data.
var publicProductRoutes = map[string]bool{
	"GET /api/releases":           true,
	"GET /api/releases/{version}": true,
}

// Routes whose path names one node-scoped resource. The resource's project,
// read under the caller's own visibility, is the scope of the decision.
// A delivery rating names a harness session, not a node. Quote presence
// session ids are a different resource and stay unresolved here.
func routeTarget(pattern string, values map[string]string) (kind, id string) {
	switch {
	case values["recurrenceId"] != "":
		return "recurrence", values["recurrenceId"]
	case values["questionId"] != "":
		return "node", values["questionId"]
	case values["nodeId"] != "":
		return "node", values["nodeId"]
	case strings.HasPrefix(pattern, "GET /api/knowledge/{id}") || strings.HasPrefix(pattern, "PATCH /api/knowledge/{id}") || strings.HasPrefix(pattern, "DELETE /api/knowledge/{id}"):
		return "node", values["id"]
	case pattern == "POST /api/knowledge/learnings/{learningId}/accept" || pattern == "POST /api/knowledge/learnings/{learningId}/dismiss" || pattern == "POST /api/knowledge/learnings/{learningId}/draft":
		// Accept and dismiss name the source item, not a project. The node's
		// project_id is that item's project, including a project node itself.
		nodeID, ok := learningSourceNode(values["learningId"])
		if !ok {
			return "", ""
		}
		return "node", nodeID
	case strings.Contains(pattern, " /api/attachments/{id}"):
		return "attachment", values["id"]
	case values["relationId"] != "":
		return "relation", values["relationId"]
	case values["eventId"] != "":
		return "event", values["eventId"]
	case pattern == "GET /api/inbox/messages/{messageId}/receipt":
		return "inbox_receipt", values["messageId"]
	case strings.HasSuffix(pattern, " /api/node-keys/{key}"):
		return "node_key", values["key"]
	case strings.HasSuffix(pattern, " /api/harness-sessions/{sessionId}/delivery-rating"):
		return "session", values["sessionId"]
	}
	return "", ""
}

// learningSourceNode is the node a method-learning id names. Ticket ids are
// "n-<uuid>"; comment ids are "c-<uuid>-<event id>". The uuid is the node the
// comment is on, which is the project itself when the comment belongs to a project.
func learningSourceNode(raw string) (string, bool) {
	switch {
	case strings.HasPrefix(raw, "n-") && uuidPattern.MatchString(raw[2:]):
		return raw[2:], true
	case strings.HasPrefix(raw, "c-") && len(raw) > 39 && raw[38] == '-' && uuidPattern.MatchString(raw[2:38]) && decimalID(raw[39:]):
		return raw[2:38], true
	default:
		return "", false
	}
}

func decimalID(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type routeScopeKey struct{}

// WithRouteScope records the scope the route was authorized in, so handlers
// recheck permissions in the same scope.
func WithRouteScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, routeScopeKey{}, scope)
}

// RouteScope returns the scope the authorization middleware decided the route
// in; the zero Scope (workspace only) when there is none.
func RouteScope(ctx context.Context) Scope {
	scope, _ := ctx.Value(routeScopeKey{}).(Scope)
	return scope
}

// ResolveRouteScope finds the project a route acts in when its path does not
// name one: the project of the node, attachment, relation, event or harness
// session it targets (read with the caller's visibility, so an invisible
// target resolves to no project), or AnyProject for ProjectFilteredRoutes.
// ok is false when the route has no project scope beyond the workspace.
func ResolveRouteScope(ctx context.Context, pool *pgxpool.Pool, pattern, path string) (Scope, bool, error) {
	values := PatternValues(pattern, path)
	if id := values["projectId"]; id != "" {
		if !uuidPattern.MatchString(id) {
			return Scope{}, false, nil
		}
		return Scope{ProjectID: id}, true, nil
	}
	if kind, id := routeTarget(pattern, values); kind != "" {
		project, err := targetProject(ctx, pool, kind, id)
		if err != nil || project == "" {
			return Scope{}, false, err
		}
		return Scope{ProjectID: project}, true, nil
	}
	if ProjectFilteredRoutes[pattern] || ProjectDecidedRoutes[pattern] || publicProductRoutes[pattern] {
		return Scope{AnyProject: true}, true, nil
	}
	return Scope{}, false, nil
}

func targetProject(ctx context.Context, pool *pgxpool.Pool, kind, id string) (string, error) {
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || pool == nil {
		return "", nil
	}
	var query string
	args := []any{id}
	switch kind {
	case "recurrence":
		if !uuidPattern.MatchString(id) {
			return "", nil
		}
		query = `SELECT project_id::text FROM recurrences WHERE id=$1::uuid`
	case "node":
		if !uuidPattern.MatchString(id) {
			return "", nil
		}
		query = `SELECT project_id::text FROM nodes WHERE id=$1::uuid`
	case "node_key":
		if len(id) > 30 {
			return "", nil
		}
		query = `SELECT n.project_id::text FROM nodes n WHERE n.key=$1
		  UNION ALL SELECT n.project_id::text FROM node_key_aliases a JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.node_id WHERE a.key=$1
		  LIMIT 1`
	case "attachment":
		if !uuidPattern.MatchString(id) {
			return "", nil
		}
		query = `SELECT n.project_id::text FROM attachments a JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.node_id WHERE a.id=$1::uuid`
	case "relation":
		if !uuidPattern.MatchString(id) {
			return "", nil
		}
		query = `SELECT n.project_id::text FROM node_relations r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.source_node_id WHERE r.id=$1::uuid`
	case "event":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 1 {
			return "", nil
		}
		args = []any{n}
		query = `SELECT n.project_id::text FROM events e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id WHERE e.id=$1`
	case "session":
		if !uuidPattern.MatchString(id) {
			return "", nil
		}
		// harness_sessions project visibility hides a session the caller cannot
		// see, so the route stays in the workspace and a project guest is denied.
		query = `SELECT project_id::text FROM harness_sessions WHERE id=$1::uuid`
	case "inbox_receipt":
		if !uuidPattern.MatchString(id) {
			return "", nil
		}
		// A project grant may authorize only the sender's existing receipt.
		// Keep the lookup inside caller-scoped RLS and let the permission
		// decision below verify the resolved project binding.
		query = `SELECT c.project_id::text FROM inbox_compat_messages c
		  JOIN inbox_receipts r ON r.tenant_id=c.tenant_id AND r.message_id=c.inbox_message_id
		  JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.project_id
		  WHERE c.inbox_message_id=$1::uuid AND c.sender_principal_id=$2::uuid`
		args = []any{id, p.ID}
	default:
		return "", nil
	}
	var project *string
	err := db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(&project)
	})
	if errors.Is(err, pgx.ErrNoRows) || project == nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return *project, nil
}

// PatternValues maps the wildcards of a ServeMux pattern (a method, a space
// and a path such as /api/nodes/{nodeId}) to the segments of path.
func PatternValues(pattern, path string) map[string]string {
	out := map[string]string{}
	_, route, ok := strings.Cut(pattern, " ")
	if !ok {
		route = pattern
	}
	want := strings.Split(strings.Trim(route, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(want) != len(got) {
		return out
	}
	for i, segment := range want {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			out[strings.TrimSuffix(strings.Trim(segment, "{}"), "...")] = got[i]
		}
	}
	return out
}
