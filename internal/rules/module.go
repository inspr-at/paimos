// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) httpapi.Module { return &Module{pool: pool} }

type endpoint func(*http.Request, pgx.Tx, tenant.Principal) (any, error)

// What a write answers when its COMMIT was sent but not acknowledged. Only a
// batch publication can find out afterwards (its answer is stored, keyed by
// person and request); every other write says it does not know.
const (
	unknownChange = "The result is unknown. Reload to see the current state before trying again."
	unknownSet    = "The set may have been created. Reload and check before creating it again."
	unknownLayer  = "The layer may have been created. Reload and check before creating it again."
	unknownBatch  = "The server did not confirm whether this publication was saved. Repeating the same request is safe: an identical batch returns its stored result and never publishes twice."
)

func (m *Module) Mount(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern, permission string
		handler             endpoint
		unknown             string
	}{
		{"GET /api/rules/layers", "rules.read", m.layers, ""}, {"POST /api/rules/layers", "rules.write", m.createLayer, unknownLayer},
		{"GET /api/rules/sets", "rules.read", m.sets, ""}, {"POST /api/rules/sets", "rules.write", m.createSet, unknownSet},
		{"GET /api/rules/sets/{setId}", "rules.read", m.getSet, ""}, {"PUT /api/rules/sets/{setId}/draft", "rules.write", m.draft, unknownChange},
		{"POST /api/rules/sets/{setId}/publish", "rules.publish", m.publish, unknownChange}, {"POST /api/rules/sets/{setId}/restore", "rules.publish", m.restore, unknownChange},
		{"GET /api/rules/sets/{setId}/versions", "rules.read", m.versions, ""}, {"GET /api/rules/sets/{setId}/versions/{version}", "rules.read", m.version, ""},
		{"GET /api/rules/merged", "rules.read", m.merged, ""},
		{"GET /api/rules/channels", "rules.read", m.channels, ""},
		{"GET /api/rules/comparisons", "rules.read", m.listComparisons, ""},
		{"POST /api/rules/comparisons", "rules.write", m.createComparison, "The comparison may have been saved. Reload before uploading it again."},
		{"GET /api/rules/explained", "rules.read", m.explained, ""},
		{"PUT /api/rules/sets/{setId}/tldr", "rules.write", m.tldr, unknownChange},
		{"GET /api/rules/budget", "rules.read", m.getBudget, ""}, {"PUT /api/rules/budget", "settings.manage", m.putBudget, unknownChange},
		{"POST /api/rules/publish", "rules.publish", m.publishBatch, unknownBatch},
	} {
		mux.HandleFunc(route.pattern, m.endpoint(route.permission, route.unknown, route.handler))
	}
}
func (m *Module) endpoint(permission, unknown string, fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
			writeFailure(w, fail(401, "unauthorized", "authentication required"))
			return
		}
		if p.Kind != tenant.Person && p.Kind != tenant.Agent {
			writeFailure(w, authz.ErrForbidden)
			return
		}
		if id := r.PathValue("setId"); id != "" && !workorders.UUID(id) {
			writeFailure(w, fail(400, "invalid_scope", "invalid set UUID"))
			return
		}
		if permission == "rules.publish" && p.Kind != tenant.Person {
			writeFailure(w, authz.ErrForbidden)
			return
		}
		// The whole bounded body is read before any transaction or lock, so a slow
		// or stalled upload never holds the tenant locks taken below.
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeFailure(w, fail(413, "invalid_request", "request body exceeds 2 MiB"))
				return
			}
			writeFailure(w, fail(400, "invalid_request", "request body could not be read"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		// The transaction's context is never cancelled: pgx answers a cancelled
		// context by dropping the connection rather than rolling back, which
		// would leave the locks to the server's timing. The deadline bounds the
		// work instead: lock and statement timeouts for SQL, explicit checks in
		// the budget's CPU loops, and InTenant rolls back when fn fails.
		deadline := time.Now().Add(txTimeout)
		pending := &outcome{}
		r = r.WithContext(context.WithValue(withDeadline(context.WithoutCancel(r.Context()), deadline), outcomeKey{}, pending))
		var out any
		committing := false
		err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
			// Bounded waits and work: a lock that cannot be had in time fails the
			// request (503) instead of queueing behind or ahead of access changes.
			// The server enforces the deadline on the whole transaction too:
			// transaction_timeout (Postgres 17+) and the idle-in-transaction
			// timeout end the session, and with it every lock, at the deadline,
			// however long the application spends between statements.
			remaining := fmt.Sprintf("%dms", max(time.Until(deadline).Milliseconds(), 1))
			if _, err := tx.Exec(r.Context(), `SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$2,true),set_config('transaction_timeout',$3,true),set_config('idle_in_transaction_session_timeout',$3,true)`, lockTimeout, statementTimeout, remaining); err != nil {
				return err
			}
			if err := authz.RequireTx(r.Context(), tx, p, permission, authz.Scope{AnyProject: true}); err != nil {
				return err
			}
			owner, err := actorOwner(r.Context(), tx, p)
			if err != nil {
				return err
			}
			agent := ""
			if p.Kind == tenant.Agent {
				agent = p.ID
			}
			if err = enterRules(r.Context(), tx, owner, agent); err != nil {
				return err
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0)),set_config('aeon.rules_write','on',true)`); err != nil {
					return err
				}
				if err = lockAccess(r.Context(), tx, p.TenantID); err != nil {
					return err
				}
				// Project visibility was derived when the transaction began; derive
				// it again under the access lock so a revocation that committed
				// meanwhile is honoured by every read and decision that follows.
				var creator any
				if workorders.UUID(p.KeyCreatorID) {
					creator = p.KeyCreatorID
				}
				if _, err = tx.Exec(r.Context(), `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, p.TenantID, p.ID, creator); err != nil {
					return err
				}
				if owner, err = actorOwner(r.Context(), tx, p); err != nil {
					return err
				}
				if err = enterRules(r.Context(), tx, owner, agent); err != nil {
					return err
				}
				if err = ensureKinds(r.Context(), tx, p); err != nil {
					return err
				}
			}
			r = r.WithContext(withDoctrineCatalog(r.Context()))
			if out, err = fn(r, tx, p); err != nil {
				return err
			}
			// Never commit at or after the deadline: this is the last moment
			// before COMMIT, with a margin for the COMMIT itself.
			if time.Now().After(deadline.Add(-commitMargin)) {
				return errStopped
			}
			committing = true
			return nil
		})
		if err != nil && committing && !rolledBack(err) {
			// COMMIT was sent and its answer did not arrive: the change may be
			// durable, or still committing. Never answer "nothing changed" here.
			if out, found := m.reconcile(p.TenantID, pending); found {
				httpapi.WriteJSON(w, 200, out)
				return
			}
			writeFailure(w, &Error{Status: 503, Code: "outcome_unknown", Message: unknown})
			return
		}
		if err != nil {
			// Before COMMIT nothing is durable. Past the deadline every such
			// failure is the deadline's: the server may have ended the session.
			if time.Now().After(deadline.Add(-commitMargin)) {
				err = errStopped
			}
			writeFailure(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, out)
	}
}

