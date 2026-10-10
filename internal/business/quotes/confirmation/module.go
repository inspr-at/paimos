// SPDX-License-Identifier: AGPL-3.0-only

// Package confirmation owns immutable quote PDF receipts and their retryable
// rendering jobs. SMTPAdapter is a future integration seam: this build has no
// configured adapter and never sends mail on finalize, acceptance, or import.
package confirmation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/quotepdf"
	"github.com/inspr-at/paimos/internal/tenant"
)

// SMTPAdapter is intentionally unconfigured. An integration must explicitly
// opt in under a later plugin manifest and resolve uncertain sends manually.
type SMTPAdapter interface {
	Send(context.Context, string, []byte) error
}

type Module struct {
	pool     *pgxpool.Pool
	registry *plugins.Registry
	assets   fs.FS
	store    attachments.Store
	renders  chan struct{}
}

var _ httpapi.Module = (*Module)(nil)
var _ plugins.JobProvider = (*Module)(nil)

const PluginID = "quote_confirmation"
const JobID = "render_receipt"

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// New returns both an HTTP module and a tenant-scoped ProcessNext worker. The
// coordinator mounts it and schedules ProcessNext with bounded concurrency.
func New(pool *pgxpool.Pool, registry *plugins.Registry, assets fs.FS, store attachments.Store) (*Module, error) {
	if pool == nil || registry == nil {
		return nil, errors.New("confirmation requires pool and registry")
	}
	if _, ok := registry.Lookup("business_quotes"); !ok {
		return nil, errors.New("business_quotes manifest absent")
	}
	return &Module{pool: pool, registry: registry, assets: assets, store: store, renders: make(chan struct{}, 1)}, nil
}

// SetRenderConcurrency configures the process-wide limit before the module is
// mounted. ProcessNext and the plugin job entry share this same semaphore.
func (m *Module) SetRenderConcurrency(limit int) error {
	if limit < 1 || limit > 4 {
		return errors.New("confirmation render concurrency must be between 1 and 4")
	}
	m.renders = make(chan struct{}, limit)
	return nil
}

// ManifestPlugin declares the tenant-scoped receipt renderer to the R3 job
// host. Construct New after business_quotes is registered, register this
// plugin before Seal, then mount the same Module. No SMTP integration exists.
func ManifestPlugin(worker *Module) (plugins.Plugin, error) {
	if worker == nil {
		return plugins.Plugin{}, errors.New("confirmation worker required")
	}
	p := plugins.Plugin{Manifest: plugins.Manifest{ID: PluginID, Version: "1", Owner: "aeon",
		Permissions:    []string{fence.PermJobsRun},
		BackgroundJobs: []plugins.Capability{{ID: JobID, Permission: fence.PermJobsRun}}}, Jobs: worker}
	sum, err := plugins.Digest(p)
	if err != nil {
		return plugins.Plugin{}, err
	}
	p.Manifest.DigestSHA256 = sum
	return p, nil
}

