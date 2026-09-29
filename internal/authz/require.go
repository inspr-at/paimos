// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

var ErrForbidden = errors.New("permission denied")
var ErrNoStore = errors.New("authorization store missing")

// Scope says where a permission is evaluated. The workspace binding always
// counts. ProjectID adds that project's binding. AnyProject adds every project
// binding and is used only for routes whose data project row-level security
// confines to the caller's visible projects (ProjectFilteredRoutes).
type Scope struct {
	ProjectID  string
	AnyProject bool
}
type RoleRef struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}
type Grant struct {
	Role        *RoleRef `json:"role"`
	Permissions []string `json:"permissions"`
}
type ProjectGrant struct {
	ID          string   `json:"id"`
	Role        *RoleRef `json:"role"`
	Permissions []string `json:"permissions"`
}
type Effective struct {
	Workspace Grant         `json:"workspace"`
	Project   *ProjectGrant `json:"project"`
	// anyProject is the workspace set plus every project binding's
	// project-grantable and self-service permissions.
	anyProject []string
}

type poolKey struct{}

func BindPool(ctx context.Context, pool *pgxpool.Pool) context.Context {
	return context.WithValue(ctx, poolKey{}, pool)
}

// Require denies unknown permissions, inactive principals, missing bindings,
// and agent keys whose scopes do not include the requested permission.
func Require(ctx context.Context, permission string, scope Scope) error {
	if _, ok := Lookup(permission); !ok {
		return ErrForbidden
	}
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok {
		return ErrForbidden
	}
	pool, ok := ctx.Value(poolKey{}).(*pgxpool.Pool)
	if !ok || pool == nil {
		return ErrNoStore
	}
	effective, err := Load(ctx, pool, p, scope.ProjectID)
	if err != nil {
		return err
	}
	return permitEffective(p, permission, effective, scope)
}

func requireTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string, scope Scope) error {
	if _, ok := Lookup(permission); !ok {
		return ErrForbidden
	}
	effective, err := loadTx(ctx, tx, p, scope.ProjectID)
	if err != nil {
		return err
	}
	return permitEffective(p, permission, effective, scope)
}

// RequireInProjects decides permission in each listed project separately; ""
// stands for the workspace (a node outside every project). A write that
// changes several projects, such as a move or its undo, needs the permission
// in every one of them, never just in the project its route was decided in.
func RequireInProjects(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string, projectIDs ...string) error {
	seen := map[string]bool{}
	for _, id := range projectIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := requireTx(ctx, tx, p, permission, Scope{ProjectID: id}); err != nil {
			return err
		}
	}
	return nil
}

// RequireTx makes a decision inside an existing db.InTenant transaction. It
// is used by handlers whose resource lock and access check must be atomic.
func RequireTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string, scope Scope) error {
	return requireTx(ctx, tx, p, permission, scope)
}

func permitEffective(p tenant.Principal, permission string, effective Effective, scope Scope) error {
	allowed := contains(effective.Workspace.Permissions, permission)
	if effective.Project != nil {
		allowed = allowed || contains(effective.Project.Permissions, permission)
	}
	if scope.AnyProject {
		allowed = allowed || contains(effective.anyProject, permission)
	}
	if !allowed {
		return ErrForbidden
	}
	if p.Kind == tenant.Agent && !containsScope(p.Scopes, permission) && !CoordinatorCeiling(p.Scopes, permission) {
		return ErrForbidden
	}
	return nil
}

func contains(items []string, want string) bool {
	for _, v := range items {
		if v == want {
			return true
		}
	}
	return false
}
func containsScope(items []string, want string) bool {
	for _, v := range items {
		if strings.ReplaceAll(v, ":", ".") == want {
			return true
		}
	}
	return false
}

// Load evaluates bindings inside one tenant transaction. Project support is
// read-ready for P2; P1 writes only workspace bindings.
func Load(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, projectID string) (Effective, error) {
	var result Effective
	err := db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		result, err = loadTx(ctx, tx, p, projectID)
		return err
	})
	return result, err
}

