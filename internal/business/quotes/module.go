// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Module struct {
	pool     *pgxpool.Pool
	registry *plugins.Registry
	linkKey  []byte
}

var _ httpapi.Module = (*Module)(nil)
var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// New exposes the quote httpapi.Module. The coordinator registers
// ManifestPlugin, seals the registry and mounts this module; it owns no shared
// server wiring. Dependencies business_costs and business_crm must be present.
func New(pool *pgxpool.Pool, registry *plugins.Registry) (httpapi.Module, error) {
	if pool == nil || registry == nil {
		return nil, fmt.Errorf("quotes: pool and registry required")
	}
	_, ok := registry.Lookup(PluginID)
	if !ok {
		return nil, fmt.Errorf("quotes: manifest is not registered")
	}
	key, err := config.LinkKeyFromEnv()
	if err != nil {
		return nil, err
	}
	return &Module{pool: pool, registry: registry, linkKey: key}, nil
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/quotes", m.list)
	mux.HandleFunc("POST /api/quotes", m.create)
	mux.HandleFunc("GET /api/quotes/{quoteId}", m.get)
	mux.HandleFunc("DELETE /api/quotes/{quoteId}", m.remove)
	mux.HandleFunc("GET /api/quotes/settings", m.settingsGet)
	mux.HandleFunc("PATCH /api/quotes/settings", m.settingsPatch)
	mux.HandleFunc("GET /api/quote-profiles", m.profileList)
	mux.HandleFunc("POST /api/quote-profiles", m.profileWrite)
	mux.HandleFunc("GET /api/quote-profiles/{profileId}", m.profileGet)
	mux.HandleFunc("PATCH /api/quote-profiles/{profileId}", m.profileWrite)
	mux.HandleFunc("DELETE /api/quote-profiles/{profileId}", m.profileArchive)
	mux.HandleFunc("POST /api/quote-profiles/{profileId}/undo", m.profileUndo)
	mux.HandleFunc("POST /api/quote-profiles/assets", m.profileAssetUpload)
	mux.HandleFunc("GET /api/quote-profiles/assets/{assetId}", m.profileAssetGet)
	mux.HandleFunc("PUT /api/quotes/{quoteId}/profile", m.selectProfile)
	mux.HandleFunc("GET /api/quotes/{quoteId}/draft", m.draftGet)
	mux.HandleFunc("PATCH /api/quotes/{quoteId}/draft", m.draftPatch)
	mux.HandleFunc("POST /api/quotes/{quoteId}/draft/branch", m.branchDraft)
	mux.HandleFunc("POST /api/quotes/{quoteId}/finalize", m.finalize)
	mux.HandleFunc("POST /api/quotes/{quoteId}/duplicate", m.duplicate)
	mux.HandleFunc("PATCH /api/quotes/{quoteId}/visibility", m.visibility)
	mux.HandleFunc("GET /api/quotes/{quoteId}/versions", m.versions)
	mux.HandleFunc("POST /api/quotes/{quoteId}/versions", m.freeze)
	mux.HandleFunc("GET /api/quotes/{quoteId}/versions/{version}", m.version)
	mux.HandleFunc("POST /api/quotes/{quoteId}/versions/{version}/issue", m.issue)
	mux.HandleFunc("POST /api/quotes/{quoteId}/versions/{version}/accept", m.accept)
	mux.HandleFunc("GET /api/quotes/{quoteId}/versions/{version}/export", m.export)
}

type failure struct {
	status  int
	message string
	fields  map[string]string
}

func (f failure) Error() string { return f.message }
func bad(s string) error        { return failure{status: 400, message: s} }
func badField(field, reason string) error {
	return failure{status: 400, message: "invalid quote document", fields: map[string]string{field: reason}}
}
func denied() error           { return failure{status: 403, message: "quote operation is not available"} }
func missing() error          { return failure{status: 404, message: "quote not found"} }
func conflict(s string) error { return failure{status: 409, message: s} }
func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		var f failure
		if errors.As(err, &f) {
			if len(f.fields) > 0 {
				httpapi.WriteJSON(w, f.status, map[string]any{"error": f.message, "errors": f.fields})
			} else {
				httpapi.WriteError(w, f.status, f.message)
			}
		} else {
			httpapi.WriteError(w, 500, "quote operation failed")
		}
		return
	}
	httpapi.WriteJSON(w, status, value)
}
func caller(r *http.Request) (tenant.Principal, error) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidRe.MatchString(p.TenantID) || !uuidRe.MatchString(p.ID) {
		return p, denied()
	}
	return p, nil
}
func person(p tenant.Principal) bool { return p.Kind == tenant.Person }