// outcome carries a batch publication's way to find its stored answer after
// an unanswered COMMIT. Other writes register nothing: they cannot tell.
type outcome struct {
	owner  string
	digest []byte
}
type outcomeKey struct{}

// onUncertain records, for a batch publication, the key of its stored answer.
func onUncertain(r *http.Request, owner string, digest []byte) {
	if o, ok := r.Context().Value(outcomeKey{}).(*outcome); ok {
		o.owner, o.digest = owner, digest
	}
}

// rolledBack reports whether a COMMIT failure is a confirmed rollback: the
// server answered with an error, or pgx saw the transaction end in ROLLBACK.
// Anything else (EOF, a reset, a timeout while waiting) leaves the outcome open.
func rolledBack(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) || errors.Is(err, pgx.ErrTxCommitRollback)
}

// reconcileHook runs before the reconciliation; tests use it to revoke access
// or to make the reconciliation fail.
var reconcileHook = func() error { return nil }

// reconcile looks, on a fresh connection, for the stored answer of a batch
// publication whose COMMIT went unanswered. The row is keyed by tenant, person
// and request digest, so it can only be this request's own. The read does not
// depend on the caller's current access (a revocation after the commit must not
// hide it): it runs without the caller as principal and opens only this
// person's batch rows. Found: the stored answer. Anything else (no row yet,
// the COMMIT perhaps still in flight, or a failed read) is not an answer.
func (m *Module) reconcile(tenantID string, pending *outcome) (any, bool) {
	if pending.digest == nil || reconcileHook() != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()
	var out any
	var found bool
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		ms := fmt.Sprintf("%dms", reconcileTimeout.Milliseconds())
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$1,true),set_config('aeon.rules_access','on',true),set_config('aeon.rules_owner',$2,true)`, ms, pending.owner); err != nil {
			return err
		}
		var err error
		out, found, err = storedBatch(ctx, tx, tenantID, pending.owner, pending.digest)
		return err
	})
	return out, err == nil && found
}

// enterRules turns on rule visibility for this person or agent: it keeps the
// caller's project visibility for project rule layers, then opens workspace
// visibility for company and person rule nodes. No generic API runs in this
// transaction.
func enterRules(ctx context.Context, tx pgx.Tx, owner, agent string) error {
	var visibleProjects string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&visibleProjects); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT set_config('aeon.rules_projects',$3,true),set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_agent',$2,true),set_config('aeon.rules_access','on',true),set_config('aeon.visible_projects','*',true)`, owner, agent, visibleProjects)
	return err
}

