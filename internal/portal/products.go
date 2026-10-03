// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type productContextKey struct{}
type productSlugContextKey struct{}

func productSlugForContext(ctx context.Context) string {
	slug, _ := ctx.Value(productSlugContextKey{}).(string)
	return slug
}

type productSettings struct {
	ProductID       string     `json:"product_id"`
	Slug            string     `json:"slug"`
	Published       bool       `json:"published"`
	Default         bool       `json:"is_default"`
	Policy          string     `json:"participation_policy"`
	Revision        int64      `json:"revision"`
	ThemeRevision   int64      `json:"theme_revision"`
	RegisteredSince *time.Time `json:"registered_since,omitempty"`
	HistoryUntil    *time.Time `json:"anonymous_history_until,omitempty"`
}

type productSettingsWrite struct {
	Revision  int64  `json:"revision"`
	Slug      string `json:"slug"`
	Published *bool  `json:"published"`
	Default   *bool  `json:"is_default"`
	Policy    string `json:"participation_policy"`
}

type ballotHistory struct {
	WishKey string `json:"wish_key"`
	Votes   int64  `json:"votes"`
}

type participation struct {
	Policy              string          `json:"policy"`
	Voting              bool            `json:"voting_enabled"`
	Intake              bool            `json:"wish_intake_enabled"`
	Corrections         bool            `json:"corrections_enabled"`
	RegisteredAvailable bool            `json:"registered_available"`
	RegisteredSince     *time.Time      `json:"registered_since,omitempty"`
	HistoryUntil        *time.Time      `json:"anonymous_history_until,omitempty"`
	History             []ballotHistory `json:"anonymous_history"`
}

// Every portal write uses the same fences as access changes and node moves.
// No resource or event-counter lock precedes these locks.
func lockPortalTenant(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid FOR NO KEY UPDATE`)
	return err
}

func lockPortalTree(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0))`)
	return err
}

func loadProductSettings(ctx context.Context, tx pgx.Tx, id string, lock bool) (productSettings, error) {
	var out productSettings
	query := `SELECT p.product_id::text,p.slug,p.published,p.is_default,p.participation_policy,p.revision,p.theme_revision,p.registered_since,p.anonymous_history_until
        FROM portal_products p JOIN nodes n ON n.tenant_id=p.tenant_id AND n.id=p.product_id
        JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
        WHERE p.product_id=$1::uuid AND n.parent_id IS NULL AND n.deleted_at IS NULL AND k.slug='portal_product'`
	if lock {
		query += ` FOR NO KEY UPDATE OF p,n`
	}
	err := tx.QueryRow(ctx, query, id).Scan(&out.ProductID, &out.Slug, &out.Published, &out.Default, &out.Policy, &out.Revision, &out.ThemeRevision, &out.RegisteredSince, &out.HistoryUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errClosed
	}
	return out, err
}

// Empty slug resolves only the explicit default, never an alternative product.
func publicProduct(ctx context.Context, tx pgx.Tx, slug string, lock bool) (productSettings, error) {
	var out productSettings
	if slug != "" && !slugPattern.MatchString(slug) {
		return out, errClosed
	}
	query := `SELECT p.product_id::text,p.slug,p.published,p.is_default,p.participation_policy,p.revision,p.theme_revision,p.registered_since,p.anonymous_history_until
        FROM portal_products p JOIN nodes n ON n.tenant_id=p.tenant_id AND n.id=p.product_id
        JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
        WHERE (($1='' AND p.is_default) OR ($1<>'' AND p.slug=$1))
        AND p.published AND n.state='published' AND n.parent_id IS NULL AND n.deleted_at IS NULL AND k.slug='portal_product'
        AND EXISTS(SELECT 1 FROM portal_settings WHERE enabled)`
	if lock {
		query += ` FOR NO KEY UPDATE OF p,n`
	}
	err := tx.QueryRow(ctx, query, slug).Scan(&out.ProductID, &out.Slug, &out.Published, &out.Default, &out.Policy, &out.Revision, &out.ThemeRevision, &out.RegisteredSince, &out.HistoryUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errClosed
	}
	return out, err
}

func publicWriteProduct(ctx context.Context, tx pgx.Tx, slug string) (productSettings, error) {
	if err := lockPortalTenant(ctx, tx); err != nil {
		return productSettings{}, err
	}
	if err := lockPortalTree(ctx, tx); err != nil {
		return productSettings{}, err
	}
	return publicProduct(ctx, tx, slug, true)
}

func allowParticipation(p productSettings, vote bool) error {
	switch p.Policy {
	case "legacy":
		return nil
	case "registered":
		if vote {
			return statusError{http.StatusUnauthorized, "eligible portal sign-in required"}
		}
		return statusError{http.StatusForbidden, "registered participation is not available"}
	default:
		return statusError{http.StatusForbidden, "participation is disabled"}
	}
}

func (m *Module) readProductSettings(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, _ tenant.Principal) (any, error) {
		id := r.PathValue("productId")
		if !uuidPattern.MatchString(id) {
			return nil, errClosed
		}
		return loadProductSettings(ctx, tx, id, false)
	})
}