// Run is invoked only with the host's narrowed jobs.run grant and lease.
func (m *Module) Run(ctx context.Context, call plugins.Call, jobID string) (plugins.JobResult, error) {
	if jobID != JobID || !call.Grant.Allows(fence.PermJobsRun) {
		return plugins.JobResult{}, errors.New("confirmation job not granted")
	}
	_, err := m.ProcessNext(ctx, call.Principal.TenantID)
	if err != nil {
		return plugins.JobResult{}, err
	}
	return plugins.JobResult{Outcome: "succeeded"}, nil
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/quotes/readiness", m.readiness)
	mux.HandleFunc("GET /api/quotes/acceptances", m.notices)
	mux.HandleFunc("GET /api/quotes/{quoteId}/versions/{version}/confirmation", m.status)
	mux.HandleFunc("GET /api/quotes/{quoteId}/versions/{version}/confirmation/receipt", m.receipt)
	mux.HandleFunc("POST /api/quotes/{quoteId}/versions/{version}/confirmation/retry", m.retry)
}
func (m *Module) caller(r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || !uuidPattern.MatchString(p.TenantID) || !uuidPattern.MatchString(p.ID) {
		return p, false
	}
	return p, authz.RequirePattern(authz.BindPool(r.Context(), m.pool), r.Pattern, authz.Scope{}) == nil
}
func route(r *http.Request) (string, int, bool) {
	id := r.PathValue("quoteId")
	version, err := strconv.Atoi(r.PathValue("version"))
	return id, version, uuidPattern.MatchString(id) && err == nil && version > 0
}
func (m *Module) gate(ctx context.Context, tx pgx.Tx, tenantID, permission string) error {
	for _, id := range []string{"business_quotes", "business_costs", "business_crm"} {
		plug, ok := m.registry.Lookup(id)
		if !ok {
			return errors.New("plugin disabled")
		}
		var enabled bool
		var digest string
		var permissions []string
		if err := tx.QueryRow(ctx, `SELECT enabled,manifest_digest_sha256,permissions FROM plugin_installations WHERE tenant_id=$1::uuid AND plugin_id=$2`, tenantID, id).Scan(&enabled, &digest, &permissions); err != nil {
			return err
		}
		if !enabled || digest != plug.Manifest.DigestSHA256 {
			return errors.New("plugin disabled")
		}
		if id == "business_quotes" {
			found := false
			for _, p := range permissions {
				if p == permission {
					found = true
				}
			}
			if !found {
				return errors.New("permission missing")
			}
		}
	}
	return nil
}
func fail(w http.ResponseWriter, status int, message string) { httpapi.WriteError(w, status, message) }