type deadlineKey struct{}

// withDeadline records when the request's work must stop; see expired.
func withDeadline(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, deadlineKey{}, at)
}

// expired reports whether the request's deadline has passed.
func expired(ctx context.Context) bool {
	at, ok := ctx.Value(deadlineKey{}).(time.Time)
	return ok && !time.Now().Before(at)
}

// lockAccess takes the tenant row lock that every access change takes (role,
// binding, member and invite mutations in internal/authz) and holds it until
// commit. Every permission decision of a rules write is made after it, so a
// concurrent demotion either commits first and is seen, or waits for this
// write. Order: the tenant advisory lock first, then the row, the same order as
// authz.lockProjectMutation, so the two never deadlock.
//
// NO KEY UPDATE, not UPDATE: it conflicts with the FOR UPDATE that access
// changes take, but not with the KEY SHARE a foreign key check takes when some
// other request inserts a row that references the tenant (a knowledge entry,
// say). Such a request may hold KEY SHARE while it waits for the tenant
// advisory lock held here; FOR UPDATE would close that cycle into a deadlock.
func lockAccess(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, tenantID).Scan(&id)
}

// Bounds of one rules request. Variables so tests can shorten them.
var (
	maxBody          int64 = 2 << 20
	txTimeout              = 30 * time.Second
	lockTimeout            = "5s"
	statementTimeout       = "20s"
	commitMargin           = 50 * time.Millisecond
	reconcileTimeout       = 5 * time.Second
)

