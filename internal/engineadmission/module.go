// SPDX-License-Identifier: AGPL-3.0-only
package engineadmission

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct {
	pool *pgxpool.Pool
	prs  WorkPRReader
	now  func() time.Time
}

func New(pool *pgxpool.Pool, prs WorkPRReader) *Module {
	return &Module{pool: pool, prs: prs, now: func() time.Time { return time.Now().UTC() }}
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/engine/admission", m.admit)
	mux.HandleFunc("GET /api/projects/{projectId}/admission-settings", m.settings)
	mux.HandleFunc("PUT /api/projects/{projectId}/admission-settings", m.settings)
}

type failure struct {
	code    int
	message string
}

func (e *failure) Error() string          { return e.message }
func fail(code int, message string) error { return &failure{code, message} }
func writeError(w http.ResponseWriter, err error) {
	var e *failure
	switch {
	case errors.As(err, &e):
		httpapi.WriteError(w, e.code, e.message)
	case errors.Is(err, authz.ErrForbidden), errors.Is(err, db.ErrKeyAuthorityChanged):
		httpapi.WriteError(w, 403, "permission denied")
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, 404, "project not found")
	default:
		httpapi.WriteError(w, 503, "admission unavailable; no decision recorded")
	}
}
func caller(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, 401, "authentication required")
		return p, false
	}
	return p, true
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fail(400, "invalid admission JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fail(400, "one JSON object required")
	}
	return nil
}
func projectTx(ctx context.Context, tx pgx.Tx, key string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE k.slug='project' AND n.deleted_at IS NULL AND (n.id::text=$1 OR n.key=$1 OR coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1))=$1) ORDER BY n.id LIMIT 2`, key)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", pgx.ErrNoRows
	}
	if len(ids) > 1 {
		return "", fail(400, "ambiguous project")
	}
	return ids[0], nil
}
func settingsTx(ctx context.Context, tx pgx.Tx, project string) (Settings, error) {
	out := Settings{ProjectID: project}
	err := tx.QueryRow(ctx, `SELECT shadow_enabled,revision FROM engine_admission_settings WHERE project_id=$1`, project).Scan(&out.ShadowEnabled, &out.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, err
}
func admissionAuthority(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	if err := authz.RequireTx(ctx, tx, p, "engine.admission", authz.Scope{ProjectID: project}); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, p, agentplan.ReadScope, authz.Scope{}); err != nil {
		return err
	}
	_, err := agentplan.CallerOwnerTx(ctx, tx, p)
	return err
}
func accountAuthority(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	permission := "account.read"
	if p.Kind == tenant.Agent {
		permission = "account.overview.read"
	}
	return authz.RequireTx(ctx, tx, p, permission, authz.Scope{})
}

func (m *Module) admit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var in Request
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !in.valid() {
		writeError(w, fail(400, "invalid admission request"))
		return
	}
	if in.RequestID == "" {
		in.RequestID = rand.Text()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	var project string
	var enabled bool
	// Validate visibility and authority before a network read. The final write
	// below repeats both after taking the same tenant fence as access changes.
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		project, err = projectTx(ctx, tx, in.Project)
		if err != nil {
			return err
		}
		if err = admissionAuthority(ctx, tx, p, project); err != nil {
			return err
		}
		s, err := settingsTx(ctx, tx, project)
		enabled = s.ShadowEnabled
		if err == nil && enabled {
			err = accountAuthority(ctx, tx, p)
		}
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	wip := 0
	var wipErr error
	var wipAt time.Time
	if enabled && in.Kind == "first_build" && in.Harness != "" {
		readCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		if m.prs == nil {
			wipErr = errWIP
		} else {
			wip, wipErr = m.prs.Count(readCtx, p.TenantID)
		}
		stop()
		wipAt = m.now()
	}
	var out Decision
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		current, err := projectTx(ctx, tx, in.Project)
		if err != nil {
			return err
		}
		if current != project {
			return fail(409, "project changed; evaluate again")
		}
		if err = admissionAuthority(ctx, tx, p, project); err != nil {
			return err
		}
		settings, err := settingsTx(ctx, tx, project)
		if err != nil {
			return err
		}
		if settings.ShadowEnabled {
			if err = accountAuthority(ctx, tx, p); err != nil {
				return err
			}
		}
		var previous, digestBefore string
		err = tx.QueryRow(ctx, `SELECT decision::text,request_sha256 FROM engine_admission_decisions WHERE actor_principal_id=$1 AND request_id=$2`, p.ID, in.RequestID).Scan(&previous, &digestBefore)
		if err == nil {
			if digestBefore != digest {
				return fail(409, "request ID reused with different input")
			}
			return json.Unmarshal([]byte(previous), &out)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		at := m.now()
		out = Decision{RequestID: in.RequestID, ProjectID: project, Reason: "shadow_disabled", Mode: "off", EvaluatedAt: at}
		if settings.ShadowEnabled {
			out.Mode = "shadow"
			// Savepoint: unreadable/malformed inputs can still produce an audited deny.
			inputTx, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			var until *time.Time
			out.Reason, until, err = m.evaluate(ctx, inputTx, p, in, project, wip, wipErr, wipAt, at)
			if err != nil {
				if rollbackErr := inputTx.Rollback(ctx); rollbackErr != nil {
					return rollbackErr
				}
				if errors.Is(err, authz.ErrForbidden) {
					return err
				}
				if out.Reason != "daily_limit_unknown" {
					out.Reason = "inputs_unreadable"
				}
			} else if err = inputTx.Commit(ctx); err != nil {
				return err
			}
			out.Allowed = out.Reason == "allowed"
			out.RetryAfter = retry(out.Reason, until, at)
			if in.ScriptAllowed != nil {
				same := *in.ScriptAllowed == out.Allowed
				out.ScriptAgrees = &same
			}
		}
		decision, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO engine_admission_decisions(tenant_id,actor_principal_id,request_id,project_id,request_sha256,decision,evaluated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, p.ID, in.RequestID, project, digest, decision, at); err != nil {
			return err
		}
		// This event is the replay source for the decision projection. Private plan,
		// account, calendar and host measurements are never published in its snapshot.
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "engine.admission_decided", After: map[string]any{"request": in, "request_sha256": digest, "decision": out}, At: &at})
		return err // Event counter last; no subsequent resource locks or writes.
	})
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) evaluate(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Request, project string, wip int, wipErr error, wipAt, now time.Time) (string, *time.Time, error) {
	if in.Harness == "" {
		return "harness_required", nil, nil
	}
	plan, err := agentplan.ReadTx(ctx, tx, p)
	if err != nil {
		return "daily_limit_unknown", nil, err
	}
	if err := agentaccounts.PopulateDailyTx(ctx, tx, p, &plan, now); err != nil {
		return "daily_limit_unknown", nil, err
	}
	plan, allDenied, err := agentaccounts.ProjectDailySnapshot(ctx, tx, plan, project)
	if err != nil {
		return "inputs_unreadable", nil, err
	}
	if allDenied[in.Harness] {
		return "context", nil, nil
	}
	if reason := planReason(in, plan); reason != "" {
		return reason, nil, nil
	}
	if daily := agentplan.DailyStart(plan, in.Harness, now); daily.Reason != "" {
		return daily.Reason, daily.Until, nil
	}
	if in.Kind == "first_build" {
		if wipErr != nil || wip < 0 || wipAt.IsZero() || now.Sub(wipAt) > time.Minute || wipAt.After(now) {
			return "wip_unreadable", nil, nil
		}
		if wip > 8 {
			return "wip_limit", nil, nil
		}
	}
	capacity, err := agentaccounts.EngineCapacityTx(ctx, tx, p, plan.PrincipalID, in.Harness, in.Estimate, now, project)
	if err != nil {
		return "", nil, err
	}
	return capacity.Reason, capacity.Until, nil
}

func (m *Module) settings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id := r.PathValue("projectId")
	if !workorders.UUID(id) {
		writeError(w, fail(400, "project UUID required"))
		return
	}
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		ShadowEnabled    *bool  `json:"shadow_enabled"`
	}
	if r.Method == http.MethodPut {
		if p.Kind != tenant.Person {
			writeError(w, fail(403, "only a person may change admission settings"))
			return
		}
		if err := decode(w, r, &in); err != nil {
			writeError(w, err)
			return
		}
		if in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || in.ShadowEnabled == nil {
			writeError(w, fail(400, "reviewed revision and switch required"))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out Settings
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if r.Method == http.MethodPut {
			if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		project, err := projectTx(ctx, tx, id)
		if err != nil {
			return err
		}
		permission := "engine.read"
		if r.Method == http.MethodPut {
			permission = "engine.manage"
		}
		if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		out, err = settingsTx(ctx, tx, project)
		if err != nil || r.Method == http.MethodGet {
			return err
		}
		if out.Revision != *in.ExpectedRevision {
			return fail(409, "admission settings changed; reload")
		}
		before := out
		out.ShadowEnabled = *in.ShadowEnabled
		out.Revision++
		at := m.now()
		if _, err = tx.Exec(ctx, `INSERT INTO engine_admission_settings(tenant_id,project_id,shadow_enabled,revision,updated_by,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_id,project_id) DO UPDATE SET shadow_enabled=EXCLUDED.shadow_enabled,revision=EXCLUDED.revision,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`, p.TenantID, project, out.ShadowEnabled, out.Revision, p.ID, at); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "engine.admission_settings_changed", Before: before, After: out, At: &at})
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
