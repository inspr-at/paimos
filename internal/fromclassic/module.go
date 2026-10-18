// SPDX-License-Identifier: AGPL-3.0-only

// Package fromclassic resolves old Paimos browser paths to Aeon routes. Mount
// New(pool) as an httpapi.Module; the route is read-only and has no plugin.
// The caller's project visibility is enforced by nodes RLS inside db.InTenant.
package fromclassic

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const movedNotice = "That old link has moved. We brought you to the closest available page."

var numericID = regexp.MustCompile(`^[0-9]+$`)

type module struct{ pool *pgxpool.Pool }

// New returns the GET /api/from-classic module for cmd/aeon/serve.go.
func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool: pool} }

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/from-classic", m.handle)
}

type destination struct {
	Path   string `json:"path"`
	Notice string `json:"notice,omitempty"`
}

func home() destination { return destination{Path: "/", Notice: movedNotice} }

func (m *module) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	values, supplied := r.URL.Query()["path"]
	if !supplied || len(values) != 1 || len(values[0]) > 2048 || values[0] == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "one classic path is required")
		return
	}
	classic, err := url.ParseRequestURI(values[0])
	if err != nil || !strings.HasPrefix(values[0], "/") || strings.HasPrefix(values[0], "//") ||
		classic == nil || classic.Host != "" || classic.Fragment != "" || strings.ContainsAny(classic.Path, "\\\x00\r\n") {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid classic path")
		return
	}
	var target destination
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var lookupErr error
		target, lookupErr = resolve(r.Context(), tx, p, classic.Path)
		return lookupErr
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "resolve classic path", "err", err)
		httpapi.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, target)
}

func resolve(ctx context.Context, tx pgx.Tx, p tenant.Principal, path string) (destination, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch parts[0] {
	case "issues":
		if len(parts) == 2 {
			return nodeRoute(ctx, tx, p.TenantID, "issue", canonicalID(parts[1]))
		}
	case "projects":
		if len(parts) >= 2 {
			if len(parts) == 4 && parts[2] == "issues" {
				return nodeRoute(ctx, tx, p.TenantID, "issue", canonicalID(parts[3]))
			}
			return nodeRoute(ctx, tx, p.TenantID, "project", canonicalID(parts[1]))
		}
	case "customers", "crm":
		if len(parts) == 1 {
			return destination{Path: "/business/customers"}, nil
		}
		if len(parts) == 2 {
			return offerRoute(ctx, tx, p, "customer", canonicalID(parts[1]))
		}
		if parts[0] == "crm" && len(parts) >= 3 && parts[1] == "offers" {
			return offerRoute(ctx, tx, p, "offer", canonicalID(parts[2]))
		}
	case "hours":
		return destination{Path: "/business/hours"}, nil
	case "portal":
		if len(parts) >= 3 && parts[1] == "projects" {
			if len(parts) == 5 && parts[3] == "issues" {
				return nodeRoute(ctx, tx, p.TenantID, "issue", canonicalID(parts[4]))
			}
			return nodeRoute(ctx, tx, p.TenantID, "project", canonicalID(parts[2]))
		}
	}
	return home(), nil
}

func canonicalID(raw string) string {
	if len(raw) > 19 || !numericID.MatchString(raw) {
		return ""
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// nodeRoute uses the expression index and lets FORCE RLS hide project-scoped
// nodes. A second match is ambiguous across source instances, so fail closed.
func nodeRoute(ctx context.Context, tx pgx.Tx, tenantID, kind, classicID string) (destination, error) {
	if classicID == "" {
		return home(), nil
	}
	var query string
	if kind == "project" {
		query = `SELECT coalesce(nullif(btrim(n.fields->'classic'->>'key'),''),n.key),n.key
		  FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		  WHERE n.tenant_id=$1::uuid AND n.deleted_at IS NULL AND n.fields->'classic'->>'id'=$2 AND k.slug='project' LIMIT 2`
	} else {
		query = `SELECT coalesce(nullif(btrim(p.fields->'classic'->>'key'),''),p.key),n.key
		  FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		  JOIN nodes p ON p.tenant_id=n.tenant_id AND p.id=n.project_id AND p.deleted_at IS NULL
		  WHERE n.tenant_id=$1::uuid AND n.deleted_at IS NULL AND n.fields->'classic'->>'id'=$2
		    AND k.slug IN ('work','ticket','task','epic') LIMIT 2`
	}
	rows, err := tx.Query(ctx, query, tenantID, classicID)
	if err != nil {
		return destination{}, err
	}
	defer rows.Close()
	var projectKey, nodeKey string
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return destination{}, err
		}
		return home(), nil
	}
	if err := rows.Scan(&projectKey, &nodeKey); err != nil {
		return destination{}, err
	}
	if rows.Next() {
		return home(), nil
	}
	if err := rows.Err(); err != nil {
		return destination{}, err
	}
	path := "/p/" + url.PathEscape(projectKey)
	if kind == "issue" {
		path += "/" + url.PathEscape(nodeKey)
	}
	return destination{Path: path}, nil
}

// paimos_offer_imports retains the numeric identities of offline-imported CRM
// and offer records. The target node still has to be visible under RLS.
func offerRoute(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, classicID string) (destination, error) {
	if classicID == "" {
		return home(), nil
	}
	permission := "crm.read"
	path := "/business/customers/"
	if kind == "offer" {
		permission, path = "quotes.read", "/business/quotes/"
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return home(), nil
		}
		return destination{}, err
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM paimos_offer_imports i
	  JOIN nodes n ON n.tenant_id=i.tenant_id AND n.id=i.node_id AND n.deleted_at IS NULL
	  WHERE i.tenant_id=$1::uuid AND i.source_kind=$2 AND i.source_id=$3 LIMIT 2`, p.TenantID, kind, classicID)
	if err != nil {
		return destination{}, err
	}
	defer rows.Close()
	var id string
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return destination{}, err
		}
		return home(), nil
	}
	if err := rows.Scan(&id); err != nil {
		return destination{}, err
	}
	if rows.Next() {
		return home(), nil
	}
	if err := rows.Err(); err != nil {
		return destination{}, err
	}
	return destination{Path: path + id}, nil
}
