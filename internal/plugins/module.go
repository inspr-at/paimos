// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	eventInstallation = "plugin.installation_changed"
	eventJobRan       = "plugin.job_ran"
)

var digestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Installation is a tenant's pin of one compiled plugin.
type Installation struct {
	ManifestDigestSHA256 string    `json:"manifest_digest_sha256"`
	Enabled              bool      `json:"enabled"`
	Permissions          []string  `json:"permissions"`
	PluginID             string    `json:"plugin_id"`
	Version              string    `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// InstallationWrite is the admin request. Permissions replace the previous set.
type InstallationWrite struct {
	ManifestDigestSHA256 string
	Enabled              bool
	Permissions          []string
}

// CatalogItem is a compiled manifest plus the tenant's pin. A tenant with no
// row still gets an installation object: enabled is false and UpdatedAt is zero.
type CatalogItem struct {
	Manifest
	Installation Installation `json:"installation"`
}

type installationBody struct {
	ManifestDigestSHA256 string    `json:"manifest_digest_sha256"`
	Enabled              *bool     `json:"enabled"`
	Permissions          *[]string `json:"permissions"`
}

type installSnap struct {
	PluginID             string   `json:"plugin_id"`
	Version              string   `json:"version"`
	ManifestDigestSHA256 string   `json:"manifest_digest_sha256"`
	Owner                string   `json:"owner"`
	Enabled              bool     `json:"enabled"`
	Permissions          []string `json:"permissions"`
}

// Module serves /api/plugins and is the host for extension calls.
type Module struct {
	pool        *pgxpool.Pool
	reg         *Registry
	appendEvent func(context.Context, pgx.Tx, tenant.Principal, events.Change) (events.Event, error)
	now         func() time.Time
	leaseTTL    time.Duration
	leases      *leaseTable
}

var _ httpapi.Module = (*Module)(nil)

// New returns the HTTP module with Pharos and Janus registered.
// The coordinator appends it to httpapi.Server.Modules.
func New(pool *pgxpool.Pool) *Module {
	reg, err := Builtin()
	if err != nil {
		panic(err)
	}
	return newModule(pool, reg)
}

// NewWithRegistry returns the HTTP module for a registry the caller has sealed.
func NewWithRegistry(pool *pgxpool.Pool, reg *Registry) *Module {
	if reg == nil || !reg.isSealed() {
		panic("plugins: registry must be sealed")
	}
	return newModule(pool, reg)
}

func newModule(pool *pgxpool.Pool, reg *Registry) *Module {
	return &Module{
		pool:        pool,
		reg:         reg,
		appendEvent: events.Append,
		now:         time.Now,
		leaseTTL:    30 * time.Second,
		leases:      &leaseTable{held: map[string]lease{}},
	}
}

// Mount registers the plugin routes.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins", m.list)
	mux.HandleFunc("PUT /api/plugins/{pluginId}/installation", m.configure)
}

func (m *Module) inTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tenantID, fn)
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	items, err := m.List(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (m *Module) configure(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body installationBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if body.Enabled == nil || body.Permissions == nil {
		writeErr(w, invalid("enabled and permissions are required"))
		return
	}
	out, err := m.Configure(r.Context(), p, r.PathValue("pluginId"), InstallationWrite{
		ManifestDigestSHA256: body.ManifestDigestSHA256,
		Enabled:              *body.Enabled,
		Permissions:          *body.Permissions,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// List returns every compiled plugin and this tenant's pin.
func (m *Module) List(ctx context.Context, p tenant.Principal) ([]CatalogItem, error) {
	if p.ID == "" || p.TenantID == "" {
		return nil, unauthorized()
	}
	var rows map[string]installRow
	err := m.inTenant(tenant.WithPrincipal(ctx, p), p.TenantID, func(tx pgx.Tx) error {
		var err error
		rows, err = listInstalls(ctx, tx, p.TenantID)
		return err
	})
	if err != nil {
		return nil, err
	}
	compiled := m.reg.list()
	items := make([]CatalogItem, 0, len(compiled))
	for _, plug := range compiled {
		item := CatalogItem{Manifest: plug.Manifest}
		if row, ok := rows[plug.Manifest.ID]; ok {
			item.Installation = row.installation()
		} else {
			item.Installation = Installation{
				ManifestDigestSHA256: plug.Manifest.DigestSHA256,
				Permissions:          []string{},
				PluginID:             plug.Manifest.ID,
				Version:              plug.Manifest.Version,
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// Configure pins a compiled plugin for the tenant. An unchanged pin does not
// append another event.
func (m *Module) Configure(ctx context.Context, p tenant.Principal, pluginID string, in InstallationWrite) (Installation, error) {
	if p.ID == "" || p.TenantID == "" {
		return Installation{}, unauthorized()
	}
	if p.Kind != tenant.Person || authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), "plugins.manage", authz.Scope{}) != nil {
		return Installation{}, forbidden("permission denied")
	}
	if !validID(pluginID) {
		return Installation{}, invalid("invalid plugin id")
	}
	plug, ok := m.reg.plugin(pluginID)
	if !ok {
		return Installation{}, notFound("plugin not found")
	}
	if !digestRe.MatchString(in.ManifestDigestSHA256) {
		return Installation{}, invalid("invalid manifest_digest_sha256")
	}
	if in.ManifestDigestSHA256 != plug.Manifest.DigestSHA256 {
		return Installation{}, conflict("manifest digest mismatch")
	}
	perms, err := normalizeGrant(in.Permissions, plug.Manifest.Permissions)
	if err != nil {
		return Installation{}, err
	}
	var out Installation
	err = m.inTenant(tenant.WithPrincipal(ctx, p), p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if authz.RequireTx(ctx, tx, p, "plugins.manage", authz.Scope{}) != nil {
			return forbidden("permission denied")
		}
		current, err := loadInstall(ctx, tx, p.TenantID, pluginID, true)
		if err != nil {
			return err
		}
		if current != nil && samePin(*current, plug.Manifest, in.Enabled, perms) {
			out = current.installation()
			return nil
		}
		var before any
		if current != nil {
			before = snapOf(*current)
		}
		updated, err := upsertInstall(ctx, tx, p, plug.Manifest, in.Enabled, perms, current == nil)
		if err != nil {
			return err
		}
		after := installSnap{
			PluginID: pluginID, Version: plug.Manifest.Version, ManifestDigestSHA256: plug.Manifest.DigestSHA256,
			Owner: plug.Manifest.Owner, Enabled: in.Enabled, Permissions: perms,
		}
		ev, err := m.appendEvent(ctx, tx, p, events.Change{Type: eventInstallation, Before: before, After: after})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO plugin_installation_events
			(tenant_id, plugin_id, event_id, manifest_digest_sha256, enabled, permissions)
			VALUES ($1::uuid, $2, $3, $4, $5, $6)`,
			p.TenantID, pluginID, ev.ID, plug.Manifest.DigestSHA256, in.Enabled, perms)
		if err != nil {
			return err
		}
		out = Installation{
			ManifestDigestSHA256: plug.Manifest.DigestSHA256,
			Enabled:              in.Enabled,
			Permissions:          cloneStrings(perms),
			PluginID:             pluginID,
			Version:              plug.Manifest.Version,
			UpdatedAt:            updated,
		}
		return nil
	})
	return out, err
}