func writeFailure(w http.ResponseWriter, err error) {
	var e *Error
	var we *workorders.Error
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &e):
	case errors.Is(err, authz.ErrForbidden):
		e = &Error{Status: 403, Code: "forbidden", Message: "permission or scoped ownership denied"}
	case errors.Is(err, pgx.ErrNoRows):
		e = &Error{Status: 404, Code: "not_found", Message: "rule resource unavailable"}
	case errors.As(err, &we):
		e = &Error{Status: we.Status, Code: "invalid_request", Message: we.Message}
	case errors.As(err, &pe) && (pe.Code == "55P03" || pe.Code == "57014" || pe.Code == "25P03" || pe.Code == "25P04"), errors.Is(err, context.DeadlineExceeded):
		e = &Error{Status: 503, Code: "busy", Message: "the rules store is busy; nothing was changed, try again"}
	case errors.As(err, &pe) && pe.Code == "23505":
		e = &Error{Status: 409, Code: "revision_conflict", Message: "rule identity or version already exists"}
	default:
		e = &Error{Status: 500, Code: "internal_error", Message: "rule operation failed"}
	}
	httpapi.WriteJSON(w, e.Status, e)
}
func actorOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	id := p.ID
	if p.Kind == tenant.Agent {
		// Rule scopes and merged contexts require a human owner. Operator
		// keys have none; never substitute an arbitrary person or service actor.
		if p.KeyCreatorID == "" {
			return "", fail(403, "rules_owner_required", "rules access requires an agent key created by a signed-in person; operator-created keys have no human rule owner")
		}
		id = p.KeyCreatorID
	}
	if !workorders.UUID(id) {
		return "", authz.ErrForbidden
	}
	var owner string
	err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active'`, p.TenantID, id).Scan(&owner)
	return owner, err
}
func permission(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Scope, action string) error {
	if err := ValidateScope(s); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, p, action, authz.Scope{ProjectID: s.ProjectID, AnyProject: action == "rules.read" && s.ProjectID == ""}); err != nil {
		return err
	}
	if action == "rules.publish" && p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	owner, err := actorOwner(ctx, tx, p)
	if err != nil {
		return err
	}
	if s.OwnerID != "" && s.OwnerID != owner {
		return authz.ErrForbidden
	}
	if s.OwnerID != "" {
		if err = principalExists(ctx, tx, s.OwnerID, "person"); err != nil {
			return err
		}
	}
	if s.AgentID != "" {
		if p.Kind == tenant.Agent {
			if s.AgentID != p.ID {
				return authz.ErrForbidden
			}
		} else {
			var controlled bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_keys k JOIN principals a ON a.tenant_id=k.tenant_id AND a.id=k.principal_id WHERE k.tenant_id=$1 AND k.principal_id=$2 AND k.created_by_principal_id=$3 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND a.kind='agent' AND a.status='active')`, p.TenantID, s.AgentID, owner).Scan(&controlled)
			if err != nil {
				return err
			}
			if !controlled {
				// The EXISTS above stays the allow rule. A reason only explains the denial
				// and never grants access when the two disagree. Someone who cannot already
				// see this agent gets the same forbidden result as a missing agent, so the
				// code does not reveal that the agent exists or is inactive.
				allowed, reason, classErr := authz.ClassifyAgentControl(ctx, tx, p.TenantID, owner, s.AgentID)
				if classErr != nil {
					return classErr
				}
				if !allowed && reason != "" {
					visible, seeErr := mayExplainAgentDenial(ctx, tx, p, owner, s.AgentID)
					if seeErr != nil {
						return seeErr
					}
					if visible {
						return &agentControlDenial{reason: reason}
					}
				}
				return authz.ErrForbidden
			}
		}
		if err = principalExists(ctx, tx, s.AgentID, "agent"); err != nil {
			return err
		}
	}
	if s.ProjectID != "" {
		if err = authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: s.ProjectID}); err != nil {
			return err
		}
		var found bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL)`, s.ProjectID).Scan(&found)
		if err != nil {
			return err
		}
		if !found {
			return pgx.ErrNoRows
		}
	}
	if s.TaskID != "" {
		var found bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL)`, s.TaskID, s.ProjectID).Scan(&found)
		if err != nil {
			return err
		}
		if !found {
			return pgx.ErrNoRows
		}
	}
	if action != "rules.read" {
		if s.Layer == "company" {
			if p.Kind != tenant.Person {
				return authz.ErrForbidden
			}
			return authz.RequireTx(ctx, tx, p, "rules.publish", authz.Scope{})
		}
		// Workspace role sets and project work-product rules belong to their
		// administrators. Agents may draft only within their creator's authority.
		if s.Layer == "project" || s.Role != "" {
			person := tenant.Principal{ID: owner, TenantID: p.TenantID, Kind: tenant.Person}
			return authz.RequireTx(ctx, tx, person, "rules.publish", authz.Scope{ProjectID: s.ProjectID})
		}
	}
	return nil
}
func principalExists(ctx context.Context, tx pgx.Tx, id, kind string) error {
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind=$2 AND status='active')`, id, kind).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return pgx.ErrNoRows
	}
	return nil
}
func (m *Module) merged(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	c, snapshots, err := m.mergeInputs(r, tx, p)
	if err != nil {
		return nil, err
	}
	limits, err := LoadBudget(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	maximum, err := RequestMaximum(r)
	if err != nil {
		return nil, err
	}
	cat, err := loadDoctrineCatalog(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	merged, err := MergeDeliveredForClient(c, snapshots, time.Now().UTC(), limits, maximum, cat)
	if err != nil {
		return nil, err
	}
	if err = RecordServedManifest(r.Context(), tx, c, merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// mergeInputs checks the merge selectors and the caller's right to read that
// session file, then loads the live snapshots it is built from under the store
// caps. The merged file and its explanation for people share it.
func (m *Module) mergeInputs(r *http.Request, tx pgx.Tx, p tenant.Principal) (Context, []Snapshot, error) {
	q := r.URL.Query()
	allowed := map[string]bool{"project_id": true, "person_id": true, "agent_id": true, "role": true, "harness": true, "task_id": true}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return Context{}, nil, fail(400, "invalid_scope", "unknown or repeated merge selector")
		}
	}
	c := Context{p.TenantID, q.Get("project_id"), q.Get("person_id"), q.Get("agent_id"), q.Get("role"), q.Get("harness"), q.Get("task_id")}
	if err := ValidateContext(c); err != nil {
		return c, nil, err
	}
	owner, err := actorOwner(r.Context(), tx, p)
	if err != nil {
		return c, nil, err
	}
	if owner != c.PersonID {
		return c, nil, authz.ErrForbidden
	}
	if p.Kind == tenant.Agent && c.AgentID != p.ID {
		return c, nil, authz.ErrForbidden
	}
	scope := Scope{Layer: "project", ProjectID: c.ProjectID}
	if err = permission(r.Context(), tx, p, scope, "rules.read"); err != nil {
		return c, nil, surfacePreviewReason(err)
	}
	if c.AgentID != "" {
		scope = Scope{Layer: "agent", OwnerID: c.PersonID, AgentID: c.AgentID}
		if c.TaskID != "" {
			scope.TaskID = c.TaskID
			scope.ProjectID = c.ProjectID
		}
		if err = permission(r.Context(), tx, p, scope, "rules.read"); err != nil {
			return c, nil, surfacePreviewReason(err)
		}
	}
	ss, err := allSets(r.Context(), tx, "")
	if err != nil {
		return c, nil, err
	}
	// Count each snapshot as it is read. A snapshot costs at least one rule, so
	// once the running total is at the cap the rest are refused unread. A
	// snapshot that itself crosses 2,000 rules or 2 MiB is the last one loaded.
	budget := &work{ctx: r.Context()}
	snapshots := []Snapshot{}
	for _, s := range ss {
		if !s.Scope.matches(c) || s.PublishedVersion == "" {
			continue
		}
		if err = permission(r.Context(), tx, p, s.Scope, "rules.read"); err != nil {
			return c, nil, surfacePreviewReason(err)
		}
		if budget.rules >= maxBudgetRules {
			return c, nil, errMergeStoreTooLarge
		}
		snap, err := loadVersion(r.Context(), tx, s.ID, s.PublishedVersion)
		if err != nil {
			return c, nil, err
		}
		if err = budget.admit(snap); err != nil {
			return c, nil, err
		}
		snapshots = append(snapshots, snap)
	}
	return c, snapshots, nil
}

// fields is the storage envelope on ordinary Aeon nodes. Scope is copied onto
// every child so RLS never needs a recursive parent lookup.
type fields struct {
	Resource         string    `json:"_aeon_rule_resource"`
	Scope            Scope     `json:"scope"`
	Name             string    `json:"name,omitempty"`
	Revision         int64     `json:"revision,omitempty"`
	PublishedVersion string    `json:"published_version,omitempty"`
	Rule             *Rule     `json:"rule,omitempty"`
	Version          string    `json:"version,omitempty"`
	Snapshot         *Snapshot `json:"snapshot,omitempty"`
	TLDR             *TLDR     `json:"tldr,omitempty"`
}

func decodeFields(raw []byte) (fields, error) {
	var f fields
	err := json.Unmarshal(raw, &f)
	return f, err
}
func ensureKinds(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	_, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES ($1,'aeon_rule_layer','Rule layer','ARL','shield'),($1,'aeon_rule_set','Rule set','ARS','shield'),($1,'aeon_rule','Rule','ARR','shield'),($1,'aeon_rule_version','Rule version','ARV','shield') ON CONFLICT (tenant_id,slug) DO NOTHING`, p.TenantID)
	return err
}
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }
func resourceSlug(resource string) string {
	if resource == "rule" {
		return "aeon_rule"
	}
	return "aeon_rule_" + resource
}
func insertNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, parent, title string, f fields) (string, error) {
	var id string
	var parentID any
	if parent != "" {
		parentID = parent
	}
	err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields) SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,$2,$3,$4 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug=$5 RETURNING id::text`, p.TenantID, title, parentID, jsonBytes(f), resourceSlug(f.Resource)).Scan(&id)
	return id, err
}
func nodeFields(ctx context.Context, tx pgx.Tx, id, resource string) (fields, string, error) {
	if !workorders.UUID(id) {
		return fields{}, "", fail(400, "invalid_scope", "invalid resource UUID")
	}
	var raw []byte
	var parent string
	err := tx.QueryRow(ctx, `SELECT fields,coalesce(parent_id::text,'') FROM nodes WHERE id=$1 AND rule_resource=$2 AND deleted_at IS NULL`, id, resource).Scan(&raw, &parent)
	if err != nil {
		return fields{}, "", err
	}
	f, err := decodeFields(raw)
	return f, parent, err
}
func isDenied(err error) bool {
	return errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows)
}

// agentControlDenial is a forbidden decision that also names why this person
// does not control the named agent. List and budget checks use isDenied and
// skip the layer. Only the merged preview turns the reason into a response code.
type agentControlDenial struct {
	reason string
}

func (e *agentControlDenial) Error() string { return authz.PreviewDenialMessage(e.reason) }
func (e *agentControlDenial) Unwrap() error { return authz.ErrForbidden }

func surfacePreviewReason(err error) error {
	var denial *agentControlDenial
	if errors.As(err, &denial) {
		return fail(403, denial.reason, authz.PreviewDenialMessage(denial.reason))
	}
	return err
}

// mayExplainAgentDenial reports whether a specific denial would tell this
// person something they can already see. GET /members lists every agent to a
// caller with members.read. A key they created is already known to them.
func mayExplainAgentDenial(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, agentID string) (bool, error) {
	var created bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_keys WHERE tenant_id=$1 AND principal_id=$2 AND created_by_principal_id=$3)`, p.TenantID, agentID, owner).Scan(&created)
	if err != nil {
		return false, err
	}
	if created {
		return true, nil
	}
	err = authz.RequireTx(ctx, tx, p, "members.read", authz.Scope{})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, authz.ErrForbidden) {
		return false, nil
	}
	return false, err
}