type Job struct {
	QuoteNodeID       string    `json:"quote_node_id"`
	Version           int       `json:"version"`
	State             string    `json:"state"`
	Attempts          int       `json:"attempts"`
	NextAttemptAt     time.Time `json:"next_attempt_at"`
	ReceiptSHA256     *string   `json:"receipt_sha256,omitempty"`
	RendererVersion   *string   `json:"renderer_version,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
	AcceptanceEventID int64     `json:"-"`
}

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.QuoteNodeID, &j.Version, &j.State, &j.Attempts, &j.NextAttemptAt, &j.ReceiptSHA256, &j.RendererVersion, &j.UpdatedAt, &j.AcceptanceEventID)
	return j, err
}

const jobColumns = `quote_node_id::text,version,state,attempts,next_attempt_at,receipt_sha256,renderer_version,updated_at,acceptance_event_id`

func (m *Module) readiness(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(r)
	if !ok {
		fail(w, 403, "quote access denied")
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error { return m.gate(r.Context(), tx, p.TenantID, fence.PermViewsProvide) })
	if err != nil {
		fail(w, 403, "quote access denied")
		return
	}
	assetErr := errors.New("print assets unavailable")
	if m.assets != nil {
		_, assetErr = fs.Stat(m.assets, "quote-print.html")
	}
	httpapi.WriteJSON(w, 200, map[string]any{"renderer_available": quotepdf.Available() && assetErr == nil, "smtp_enabled": false, "smtp_configured": false, "email_delivery": "disabled"})
}
func (m *Module) status(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(r)
	if !ok {
		fail(w, 403, "quote access denied")
		return
	}
	id, version, ok := route(r)
	if !ok {
		fail(w, 400, "invalid quote route")
		return
	}
	var j Job
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermViewsProvide); err != nil {
			return err
		}
		var err error
		j, err = scanJob(tx.QueryRow(r.Context(), `SELECT `+jobColumns+` FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid AND version=$2`, id, version))
		return err
	})
	if err != nil {
		fail(w, 404, "confirmation not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, j)
}
func (m *Module) receipt(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(r)
	if !ok {
		fail(w, 403, "quote access denied")
		return
	}
	id, version, ok := route(r)
	if !ok {
		fail(w, 400, "invalid quote route")
		return
	}
	var digest string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermViewsProvide); err != nil {
			return err
		}
		return tx.QueryRow(r.Context(), `SELECT file_sha256 FROM quote_confirmation_receipts WHERE quote_node_id=$1::uuid AND version=$2`, id, version).Scan(&digest)
	})
	if err != nil {
		fail(w, 404, "receipt not found")
		return
	}
	f, err := m.store.Open(p.TenantID, digest, "original")
	if err != nil {
		fail(w, 503, "receipt file unavailable")
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="quote-receipt.pdf"`)
	_, _ = io.Copy(w, f)
}
func (m *Module) notices(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(r)
	if !ok {
		fail(w, 403, "quote access denied")
		return
	}
	type Notice struct {
		QuoteNodeID       string    `json:"quote_node_id"`
		Version           int       `json:"version"`
		Channel           string    `json:"channel"`
		AcceptedAt        time.Time `json:"accepted_at"`
		ConfirmationState string    `json:"confirmation_state"`
	}
	out := []Notice{}
	createdByMe := r.URL.Query().Get("created_by_me") == "true"
	limit := 100
	if createdByMe {
		limit = 20
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermViewsProvide); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT d.quote_node_id::text,d.version,d.channel,d.decided_at,coalesce(j.state,'pending') FROM quote_decisions d JOIN quote_versions v ON v.tenant_id=d.tenant_id AND v.quote_node_id=d.quote_node_id AND v.version=d.version LEFT JOIN quote_confirmation_jobs j ON j.tenant_id=d.tenant_id AND j.quote_node_id=d.quote_node_id AND j.version=d.version WHERE (NOT $1::boolean OR v.created_by_principal_id=$2::uuid) ORDER BY d.decided_at DESC,d.quote_node_id LIMIT $3`, createdByMe, p.ID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n Notice
			if err := rows.Scan(&n.QuoteNodeID, &n.Version, &n.Channel, &n.AcceptedAt, &n.ConfirmationState); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	if err != nil {
		fail(w, 403, "quote access denied")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) retry(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(r)
	if !ok {
		fail(w, 403, "quote access denied")
		return
	}
	if authz.Require(authz.BindPool(r.Context(), m.pool), "quotes.manage", authz.Scope{}) != nil {
		fail(w, 403, "administrator required")
		return
	}
	id, version, ok := route(r)
	if !ok {
		fail(w, 400, "invalid quote route")
		return
	}
	var input struct {
		AcknowledgeUncertain bool `json:"acknowledge_uncertain"`
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 1025))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		fail(w, 400, "invalid retry request")
		return
	}
	var j Job
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermStepsApply); err != nil {
			return err
		}
		var err error
		j, err = scanJob(tx.QueryRow(r.Context(), `SELECT `+jobColumns+` FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid AND version=$2 FOR UPDATE`, id, version))
		if err != nil {
			return err
		}
		if j.State != "failed" && (j.State != "uncertain" || !input.AcknowledgeUncertain) {
			return errors.New("retry not permitted")
		}
		nextState := "pending"
		if j.ReceiptSHA256 != nil {
			nextState = "ready"
		} // A stored receipt is never rendered or sent twice.
		_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &id, Type: "quote.confirmation_retry_requested", Before: map[string]any{"state": j.State}, After: map[string]any{"state": nextState, "version": version}})
		if err != nil {
			return err
		}
		j, err = scanJob(tx.QueryRow(r.Context(), `UPDATE quote_confirmation_jobs SET state=$3,attempts=CASE WHEN $3='pending' THEN 0 ELSE attempts END,next_attempt_at=clock_timestamp(),lease_until=NULL,last_safe_error='',updated_at=clock_timestamp() WHERE quote_node_id=$1::uuid AND version=$2 RETURNING `+jobColumns, id, version, nextState))
		return err
	})
	if err != nil {
		fail(w, 409, "retry not permitted")
		return
	}
	httpapi.WriteJSON(w, 200, j)
}

func serviceActor(ctx context.Context, tx pgx.Tx, tenantID string) (tenant.Principal, error) {
	p := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent, Roles: []string{"quote_confirmation_service"}}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "quote-confirmation:"+tenantID); err != nil {
		return p, err
	}
	err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND 'quote_confirmation_service'=ANY(roles) ORDER BY created_at,id LIMIT 1`).Scan(&p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent','Quote confirmation service',ARRAY['quote_confirmation_service']) RETURNING id::text`, tenantID).Scan(&p.ID)
		if err == nil {
			_, err = events.Append(ctx, tx, p, events.Change{Type: "principal.created", After: map[string]any{"id": p.ID, "kind": "agent", "role": "quote_confirmation_service"}})
		}
	}
	return p, err
}