// portalContext serves the routes a customer may call (read, version, export,
// accept). A customer sees no project and no workspace node, so these reads
// run with every project visible; canReadQuote and the acceptance checks
// authorize the caller against the quote's recipient first (ADR-003 P2).
func portalContext(r *http.Request) context.Context {
	return db.AllProjects(r.Context(), "quote portal")
}
func (m *Module) allow(r *http.Request, p tenant.Principal) bool {
	return authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(r.Context(), p), m.pool), r.Pattern, authz.Scope{}) == nil
}
func pathID(r *http.Request) (string, error) {
	s := r.PathValue("quoteId")
	if !uuidRe.MatchString(s) {
		return "", bad("invalid quote id")
	}
	return s, nil
}
func pathVersion(r *http.Request) (int, error) {
	n, e := strconv.Atoi(r.PathValue("version"))
	if e != nil || n < 1 {
		return 0, bad("invalid version")
	}
	return n, nil
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20+1))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return bad("invalid JSON body")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return bad("invalid JSON body")
	}
	return nil
}
func (m *Module) tx(ctx context.Context, p tenant.Principal, perm string, write bool, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.enabled(ctx, tx, p.TenantID, perm, write); err != nil {
			return err
		}
		return fn(tx)
	})
}
func (m *Module) enabled(ctx context.Context, tx pgx.Tx, tenantID, perm string, lock bool) error {
	for _, id := range []string{PluginID, "business_costs", "business_crm"} {
		plug, ok := m.registry.Lookup(id)
		if !ok {
			return denied()
		}
		q := `SELECT enabled,manifest_digest_sha256,permissions FROM plugin_installations WHERE tenant_id=$1::uuid AND plugin_id=$2`
		if lock {
			q += ` FOR SHARE`
		}
		var on bool
		var digest string
		var perms []string
		err := tx.QueryRow(ctx, q, tenantID, id).Scan(&on, &digest, &perms)
		if errors.Is(err, pgx.ErrNoRows) {
			return denied()
		}
		if err != nil {
			return err
		}
		if !on || digest != plug.Manifest.DigestSHA256 {
			return denied()
		}
		if id == PluginID {
			found := false
			for _, p := range perms {
				if p == perm {
					found = true
				}
			}
			if !found {
				return denied()
			}
		}
	}
	return nil
}

type quote struct {
	QuoteNodeID       string `json:"quote_node_id"`
	ProjectNodeID     string `json:"project_node_id"`
	CustomerOrgNodeID string `json:"customer_org_node_id"`
	CurrentVersion    int    `json:"current_version"`
	State             string `json:"state"`
	Revision          int64  `json:"revision"`
	OfferNo           string `json:"offer_no,omitempty"`
	Archived          bool   `json:"archived"`
	ProjectRef        string `json:"project_ref"`
	ClassicStatus     string `json:"classic_status"`
}

