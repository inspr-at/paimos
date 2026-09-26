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
// caller's project bindings (Scope.AnyProject). Each one reads only rows that
// project row-level security or handler-level authorization confines to the
// caller's visible projects (nodes, relations, events, knowledge, search,
// views and approvals pinned to a project, live agent sessions),
// workspace configuration every reader needs (kinds), or the caller's own
// profile and preferences. Every other route without a project in its path is
// authorized by the workspace binding alone, so a project-only principal never
// reaches workspace-wide data such as members, quotes, CRM or hours.
var ProjectFilteredRoutes = map[string]bool{
	"GET /api/approvals":                          true,
	"GET /api/harness-sessions/live":              true,
	"GET /api/projects":                           true,
	"GET /api/nodes":                              true,
	"GET /api/nodes/lookup":                       true,
	"GET /api/nodes/tree":                         true,
	"GET /api/search":                             true,
	"GET /api/events":                             true,
	"GET /api/events/stream":                      true,
	"GET /api/from-classic":                       true,
	"GET /api/knowledge":                          true,
	"GET /api/knowledge/graph":                    true,
	"GET /api/knowledge/resolve":                  true,
	"GET /api/tickets/graph":                      true,
	"GET /api/relations":                          true,
	"GET /api/views":                              true,
	"GET /api/views/{viewId}":                     true,
	"GET /api/kinds":                              true,
	"GET /api/kinds/{kindId}":                     true,
	"GET /api/me":                                 true,
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
	"POST /api/nodes":     true,
	"POST /api/relations": true,
	"POST /api/knowledge": true,
}

// Product release notes are the same for everyone; they are not tenant data.
var publicProductRoutes = map[string]bool{
	"GET /api/releases":           true,
	"GET /api/releases/{version}": true,
}

// Routes whose path names one node-scoped resource. The resource's project,
// read under the caller's own visibility, is the scope of the decision.
func routeTarget(pattern string, values map[string]string) (kind, id string) {
	switch {
	case values["nodeId"] != "":
		return "node", values["nodeId"]
	case strings.HasPrefix(pattern, "GET /api/knowledge/{id}") || strings.HasPrefix(pattern, "PATCH /api/knowledge/{id}") || strings.HasPrefix(pattern, "DELETE /api/knowledge/{id}"):
		return "node", values["id"]
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
	}
	return "", ""
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
// name one: the project of the node, attachment, relation or event it targets
// (read with the caller's visibility, so an invisible target resolves to no
// project), or AnyProject for ProjectFilteredRoutes. ok is false when the
// route has no project scope beyond the workspace.
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
