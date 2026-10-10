// SPDX-License-Identifier: AGPL-3.0-only
// Package routineactions brokers a closed catalogue of run-bound mutations.
package routineactions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const MaxBodyBytes = 65536
const MaxRevision int64 = 253402300799999999

type WorkCreate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}
type WorkUpdate struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"`
}
type KnowledgeWrite struct {
	Type  string `json:"type"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Body  string `json:"body"`
}
type Request struct {
	AttemptID        string          `json:"attempt_id"`
	ActionKey        string          `json:"action_key"`
	Kind             string          `json:"kind"`
	ProjectID        string          `json:"project_id"`
	TargetID         string          `json:"target_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	WorkCreate       *WorkCreate     `json:"work_create,omitempty"`
	WorkUpdate       *WorkUpdate     `json:"work_update,omitempty"`
	KnowledgeWrite   *KnowledgeWrite `json:"knowledge_write,omitempty"`
}
type Receipt struct {
	ID            string `json:"id"`
	RunID         string `json:"run_id"`
	ActionKey     string `json:"action_key"`
	Kind          string `json:"kind"`
	RequestDigest string `json:"request_digest"`
	State         string `json:"state"`
	NodeID        string `json:"node_id"`
	Revision      int64  `json:"revision"`
}

// Binding is loaded from persisted lineage, never from a client credential or
// owner supplied in the request. Current consent is checked by the authorizer.
type Binding struct {
	RunID, RecurrenceID, OwnerID, ExecutionPrincipalID, ProjectID, ParentID string
	DefinitionRevision, ConsentRevision                                     int64
	Assignment                                                              json.RawMessage
	AssignmentDigest                                                        string
}

// AuthorizeTx resolves current definition/qualification/consent and the fixed
// evaluator in the same final transaction. No network/model work is permitted.
type AuthorizeTx func(context.Context, pgx.Tx, tenant.Principal, Binding, routineguard.Context) (tenant.Principal, routineguard.Decision, error)

type Broker struct {
	pool      *pgxpool.Pool
	authorize AuthorizeTx
}

func New(pool *pgxpool.Pool, authorize AuthorizeTx) *Broker {
	return &Broker{pool: pool, authorize: authorize}
}
func (b *Broker) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/routine-runs/{runId}/actions", b.handle)
}

var actionKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
var knowledgeSlug = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func bounded(s string, n int, required bool) bool {
	return len(s) <= n && utf8.ValidString(s) && !strings.ContainsRune(s, '\x00') && (!required || strings.TrimSpace(s) != "")
}