// listRow is what the Quotes list shows for a quote beside its projection:
// the node key, the current document's title, dates and net total, and the
// customer's name. The title and dates come from the draft while it is a draft
// and from the frozen version once issued; the list never reads a whole document.
type listRow struct {
	quote
	Key           string     `json:"key"`
	Title         string     `json:"title"`
	CustomerName  string     `json:"customer_name"`
	Currency      string     `json:"currency,omitempty"`
	NetTotalCents *int64     `json:"net_total_cents,omitempty"`
	OfferDate     string     `json:"offer_date,omitempty"`
	ValidUntil    string     `json:"valid_until,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	IssuedAt      *time.Time `json:"issued_at,omitempty"`
	AcceptedAt    *time.Time `json:"accepted_at,omitempty"`
}

// draftTotal sums the positions as a saved draft does: each row's quantity in
// hundredths times its cent price, rounded half up to cents.
func draftTotal(raw []byte) (*int64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var positions []struct {
		Quantity       string `json:"quantity"`
		UnitPriceCents int64  `json:"unit_price_cents"`
	}
	if err := json.Unmarshal(raw, &positions); err != nil {
		return nil, err
	}
	var total int64
	for _, p := range positions {
		qty, err := parseQuantity(p.Quantity)
		if err != nil || p.UnitPriceCents < 0 {
			return nil, nil
		}
		total += (qty*p.UnitPriceCents + 50) / 100
	}
	return &total, nil
}

// versionCents turns a frozen numeric(18,4) total into whole cents, half up.
func versionCents(text string) (*int64, error) {
	d, err := parseDecimal(text, false)
	if err != nil {
		return nil, err
	}
	cents := (int64(d) + 50) / 100
	return &cents, nil
}

type createWrite struct {
	Title             string `json:"title"`
	ProjectNodeID     string `json:"project_node_id"`
	CustomerOrgNodeID string `json:"customer_org_node_id"`
	ProfileID         string `json:"profile_id,omitempty"`
}

func readQuote(ctx context.Context, tx pgx.Tx, id string, lock bool) (quote, error) {
	q := `SELECT quote_node_id::text,coalesce(project_node_id::text,''),customer_org_node_id::text,current_version,state,revision,coalesce(offer_no,''),archived_at IS NOT NULL,project_ref FROM business_quotes WHERE quote_node_id=$1::uuid AND deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE`
	}
	var out quote
	err := tx.QueryRow(ctx, q, id).Scan(&out.QuoteNodeID, &out.ProjectNodeID, &out.CustomerOrgNodeID, &out.CurrentVersion, &out.State, &out.Revision, &out.OfferNo, &out.Archived, &out.ProjectRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, missing()
	}
	if err == nil {
		out.ClassicStatus, err = classicStatus(ctx, tx, out)
	}
	return out, err
}
func canReadQuote(ctx context.Context, tx pgx.Tx, p tenant.Principal, q quote, versionNo int) error {
	if authz.RequireTx(ctx, tx, p, "quotes.read", authz.Scope{}) == nil {
		return nil
	}
	if !person(p) || q.State != "issued" && q.State != "accepted" || q.CurrentVersion != versionNo || versionNo < 1 {
		return denied()
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quote_versions v JOIN crm_contact_principals cp ON cp.tenant_id=v.tenant_id AND cp.contact_node_id=v.recipient_contact_node_id JOIN node_relations rel ON rel.tenant_id=cp.tenant_id AND rel.source_node_id=cp.contact_node_id AND rel.target_node_id=$3::uuid AND rel.type='contact_for' WHERE v.quote_node_id=$1::uuid AND v.version=$2 AND cp.principal_id=$4::uuid)`, q.QuoteNodeID, versionNo, q.CustomerOrgNodeID, p.ID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return denied()
	}
	return nil
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	project := r.URL.Query().Get("project_node_id")
	org := r.URL.Query().Get("customer_org_node_id")
	state := r.URL.Query().Get("state")
	archived := r.URL.Query().Get("archived")
	if archived == "" {
		archived = "false"
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			respond(w, 0, nil, bad("invalid limit"))
			return
		}
		limit = n
	}
	var cursorTime, cursorID any
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			respond(w, 0, nil, bad("invalid cursor"))
			return
		}
		parts := strings.Split(string(b), "|")
		if len(parts) != 2 || !uuidRe.MatchString(parts[1]) {
			respond(w, 0, nil, bad("invalid cursor"))
			return
		}
		parsed, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			respond(w, 0, nil, bad("invalid cursor"))
			return
		}
		cursorTime = parsed
		cursorID = parts[1]
	}
	if (project != "" && !uuidRe.MatchString(project)) || (org != "" && !uuidRe.MatchString(org)) {
		respond(w, 0, nil, bad("invalid filter"))
		return
	}
	if state != "" && state != "draft" && state != "issued" && state != "accepted" && state != "void" || archived != "true" && archived != "false" && archived != "all" {
		respond(w, 0, nil, bad("invalid quote filter"))
		return
	}
	out := []listRow{}
	var next string
	e = m.tx(r.Context(), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT q.quote_node_id::text,coalesce(q.project_node_id::text,''),q.customer_org_node_id::text,q.current_version,q.state,q.revision,coalesce(q.offer_no,''),q.archived_at IS NOT NULL,q.project_ref,q.created_at,
			n.key,coalesce(CASE WHEN q.state='draft' THEN coalesce(NULLIF(d.document->>'title',''),NULLIF(v.title,'')) ELSE coalesce(NULLIF(s.document->>'title',''),NULLIF(v.title,''),NULLIF(d.document->>'title','')) END,n.title),coalesce(o.title,''),
			coalesce(CASE WHEN q.state='draft' THEN d.document->>'currency' END,v.currency,d.document->>'currency',''),
			CASE WHEN q.state<>'draft' AND v.quote_node_id IS NOT NULL THEN v.total::text END,
			CASE WHEN q.state='draft' OR v.quote_node_id IS NULL THEN d.document->'positions' END,
			coalesce(CASE WHEN q.state='draft' THEN d.document->>'offer_date' END,s.offer_date::text,d.document->>'offer_date',''),
			coalesce(CASE WHEN q.state='draft' THEN d.document->>'valid_until' END,s.valid_until::text,d.document->>'valid_until',''),
			GREATEST(n.updated_at,q.created_at,coalesce(d.updated_at,q.created_at),coalesce(v.created_at,q.created_at),coalesce(dec.decided_at,q.created_at),coalesce(q.archived_at,q.created_at)),
			i.issued_at,dec.decided_at
			FROM business_quotes q
			JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.quote_node_id
			LEFT JOIN nodes o ON o.tenant_id=q.tenant_id AND o.id=q.customer_org_node_id
			LEFT JOIN quote_drafts d ON d.tenant_id=q.tenant_id AND d.quote_node_id=q.quote_node_id
			LEFT JOIN quote_versions v ON v.tenant_id=q.tenant_id AND v.quote_node_id=q.quote_node_id AND v.version=q.current_version
			LEFT JOIN quote_version_snapshots s ON s.tenant_id=q.tenant_id AND s.quote_node_id=q.quote_node_id AND s.version=q.current_version
			LEFT JOIN quote_issues i ON i.tenant_id=q.tenant_id AND i.quote_node_id=q.quote_node_id AND i.version=q.current_version
			LEFT JOIN quote_decisions dec ON dec.tenant_id=q.tenant_id AND dec.quote_node_id=q.quote_node_id AND dec.version=q.current_version
			WHERE q.deleted_at IS NULL AND ($1::text='' OR q.project_node_id=NULLIF($1,'')::uuid) AND ($2::text='' OR q.customer_org_node_id=NULLIF($2,'')::uuid) AND ($3::text='' OR q.state=$3) AND ($4::text='all' OR (q.archived_at IS NOT NULL)=($4::text='true')) AND ($5::timestamptz IS NULL OR q.created_at<$5::timestamptz OR (q.created_at=$5::timestamptz AND q.quote_node_id>$6::uuid)) ORDER BY q.created_at DESC,q.quote_node_id LIMIT $7`, project, org, state, archived, cursorTime, cursorID, limit+1)
		if err != nil {
			return err
		}
		var lastTime time.Time
		var lastID string
		var hasMore bool
		for rows.Next() {
			var q listRow
			var versionTotal *string
			var positions []byte
			if err := rows.Scan(&q.QuoteNodeID, &q.ProjectNodeID, &q.CustomerOrgNodeID, &q.CurrentVersion, &q.State, &q.Revision, &q.OfferNo, &q.Archived, &q.ProjectRef, &q.CreatedAt,
				&q.Key, &q.Title, &q.CustomerName, &q.Currency, &versionTotal, &positions, &q.OfferDate, &q.ValidUntil, &q.UpdatedAt, &q.IssuedAt, &q.AcceptedAt); err != nil {
				rows.Close()
				return err
			}
			if versionTotal != nil {
				q.NetTotalCents, err = versionCents(*versionTotal)
			} else {
				q.NetTotalCents, err = draftTotal(positions)
			}
			if err != nil {
				rows.Close()
				return err
			}
			if len(out) < limit {
				out = append(out, q)
				lastTime = q.CreatedAt
				lastID = q.QuoteNodeID
			} else {
				hasMore = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if hasMore {
			next = base64.RawURLEncoding.EncodeToString([]byte(lastTime.Format(time.RFC3339Nano) + "|" + lastID))
		}
		for i := range out {
			var err error
			out[i].ClassicStatus, err = classicStatus(r.Context(), tx, out[i].quote)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if e == nil && next != "" {
		w.Header().Set("X-Next-Cursor", next)
	}
	respond(w, 200, out, e)
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	id, e := pathID(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	var out quote
	e = m.tx(portalContext(r), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		var err error
		out, err = readQuote(r.Context(), tx, id, false)
		if err != nil {
			return err
		}
		return canReadQuote(r.Context(), tx, p, out, out.CurrentVersion)
	})
	respond(w, 200, out, e)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	var in createWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 512 || (in.ProjectNodeID != "" && !uuidRe.MatchString(in.ProjectNodeID)) || !uuidRe.MatchString(in.CustomerOrgNodeID) || (in.ProfileID != "" && !uuidRe.MatchString(in.ProfileID)) {
		respond(w, 0, nil, bad("invalid quote"))
		return
	}
	var out quote
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		var err error
		out, err = createCustomerQuote(r.Context(), tx, p, in.Title, in.ProjectNodeID, in.CustomerOrgNodeID, in.ProfileID, "")
		return err
	})
	respond(w, 201, out, e)
}