func (m *Module) writeProductSettings(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		var in productSettingsWrite
		if err := decodeJSON(r, &in); err != nil || in.Revision < 1 || in.Published == nil || !slugPattern.MatchString(in.Slug) || (in.Policy != "disabled" && in.Policy != "legacy" && in.Policy != "registered") {
			return nil, statusError{http.StatusBadRequest, "invalid product settings"}
		}
		id := r.PathValue("productId")
		if !uuidPattern.MatchString(id) {
			return nil, errClosed
		}
		current, err := loadProductSettings(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if current.Revision != in.Revision {
			return nil, statusError{http.StatusConflict, "product changed; read it again"}
		}
		// Activation cannot be bypassed through the old URL or direct settings.
		if in.Policy == "registered" {
			return nil, statusError{http.StatusConflict, "registered participation not ready"}
		}
		if in.Default != nil && !*in.Default && current.Default {
			return nil, statusError{http.StatusBadRequest, "choose another default product"}
		}
		if in.Default != nil && *in.Default && !current.Default {
			if _, err := tx.Exec(ctx, `UPDATE portal_products SET is_default=false,revision=revision+1 WHERE is_default`); err != nil {
				return nil, err
			}
		}
		makeDefault := current.Default || (in.Default != nil && *in.Default)
		_, err = tx.Exec(ctx, `UPDATE portal_products SET slug=$2,published=$3,is_default=$4,participation_policy=$5,revision=revision+1 WHERE product_id=$1::uuid`, id, in.Slug, *in.Published, makeDefault, in.Policy)
		if uniqueViolation(err) {
			return nil, statusError{http.StatusConflict, "product address already used"}
		}
		if err != nil {
			return nil, err
		}
		next, err := loadProductSettings(ctx, tx, id, false)
		if err != nil {
			return nil, err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "portal.product_settings_updated", Before: current, After: next})
		return next, err
	})
}

func loadParticipation(ctx context.Context, tx pgx.Tx, p productSettings) (participation, error) {
	on := p.Policy == "legacy"
	out := participation{Policy: p.Policy, Voting: on, Intake: on, Corrections: on, RegisteredSince: p.RegisteredSince, HistoryUntil: p.HistoryUntil, History: []ballotHistory{}}
	if on {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT n.key,coalesce(sum(v.weight),0)::bigint FROM nodes n
        JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug='portal_wish'
        JOIN portal_votes v ON v.tenant_id=n.tenant_id AND v.wish_id=n.id AND v.product_id=$1::uuid
        WHERE n.parent_id=$1::uuid AND n.deleted_at IS NULL AND n.state='published'
        AND ($2::timestamptz IS NULL OR v.created_at<=$2)
        GROUP BY n.id,n.key,n.position ORDER BY n.position,n.key LIMIT 500`, p.ProductID, p.HistoryUntil)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var h ballotHistory
		if err := rows.Scan(&h.WishKey, &h.Votes); err != nil {
			return out, err
		}
		out.History = append(out.History, h)
	}
	return out, rows.Err()
}

func (m *Module) readParticipation(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if !m.limit(w, r, "portal-read", 120) {
		return
	}
	tid, err := m.resolveTenant(r.Context(), r.PathValue("tenantSlug"))
	var out participation
	if err == nil {
		err = db.InTenant(db.AllProjects(r.Context(), "public portal participation"), m.pool, tid, func(tx pgx.Tx) error {
			p, err := publicProduct(r.Context(), tx, r.PathValue("productSlug"), false)
			if err != nil {
				return err
			}
			out, err = loadParticipation(r.Context(), tx, p)
			return err
		})
	}
	if errors.Is(err, errClosed) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusOK, out)
}

// Keep the previous binary's one-row link adapter current for the default.
// Other products retain independent links and never inherit its project.
func syncLegacyPace(ctx context.Context, tx pgx.Tx, productID string) error {
	var isDefault bool
	if err := tx.QueryRow(ctx, `SELECT is_default FROM portal_products WHERE product_id=$1::uuid`, productID).Scan(&isDefault); err != nil {
		return err
	}
	if !isDefault {
		return nil
	}
	tag, err := tx.Exec(ctx, `INSERT INTO portal_pace(tenant_id,product_id,project_node_id,release_history,revision)
        SELECT tenant_id,product_id,project_node_id,release_history,revision FROM portal_product_pace WHERE product_id=$1::uuid
        ON CONFLICT(tenant_id) DO UPDATE SET product_id=EXCLUDED.product_id,project_node_id=EXCLUDED.project_node_id,release_history=EXCLUDED.release_history,revision=EXCLUDED.revision`, productID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM portal_pace WHERE product_id=$1::uuid`, productID)
	}
	return err
}

// The public contract has a stable default even for protected legacy routes.
func productForContext(ctx context.Context) string {
	id, _ := ctx.Value(productContextKey{}).(string)
	return id
}