// ProcessNext claims one tenant job with SKIP LOCKED, renders the frozen
// snapshot, and binds the immutable PDF hash. It never sends email. A crash
// leaves a lease that can be reclaimed, without replacing a stored receipt.
func (m *Module) ProcessNext(ctx context.Context, tenantID string) (bool, error) {
	select {
	case m.renders <- struct{}{}:
		defer func() { <-m.renders }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if !uuidPattern.MatchString(tenantID) {
		return false, errors.New("invalid tenant")
	}
	// A system job: it confirms quotes of every project (ADR-003 P2).
	ctx = db.AllProjects(ctx, "quote confirmation job")
	var j Job
	claimed := false
	var err error
	err = db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := m.gate(ctx, tx, tenantID, fence.PermStepsApply); err != nil {
			return err
		}
		j, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM quote_confirmation_jobs WHERE (state='pending' AND next_attempt_at<=clock_timestamp()) OR (state='failed' AND attempts<5 AND next_attempt_at<=clock_timestamp()) OR (state='rendering' AND lease_until<clock_timestamp()) ORDER BY next_attempt_at,quote_node_id LIMIT 1 FOR UPDATE SKIP LOCKED`))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := serviceActor(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		id := j.QuoteNodeID
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.confirmation_rendering", Before: map[string]any{"state": j.State}, After: map[string]any{"version": j.Version, "attempt": j.Attempts + 1}})
		if err != nil {
			return err
		}
		j, err = scanJob(tx.QueryRow(ctx, `UPDATE quote_confirmation_jobs SET state='rendering',attempts=attempts+1,lease_until=clock_timestamp()+interval '2 minutes',updated_at=clock_timestamp() WHERE quote_node_id=$1::uuid AND version=$2 RETURNING `+jobColumns, id, j.Version))
		claimed = err == nil
		return err
	})
	if err != nil || !claimed {
		return claimed, err
	}
	var document json.RawMessage
	var offerNo, originalDigest, name, company string
	var acceptedAt time.Time
	err = db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT s.document,s.offer_no,v.content_sha256,coalesce(pa.accepted_name,pr.name,''),coalesce(pa.accepted_company,''),d.decided_at FROM quote_confirmation_jobs j JOIN quote_version_snapshots s ON s.tenant_id=j.tenant_id AND s.quote_node_id=j.quote_node_id AND s.version=j.version JOIN quote_versions v ON v.tenant_id=j.tenant_id AND v.quote_node_id=j.quote_node_id AND v.version=j.version JOIN quote_decisions d ON d.tenant_id=j.tenant_id AND d.quote_node_id=j.quote_node_id AND d.version=j.version LEFT JOIN quote_public_acceptances pa ON pa.tenant_id=j.tenant_id AND pa.quote_node_id=j.quote_node_id AND pa.version=j.version LEFT JOIN quote_acceptances a ON a.tenant_id=j.tenant_id AND a.quote_node_id=j.quote_node_id AND a.version=j.version LEFT JOIN principals pr ON pr.tenant_id=a.tenant_id AND pr.id=a.customer_principal_id WHERE j.quote_node_id=$1::uuid AND j.version=$2`, j.QuoteNodeID, j.Version).Scan(&document, &offerNo, &originalDigest, &name, &company, &acceptedAt)
	})
	if err != nil {
		return true, m.markFailed(ctx, tenantID, j, "snapshot unavailable")
	}
	profileAssets, renderErr := quotepdf.LoadProfileAssets(ctx, m.pool, m.store, tenantID, document)
	if renderErr != nil {
		return true, m.markFailed(ctx, tenantID, j, "profile assets unavailable")
	}
	pdf, renderErr := quotepdf.Render(ctx, m.assets, quotepdf.Payload{Document: document, OfferNo: offerNo, ProfileAssets: profileAssets, Accepted: &quotepdf.Stamp{Name: name, Company: company, At: acceptedAt.UTC().Format(time.RFC3339), Digest: originalDigest}})
	if renderErr != nil {
		return true, m.markFailed(ctx, tenantID, j, "PDF render failed")
	}
	prepared, err := m.store.Stage(ctx, tenantID, bytes.NewReader(pdf))
	if err != nil {
		return true, m.markFailed(ctx, tenantID, j, "receipt storage failed")
	}
	defer prepared.Close()
	if prepared.ContentType != "application/pdf" {
		return true, m.markFailed(ctx, tenantID, j, "invalid receipt type")
	}
	err = db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := m.gate(ctx, tx, tenantID, fence.PermStepsApply); err != nil {
			return err
		}
		current, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid AND version=$2 FOR UPDATE`, j.QuoteNodeID, j.Version))
		if err != nil {
			return err
		}
		if current.State != "rendering" || current.Attempts != j.Attempts || current.ReceiptSHA256 != nil {
			return errors.New("render lease lost")
		}
		// serviceActor may append principal.created. Keep its domain lock before
		// the blob lock, and reserve the receipt before any possible event.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "quote-confirmation:"+tenantID); err != nil {
			return err
		}
		if err := attachments.LockBlobs(ctx, tx, tenantID, prepared.SHA256); err != nil {
			return err
		}
		p, err := serviceActor(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if err := attachments.Publish(ctx, tx, attachments.OwnerReceipt, prepared); err != nil {
			return err
		}
		id := j.QuoteNodeID
		event, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.confirmation_ready", After: map[string]any{"version": j.Version, "file_sha256": prepared.SHA256, "renderer_version": quotepdf.RendererVersion}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO quote_confirmation_receipts(tenant_id,quote_node_id,version,acceptance_event_id,original_content_sha256,file_sha256,renderer_version,stored_event_id) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8)`, tenantID, id, j.Version, j.AcceptanceEventID, originalDigest, prepared.SHA256, quotepdf.RendererVersion, event.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE quote_confirmation_jobs SET state='ready',receipt_sha256=$3,renderer_version=$4,lease_until=NULL,updated_at=clock_timestamp() WHERE quote_node_id=$1::uuid AND version=$2`, id, j.Version, prepared.SHA256, quotepdf.RendererVersion)
		return err
	})
	return true, err
}
func (m *Module) markFailed(ctx context.Context, tenantID string, j Job, reason string) error {
	return db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		current, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid AND version=$2 FOR UPDATE`, j.QuoteNodeID, j.Version))
		if err != nil {
			return err
		}
		if current.State != "rendering" || current.Attempts != j.Attempts {
			return nil
		}
		p, err := serviceActor(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		id := j.QuoteNodeID
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.confirmation_failed", After: map[string]any{"version": j.Version, "state": "failed"}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE quote_confirmation_jobs SET state='failed',last_safe_error=$3,lease_until=NULL,next_attempt_at=clock_timestamp()+make_interval(secs=>20*$4*$4),updated_at=clock_timestamp() WHERE quote_node_id=$1::uuid AND version=$2`, id, j.Version, reason, j.Attempts)
		return err
	})
}