// loadTx lets access mutations recheck grants in the same tenant transaction
// that writes the binding. It also works when the pool has one connection.
//
// A project binding grants only the permissions of its role that the registry
// allows at project scope; workspace-only permissions (members, roles, keys,
// settings, ...) never come from a project binding. For AnyProject routes the
// self-service permissions (own profile, own access) of a project role count
// too, so a project-only Guest can load their own profile.
func loadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string) (Effective, error) {
	g, err := readGrants(ctx, tx, p)
	if err != nil {
		return Effective{}, err
	}
	result := Effective{Workspace: Grant{Role: g.workspaceRole, Permissions: unique(g.workspace)}}
	if projectID != "" {
		result.Project = &ProjectGrant{ID: projectID, Permissions: []string{}}
		if grant, ok := g.projects[projectID]; ok {
			result.Project.Role = grant.Role
			result.Project.Permissions = grant.Permissions
		}
		result.Project.Permissions = unique(append(result.Project.Permissions, result.Workspace.Permissions...))
	}
	result.anyProject = unique(append(g.anyProject, result.Workspace.Permissions...))
	applyCoordinatorReads(&result, p)
	if p.Kind == tenant.Agent && p.KeyCreatorID != "" {
		creator := tenant.Principal{ID: p.KeyCreatorID, TenantID: p.TenantID, Kind: tenant.Person}
		ceiling, err := loadTx(ctx, tx, creator, projectID)
		if err != nil {
			return Effective{}, err
		}
		result.Workspace.Permissions = intersect(result.Workspace.Permissions, ceiling.Workspace.Permissions)
		if result.Project != nil && ceiling.Project != nil {
			result.Project.Permissions = intersect(result.Project.Permissions, ceiling.Project.Permissions)
		}
		result.anyProject = intersect(result.anyProject, ceiling.anyProject)
	}
	return result, nil
}

// grants is one read of a principal's bindings: the workspace binding's
// permissions, each project binding's project-grantable permissions (without
// the workspace's), and what every project binding adds for AnyProject routes.
type grants struct {
	workspaceRole *RoleRef
	workspace     []string
	projects      map[string]*ProjectGrant
	anyProject    []string
}