func normalizeGrant(in, ceiling []string) ([]string, error) {
	if in == nil {
		return nil, invalid("permissions are required")
	}
	if len(in) > maxItems {
		return nil, invalid("too many permissions")
	}
	allowed := setOf(ceiling)
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, perm := range in {
		if _, ok := seen[perm]; ok {
			return nil, invalid("duplicate permission")
		}
		seen[perm] = struct{}{}
		if !fence.KnownPermission(perm) {
			return nil, invalid("unknown permission")
		}
		if _, ok := allowed[perm]; !ok {
			return nil, conflict("permission is outside the manifest ceiling")
		}
		out = append(out, perm)
	}
	slices.Sort(out)
	return out, nil
}

func samePin(row installRow, m Manifest, enabled bool, perms []string) bool {
	return row.version == m.Version && row.digest == m.DigestSHA256 && row.owner == m.Owner &&
		row.enabled == enabled && slices.Equal(row.perms, perms)
}

func snapOf(row installRow) installSnap {
	return installSnap{
		PluginID: row.pluginID, Version: row.version, ManifestDigestSHA256: row.digest,
		Owner: row.owner, Enabled: row.enabled, Permissions: cloneStrings(row.perms),
	}
}

func upsertInstall(ctx context.Context, tx pgx.Tx, p tenant.Principal, m Manifest, enabled bool, perms []string, insert bool) (time.Time, error) {
	if insert {
		var updated time.Time
		err := tx.QueryRow(ctx, `INSERT INTO plugin_installations
			(tenant_id, plugin_id, version, manifest_digest_sha256, owner, enabled, permissions, updated_by_principal_id)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::uuid)
			RETURNING updated_at`,
			p.TenantID, m.ID, m.Version, m.DigestSHA256, m.Owner, enabled, perms, p.ID).Scan(&updated)
		return updated, err
	}
	var updated time.Time
	err := tx.QueryRow(ctx, `UPDATE plugin_installations
		SET version = $3, manifest_digest_sha256 = $4, owner = $5, enabled = $6,
		    permissions = $7, updated_by_principal_id = $8::uuid, updated_at = clock_timestamp()
		WHERE tenant_id = $1::uuid AND plugin_id = $2
		RETURNING updated_at`,
		p.TenantID, m.ID, m.Version, m.DigestSHA256, m.Owner, enabled, perms, p.ID).Scan(&updated)
	return updated, err
}