func (r Request) Validate() error {
	if !workorders.UUID(r.AttemptID) || !workorders.UUID(r.ProjectID) || !workorders.UUID(r.TargetID) || !actionKey.MatchString(r.ActionKey) || r.ActionKey == "occurrence" || r.ExpectedRevision < 1 || r.ExpectedRevision > MaxRevision {
		return workorders.Fail(400, "invalid_action_binding")
	}
	count := 0
	for _, has := range []bool{r.WorkCreate != nil, r.WorkUpdate != nil, r.KnowledgeWrite != nil} {
		if has {
			count++
		}
	}
	if count != 1 {
		return workorders.Fail(400, "one_action_payload_required")
	}
	switch r.Kind {
	case "work.create":
		v := r.WorkCreate
		if v == nil || !bounded(v.Title, 500, true) || !bounded(v.Body, 32768, false) {
			return workorders.Fail(400, "invalid_work_create")
		}
	case "work.update":
		v := r.WorkUpdate
		if v == nil || v.Title == nil && v.Body == nil && v.State == nil {
			return workorders.Fail(400, "invalid_work_update")
		}
		for _, v := range []struct {
			value    *string
			n        int
			required bool
		}{{v.Title, 500, true}, {v.Body, 32768, false}, {v.State, 64, true}} {
			if v.value != nil && !bounded(*v.value, v.n, v.required) {
				return workorders.Fail(400, "invalid_work_update")
			}
		}
	case "knowledge.write":
		v := r.KnowledgeWrite
		if v == nil || !slices.Contains([]string{"runbook", "guideline", "memory", "external-system", "related-project"}, v.Type) || !knowledgeSlug.MatchString(v.Slug) || !bounded(v.Title, 500, true) || !bounded(v.Body, 32768, false) {
			return workorders.Fail(400, "invalid_knowledge_write")
		}
	default:
		return workorders.Fail(400, "unknown_action_type")
	}
	return nil
}
func (r Request) GuardContext() routineguard.Context {
	raw, _ := json.Marshal(r)
	c := routineguard.Context{Checkpoint: "action", Action: r.Kind, Text: string(raw), Paths: []string{}, PayloadBytes: int64(len(raw))}
	if r.KnowledgeWrite != nil {
		c.Paths = []string{"knowledge/" + r.KnowledgeWrite.Type + "/" + r.KnowledgeWrite.Slug}
	}
	return c
}
func (b *Broker) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "unauthorized")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		httpapi.WriteError(w, 413, "action_body_too_large")
		return
	}
	var in Request
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !utf8.Valid(raw) || d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		httpapi.WriteError(w, 400, "invalid_action_request")
		return
	}
	out, err := b.Execute(r.Context(), p, r.PathValue("runId"), in)
	if err != nil {
		workorders.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// Execute commits the internal effect and immutable receipt together. Every
// retry rechecks live authority; no success receipt survives a failed write.
func (b *Broker) Execute(ctx context.Context, p tenant.Principal, run string, in Request) (Receipt, error) {
	if !workorders.UUID(run) {
		return Receipt{}, workorders.Fail(400, "invalid_run_id")
	}
	if err := in.Validate(); err != nil {
		return Receipt{}, err
	}
	ctx = tenant.WithPrincipal(ctx, p)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out Receipt
	err := db.InTenant(ctx, b.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		// Pairing enters tenant -> tree -> pairing before any run or target rows;
		// normal node terminal guards may reenter the already-held fences.
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		binding, err := loadBinding(ctx, tx, p, run, in.AttemptID)
		if err != nil {
			return err
		}
		if in.ProjectID != binding.ProjectID {
			return workorders.Fail(409, "action_project_changed")
		}
		permission := "nodes.write"
		if in.Kind == "knowledge.write" {
			permission = "knowledge.write"
		}
		owner := tenant.Principal{ID: binding.OwnerID, TenantID: p.TenantID, Kind: tenant.Person}
		for _, who := range []tenant.Principal{p, owner} {
			if err := authz.RequireTx(ctx, tx, who, permission, authz.Scope{ProjectID: in.ProjectID}); err != nil {
				return err
			}
		}
		if b.authorize == nil {
			return workorders.Fail(409, "routine_grant_unavailable")
		}
		actor, decision, err := b.authorize(ctx, tx, p, binding, in.GuardContext())
		if err != nil {
			return err
		}
		if actor.TenantID != p.TenantID || actor.ID != binding.ExecutionPrincipalID {
			return authz.ErrForbidden
		}
		if !decision.CanExecute() || decision.ContextDigest != routineguard.Digest(in.GuardContext()) {
			return workorders.Fail(409, "guardrail_evaluation_required")
		}
		if err := authz.RequireTx(ctx, tx, actor, permission, authz.Scope{ProjectID: in.ProjectID}); err != nil {
			return err
		}
		out, err = applyTx(ctx, tx, actor, binding, in, decision.PolicyDigest)
		return err
	})
	if err != nil {
		return Receipt{}, err
	}
	return out, nil
}
func loadBinding(ctx context.Context, tx pgx.Tx, p tenant.Principal, run, attempt string) (Binding, error) {
	var b Binding
	var state, attemptState, attemptPrincipal, attemptDigest string
	var consent bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.recurrence_id::text,r.owner_principal_id::text,coalesce(r.execution_principal_id::text,''),r.output_project_id::text,r.output_parent_id::text,r.definition_revision,r.consent_revision,r.assignment,r.assignment_digest,r.state,r.execute_consent,a.state,coalesce(a.principal_id::text,''),a.assignment_digest
 FROM routine_runs r JOIN routine_attempts a ON a.tenant_id=r.tenant_id AND a.run_id=r.id AND a.id=$2
 WHERE r.id=$1 FOR NO KEY UPDATE OF r,a`, run, attempt).Scan(&b.RunID, &b.RecurrenceID, &b.OwnerID, &b.ExecutionPrincipalID, &b.ProjectID, &b.ParentID, &b.DefinitionRevision, &b.ConsentRevision, &b.Assignment, &b.AssignmentDigest, &state, &consent, &attemptState, &attemptPrincipal, &attemptDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, workorders.Fail(404, "routine_grant_not_found")
	}
	if err != nil {
		return b, err
	}
	if state != "running" || attemptState != "running" || !consent || b.ExecutionPrincipalID == "" || attemptPrincipal != p.ID || attemptDigest != b.AssignmentDigest {
		return b, workorders.Fail(403, "active_run_grant_required")
	}
	if len(b.Assignment) > 131072 {
		return b, workorders.Fail(409, "assignment_invalid")
	}
	var assignment struct {
		AllowedActions []string `json:"allowed_actions"`
	}
	if json.Unmarshal(b.Assignment, &assignment) != nil {
		return b, workorders.Fail(409, "assignment_changed")
	}
	return b, nil
}

// applyTx is reached only after the live run grant and verified decision. Kept
// separate so transaction tests can exercise receipts without forging S08 proof.
func applyTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b Binding, in Request, policy string) (Receipt, error) {
	digest := routineguard.Digest(in)
	var prior Receipt
	var result []byte
	err := tx.QueryRow(ctx, `SELECT id::text,run_id::text,action_key,kind,request_digest,state,result FROM routine_actions WHERE run_id=$1 AND action_key=$2 FOR NO KEY UPDATE`, b.RunID, in.ActionKey).Scan(&prior.ID, &prior.RunID, &prior.ActionKey, &prior.Kind, &prior.RequestDigest, &prior.State, &result)
	if err == nil {
		if prior.RequestDigest != digest {
			return Receipt{}, workorders.Fail(409, "action_key_conflict")
		}
		if prior.State != "succeeded" || json.Unmarshal(result, &prior) != nil {
			return Receipt{}, workorders.Fail(409, "action_unconfirmed")
		}
		return prior, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, err
	}
	if err := requireTarget(ctx, tx, b, in); err != nil {
		return Receipt{}, err
	}
	var allowed struct {
		AllowedActions []string `json:"allowed_actions"`
		Sources        []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	if json.Unmarshal(b.Assignment, &allowed) != nil || !slices.Contains(allowed.AllowedActions, in.Kind) {
		return Receipt{}, authz.ErrForbidden
	}
	if in.Kind == "knowledge.write" {
		for _, source := range allowed.Sources {
			if source.ID == in.TargetID {
				return Receipt{}, workorders.Fail(403, "assignment_source_is_read_only")
			}
		}
	}
	var id string
	var revision int64
	var nodeEvents []nodes.Event
	var changes []events.Change
	if in.Kind == "knowledge.write" {
		id, revision, changes, err = writeKnowledge(ctx, tx, p, in)
	} else {
		id, revision, nodeEvents, err = writeWork(ctx, tx, p, in)
	}
	if err != nil {
		return Receipt{}, err
	}
	out := Receipt{RunID: b.RunID, ActionKey: in.ActionKey, Kind: in.Kind, RequestDigest: digest, State: "succeeded", NodeID: id, Revision: revision}
	if err := tx.QueryRow(ctx, `INSERT INTO routine_actions(tenant_id,run_id,attempt_id,action_key,kind,request_digest,target_node_id,target_revision,policy_digest,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'succeeded') RETURNING id::text`, p.TenantID, b.RunID, in.AttemptID, in.ActionKey, in.Kind, digest, in.TargetID, in.ExpectedRevision, policy).Scan(&out.ID); err != nil {
		return Receipt{}, err
	}
	raw, _ := json.Marshal(out)
	if _, err := tx.Exec(ctx, `UPDATE routine_actions SET result=$2 WHERE id=$1`, out.ID, raw); err != nil {
		return Receipt{}, err
	}
	meta, _ := json.Marshal(map[string]string{"routine_run_id": b.RunID, "routine_action_id": out.ID, "on_behalf_of": b.OwnerID})
	// No further lock acquisition after the event counter.
	for _, e := range nodeEvents {
		e.Metadata = meta
		if err := (nodes.SQLWriter{}).WriteEvent(ctx, tx, e); err != nil {
			return Receipt{}, err
		}
	}
	for _, c := range changes {
		c.Metadata = meta
		if _, err := events.Append(ctx, tx, p, c); err != nil {
			return Receipt{}, err
		}
	}
	return out, nil
}
func requireTarget(ctx context.Context, tx pgx.Tx, b Binding, in Request) error {
	var project string
	var revision time.Time
	var inScope bool
	err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id,n.id)::text,n.updated_at,
 EXISTS(WITH RECURSIVE ancestors(id,parent_id,depth) AS (SELECT id,parent_id,0 FROM nodes WHERE id=n.id UNION ALL SELECT p.id,p.parent_id,a.depth+1 FROM nodes p JOIN ancestors a ON p.id=a.parent_id WHERE a.depth<128 AND p.deleted_at IS NULL) SELECT 1 FROM ancestors WHERE id=$2)
 FROM nodes n WHERE n.id=$1 AND n.deleted_at IS NULL FOR NO KEY UPDATE`, in.TargetID, b.ParentID).Scan(&project, &revision, &inScope)
	if errors.Is(err, pgx.ErrNoRows) {
		return workorders.Fail(404, "action_target_not_found")
	}
	if err != nil {
		return err
	}
	if project != in.ProjectID {
		return workorders.Fail(409, "target_project_changed")
	}
	if revision.UnixMicro() != in.ExpectedRevision {
		return workorders.Fail(409, "target_revision_changed")
	}
	if in.Kind != "knowledge.write" && !inScope {
		return workorders.Fail(403, "target_outside_run_scope")
	}
	return nil
}