func readGrants(ctx context.Context, tx pgx.Tx, p tenant.Principal) (grants, error) {
	g := grants{workspace: []string{}, projects: map[string]*ProjectGrant{}, anyProject: []string{}}
	// Linked people have no binding of their own. Their session retains its
	// principal ID, but permissions come from the signed-in canonical person.
	var status, kind, bindingID, canonicalStatus string
	if err := tx.QueryRow(ctx, `SELECT p.status,p.kind,canonical.id::text,canonical.status
		FROM principals p JOIN principals canonical
		  ON canonical.tenant_id=p.tenant_id AND canonical.id=coalesce(p.linked_to,p.id)
		WHERE p.tenant_id=$1::uuid AND p.id=$2::uuid`, p.TenantID, p.ID).Scan(&status, &kind, &bindingID, &canonicalStatus); err != nil {
		return g, err
	}
	if status != "active" || canonicalStatus != "active" || kind != string(p.Kind) {
		return g, ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT b.scope_type,coalesce(b.scope_id::text,''),r.id::text,r.key,r.name,r.builtin,rp.permission
          FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
          LEFT JOIN role_permissions rp ON rp.tenant_id=r.tenant_id AND rp.role_id=r.id
		  WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid
		  ORDER BY b.scope_type,b.scope_id,rp.permission`, p.TenantID, bindingID)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var scopeType, scopeID, id, key, name string
		var builtin bool
		var perm *string
		if err := rows.Scan(&scopeType, &scopeID, &id, &key, &name, &builtin, &perm); err != nil {
			return g, err
		}
		ref := &RoleRef{ID: id, Key: key, Name: name}
		perms := []string{}
		if builtin {
			perms = builtinPermissions(key)
		} else if perm != nil {
			perms = []string{*perm}
		}
		if scopeType == "workspace" {
			g.workspaceRole = ref
			g.workspace = append(g.workspace, perms...)
			continue
		}
		for _, key := range perms {
			if ProjectGrantable(key) || selfPermission(key) {
				g.anyProject = append(g.anyProject, key)
			}
		}
		grant := g.projects[scopeID]
		if grant == nil {
			grant = &ProjectGrant{ID: scopeID, Permissions: []string{}}
			g.projects[scopeID] = grant
		}
		grant.Role = ref
		for _, key := range perms {
			if ProjectGrantable(key) {
				grant.Permissions = append(grant.Permissions, key)
			}
		}
	}
	return g, rows.Err()
}

// allows is permitEffective for one project (or the workspace, ""), on grants read once.
func (g grants) allows(permission, projectID string) bool {
	if contains(g.workspace, permission) {
		return true
	}
	grant := g.projects[projectID]
	return projectID != "" && grant != nil && contains(grant.Permissions, permission)
}

// ProjectCheck answers RequireTx's question for many projects without asking
// the database again: permission in the workspace (projectID "") or in that
// project, capped by an agent key's creator and scopes.
type ProjectCheck func(permission, projectID string) bool

// ProjectsTx reads the caller's bindings (and its key creator's) once, inside
// an existing db.InTenant transaction, for pages that decide per project for
// many projects at once. An inactive caller or creator is denied everything.
func ProjectsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (ProjectCheck, error) {
	deny := func(string, string) bool { return false }
	own, err := readGrants(ctx, tx, p)
	if errors.Is(err, ErrForbidden) {
		return deny, nil
	}
	if err != nil {
		return nil, err
	}
	var creator *grants
	if p.Kind == tenant.Agent && p.KeyCreatorID != "" {
		c, err := readGrants(ctx, tx, tenant.Principal{ID: p.KeyCreatorID, TenantID: p.TenantID, Kind: tenant.Person})
		if errors.Is(err, ErrForbidden) {
			return deny, nil
		}
		if err != nil {
			return nil, err
		}
		creator = &c
	}
	return func(permission, projectID string) bool {
		if _, ok := Lookup(permission); !ok {
			return false
		}
		if !own.allows(permission, projectID) && !coordinatorAllows(p, own, permission, projectID) {
			return false
		}
		if creator != nil && !creator.allows(permission, projectID) {
			return false
		}
		return p.Kind != tenant.Agent || containsScope(p.Scopes, permission) || CoordinatorCeiling(p.Scopes, permission)
	}, nil
}

// ProjectGrantable reports whether a registry permission may be granted by a
// project binding.
func ProjectGrantable(key string) bool {
	p, ok := Lookup(key)
	if !ok {
		return false
	}
	for _, at := range p.GrantableAt {
		if at == "project" {
			return true
		}
	}
	return false
}

// selfPermission names the workspace-only permissions a project role still
// needs to use its projects: the caller's own profile and effective access,
// and reading node kinds (tenant configuration every node view renders).
func selfPermission(key string) bool {
	switch key {
	case "authz.read", "profile.read", "profile.write", "profile.portal_read", "profile.portal_write", "kinds.read":
		return true
	}
	return false
}

func intersect(grants, ceiling []string) []string {
	out := make([]string, 0, len(grants))
	for _, key := range grants {
		if contains(ceiling, key) {
			out = append(out, key)
		}
	}
	return out
}

// EffectiveTx is for an existing db.InTenant transaction, chiefly key issuance.
// It keeps the grant check and key insert on one connection and snapshot.
func EffectiveTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string) (Effective, error) {
	return loadTx(ctx, tx, p, projectID)
}

func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func unique(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for _, v := range in {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// Handle records a permission at registration and enforces it before the
// handler. A nil permission is never accepted for a protected route.
func Handle(mux *http.ServeMux, pool *pgxpool.Pool, pattern, permission string, handler http.HandlerFunc) {
	if _, ok := Lookup(permission); !ok {
		panic("undeclared permission: " + permission)
	}
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		// Decide in the scope the authorization middleware chose (a project
		// the route targets, or any project for ProjectFilteredRoutes).
		scope := RouteScope(r.Context())
		if scope == (Scope{}) {
			scope = Scope{ProjectID: r.PathValue("projectId")}
		}
		ctx := BindPool(r.Context(), pool)
		if err := Require(ctx, permission, scope); err != nil {
			httpapi.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "permission denied", "code": "forbidden", "reason": err.Error()})
			return
		}
		handler(w, r.WithContext(ctx))
	})
}
