// SPDX-License-Identifier: AGPL-3.0-only

// Package authz implements ADR-003 access decisions. Permissions are the unit
// of authority; versioned built-in roles and tenant custom roles bundle them.
// Owner has the entire registry, Admin lacks ownership.transfer, Member can
// perform project work but cannot manage workspace policy, Viewer reads product
// data, Guest reads project work and comments, and Customer has only the own
// quote portal and profile permissions. Custom roles hold registry keys in
// role_permissions. No mutation may grant keys its actor lacks.
//
// A principal's active workspace binding supplies workspace access. A project
// binding (P2) adds its role's project-grantable permissions on that project,
// so effective access on a project is the union of both. At most one binding
// exists per scope. Guest is a project-only role; Owner and Customer are
// workspace-only. Agent key scopes narrow this set; an empty scope list grants
// no API access.
//
// Visibility (P2) is enforced by row-level security, not here: db.InTenant
// sets the caller's visible projects once per transaction ("*" for a
// workspace role holding nodes.read, else the projects of bindings whose role
// holds it, else none). The middleware decides a route in the workspace first;
// a caller without the workspace permission may act through a project binding
// in the project the route targets: the path's project, the project of the
// node, attachment, relation or event it names (read under the caller's own
// visibility), or any bound project for ProjectFilteredRoutes, whose data RLS
// confines. ProjectDecidedRoutes create items named in the body; their
// handlers require the permission in the item's project. RouteScope carries
// the decision to handlers that recheck. A write that changes several
// projects (a move, a relation, the undo of either) needs the permission in
// every one of them (RequireInProjects), never only where its route was
// decided.
// Deactivated principals have no effective permissions. Database triggers
// revoke their sessions and keys and protect the last active workspace owner.
// Service principals have no binding and use explicit internal call paths.
//
// The migration maps classic role labels into workspace bindings while keeping
// principals.roles readable for one release. Import reruns may seed a missing
// binding but cannot overwrite an existing one. Access mutations append events
// in the same db.InTenant transaction as the changed rows.
//
// New returns an httpapi.Module for /api/authz/permissions, /api/roles,
// /api/members (including invites, deactivation and aliases), /api/audit,
// /api/me/permissions and /api/projects/{id}/members; cmd/aeon mounts it.
// NewWithProvisioner injects the config-selected internal identity.Provisioner
// for optional invite account creation; nil retains existing invite behavior.
// The coordinator owns server configuration and module wiring. Provisioning
// is advertised and accepted only for the adapter's configured Aeon tenant.
// It occurs only after the Aeon invite commits; its outcome appends a separate
// credential-free event. Retry accepts only failed, pending invites and requires
// members.manage again.
// Project binding changes append binding.set and binding.removed events on the
// project node, so they are visible exactly with the project.
// Every authorizing route must use Handle or Require with a declared registry
// permission. RoutePermissions names the permission for every current API
// pattern, including explicit public paths; RequirePattern denies an unknown
// pattern. BindPool attaches the database needed by Require to a request.
package authz
