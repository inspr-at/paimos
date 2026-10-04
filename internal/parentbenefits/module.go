// SPDX-License-Identifier: AGPL-3.0-only
package parentbenefits

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Chat func(context.Context, string, string, int64, []modelprovider.Message) (modelprovider.Completion, error)
type Module struct {
	pool        *pgxpool.Pool
	chat        Chat
	afterTenant string
}

func New(pool *pgxpool.Pool, chat Chat) *Module { return &Module{pool: pool, chat: chat} }
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{nodeId}/benefit-generation", m.get)
	mux.HandleFunc("POST /api/nodes/{nodeId}/benefit-generation/retry", m.retry)
}

type Status struct {
	IsParent   bool      `json:"is_parent"`
	State      string    `json:"status"`
	Generation string    `json:"generation"`
	Revision   time.Time `json:"revision"`
	Generated  bool      `json:"generated"`
	Error      string    `json:"error,omitempty"`
}
type target struct {
	Status
	id, project string
	fields      []byte
	raw         []byte
}

func load(ctx context.Context, tx pgx.Tx, id string) (target, error) {
	var t target
	t.id = id
	err := tx.QueryRow(ctx, `SELECT coalesce(project_id::text,''),CASE WHEN coalesce(octet_length(fields->>'pill_en'),0)+coalesce(octet_length(fields->>'pill_de'),0)+coalesce(octet_length(fields->>'benefit_en'),0)+coalesce(octet_length(fields->>'benefit_de'),0)<=16000 THEN aeon_benefit_texts(fields) ELSE '{}'::jsonb END,benefit_generation,updated_at,aeon_work_status_is_parent(id) FROM nodes WHERE id=$1::uuid AND deleted_at IS NULL`, id).Scan(&t.project, &t.fields, &t.raw, &t.Revision, &t.IsParent)
	if err != nil {
		return t, err
	}
	var meta struct {
		Status     string `json:"status"`
		Generation string `json:"generation"`
		Generated  bool   `json:"generated"`
		Error      string `json:"error"`
	}
	if err = json.Unmarshal(t.raw, &meta); err != nil {
		return t, err
	}
	t.State = meta.Status
	if t.State == "" {
		t.State = "none"
	}
	t.Generation = meta.Generation
	t.Generated = meta.Generated
	t.Error = meta.Error
	return t, nil
}

type fault struct {
	code    int
	message string
}

func (f fault) Error() string { return f.message }
func respond(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	var f fault
	switch {
	case errors.As(err, &f):
		respond(w, f.code, map[string]string{"error": f.message})
	case errors.Is(err, pgx.ErrNoRows):
		respond(w, 404, map[string]string{"error": "node not found"})
	case errors.Is(err, authz.ErrForbidden):
		respond(w, 403, map[string]string{"error": "permission denied"})
	default:
		respond(w, 503, map[string]string{"error": "Benefit generation is unavailable. Try again."})
	}
}
func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		respond(w, 401, map[string]string{"error": "authentication required"})
	}
	return p, ok
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validUUID(s string) bool { return uuidPattern.MatchString(s) }
func fence(ctx context.Context, tx pgx.Tx, tid string) error {
	return agentpairing.LockMutation(ctx, tx)
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if !validUUID(r.PathValue("nodeId")) {
		fail(w, fault{400, "invalid node id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var t target
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		t, err = load(ctx, tx, r.PathValue("nodeId"))
		if err != nil {
			return err
		}
		return authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: t.project})
	})
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, t.Status)
}
func (m *Module) retry(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		fail(w, fault{403, "person required"})
		return
	}
	id := r.PathValue("nodeId")
	if !validUUID(id) {
		fail(w, fault{400, "invalid node id"})
		return
	}
	var in struct {
		Generation string    `json:"expected_generation"`
		Revision   time.Time `json:"expected_revision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || !validUUID(in.Generation) || in.Revision.IsZero() {
		fail(w, fault{400, "expected generation and revision required"})
		return
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		fail(w, fault{400, "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out target
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := fence(ctx, tx, p.TenantID); err != nil {
			return err
		}
		var err error
		out, err = load(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: out.project}); err != nil {
			return err
		}
		if !out.IsParent || out.State != "failed" || out.Generation != in.Generation || !out.Revision.Equal(in.Revision) {
			return fault{409, "Parent or generation changed. Reload before retrying."}
		}
		var completed bool
		if err := tx.QueryRow(ctx, `SELECT aeon_work_status_category(n.state,k.field_schema) IN ('done','accepted','delivered') FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1`, id).Scan(&completed); err != nil {
			return err
		}
		if !completed {
			return fault{409, "Parent is no longer Done."}
		}
		if err := writeMeta(ctx, tx, id, `jsonb_build_object('status','queued','generation',gen_random_uuid()::text,'generated',coalesce((benefit_generation->>'generated')::boolean,false))`); err != nil {
			return err
		}
		out, err = load(ctx, tx, id)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "parent_benefits.retried", After: out.Status})
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 202, out.Status)
}
func writeMeta(ctx context.Context, tx pgx.Tx, id, expression string, args ...any) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.parent_benefit_writer','on',true)`); err != nil {
		return err
	}
	values := append([]any{id}, args...)
	_, err := tx.Exec(ctx, `UPDATE nodes SET benefit_generation=`+expression+` WHERE id=$1::uuid`, values...)
	return err
}
