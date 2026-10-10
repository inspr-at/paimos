// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RunReceipt identifies persisted intent, never admission or a process launch.
type RunReceipt struct {
	ID               string  `json:"id"`
	State            string  `json:"state"`
	WorkOrderID      *string `json:"work_order_id,omitempty"`
	AgentRunID       *string `json:"agent_run_id,omitempty"`
	AssignmentDigest string  `json:"assignment_digest"`
	PolicyDigest     string  `json:"policy_digest"`
}

func runReceipt(ctx context.Context, tx pgx.Tx, o *Occurrence) error {
	var run RunReceipt
	err := tx.QueryRow(ctx, `SELECT id::text,state,work_order_id::text,agent_run_id::text,assignment_digest,policy_digest
 FROM routine_runs WHERE recurrence_id=$1 AND occurrence_key=$2`, o.RecurrenceID, o.Key).Scan(
		&run.ID, &run.State, &run.WorkOrderID, &run.AgentRunID, &run.AssignmentDigest, &run.PolicyDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil {
		o.Run = &run
	}
	return err
}

// The scheduler's all-project visibility is not owner authority. This runs
// after the locked definition reload, under the caller's tenant/tree fence.
func authorizeRunOwner(ctx context.Context, tx pgx.Tx, actor tenant.Principal, r Recurrence) error {
	if r.Definition == nil || r.Definition.Assignment == nil {
		return nil
	}
	owner := tenant.Principal{ID: r.Definition.OwnerPrincipalID, TenantID: actor.TenantID, Kind: tenant.Person}
	if err := validateDefinition(ctx, tx, owner, r.Input); err != nil {
		return err
	}
	return authz.RequireTx(ctx, tx, owner, "nodes.write", authz.Scope{ProjectID: r.ProjectID})
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// persistRun is called before the occurrence receipt and all event appends.
// The deferred occurrence FK closes the atomic unit at commit. Queue IDs are
// only the inert holder's existing rows; they grant no routing or execution.
func persistRun(ctx context.Context, tx pgx.Tx, actor tenant.Principal, r Recurrence, o *Occurrence, source *EventContext, name, version string) error {
	if o.NodeID == nil || r.Definition == nil || r.Definition.Assignment == nil {
		return nil
	}
	d := r.Definition
	assignment, err := json.Marshal(d.Assignment)
	if err != nil {
		return err
	}
	if len(assignment) > 131072 {
		return workorders.Fail(400, "assignment exceeds its limits")
	}
	var consent bool
	var consentRevision int64
	var executionPrincipal *string
	if err := tx.QueryRow(ctx, `SELECT execute_consent,consent_revision,execution_principal_id::text
 FROM recurrence_definitions WHERE recurrence_id=$1`, r.ID).Scan(&consent, &consentRevision, &executionPrincipal); err != nil {
		return err
	}
	// Freeze inputs, not an invented policy verdict. Later executors must resolve
	// current guardrails, qualification and owner authority before admission.
	policy, err := json.Marshal(struct {
		Definition         *Definition `json:"definition"`
		Revision           int64       `json:"revision"`
		ProjectID          string      `json:"project_id"`
		ParentID           string      `json:"parent_id"`
		Consent            bool        `json:"execute_consent"`
		ConsentRevision    int64       `json:"consent_revision"`
		ExecutionPrincipal *string     `json:"execution_principal_id"`
	}{d, r.Revision, r.ProjectID, r.ParentID, consent, consentRevision, executionPrincipal})
	if err != nil {
		return err
	}
	sourceReceipt, err := json.Marshal(struct {
		Key     string        `json:"occurrence_key"`
		EventID *int64        `json:"source_event_id,omitempty"`
		Name    string        `json:"release_name,omitempty"`
		Version string        `json:"release_version,omitempty"`
		Context *EventContext `json:"context,omitempty"`
	}{o.Key, o.SourceEventID, boundedText(name, 256), boundedText(version, 256), source})
	if err != nil || len(sourceReceipt) > 8192 {
		return workorders.Fail(400, "source receipt exceeds its limits")
	}
	var run RunReceipt
	run.State, run.AssignmentDigest, run.PolicyDigest = "pending", digest(assignment), digest(policy)
	if r.QueueEach {
		if err := tx.QueryRow(ctx, `SELECT work_order_id::text,id::text FROM agent_runs WHERE queue_node_id=$1`, *o.NodeID).Scan(&run.WorkOrderID, &run.AgentRunID); err != nil {
			return err
		}
	}
	err = tx.QueryRow(ctx, `INSERT INTO routine_runs(tenant_id,recurrence_id,occurrence_key,definition_revision,
 scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment,assignment_digest,
 policy_snapshot,policy_digest,execute_consent,consent_revision,execution_principal_id,work_node_id,work_order_id,agent_run_id,source_event_id,source_receipt)
 VALUES($1,$2,$3,$4,$5,nullif($6,'')::uuid,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) RETURNING id::text`,
		actor.TenantID, r.ID, o.Key, r.Revision, d.Scope.Kind, d.Scope.ProjectID, d.OwnerPrincipalID, r.ProjectID, r.ParentID,
		assignment, run.AssignmentDigest, policy, run.PolicyDigest, consent, consentRevision, executionPrincipal,
		o.NodeID, run.WorkOrderID, run.AgentRunID, o.SourceEventID, sourceReceipt).Scan(&run.ID)
	if err != nil {
		return err
	}
	// The work creation is already confirmed in this transaction. Future outward
	// actions use their own run-scoped key and immutable request digest.
	request, err := json.Marshal(r.Input)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO routine_actions(tenant_id,run_id,action_key,kind,request_digest,target_node_id,target_revision,state,result)
 VALUES($1,$2,'occurrence','work.create',$3,$4,1,'succeeded',jsonb_build_object('node_id',$4::text))`, actor.TenantID, run.ID, digest(request), o.NodeID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,payload)
 VALUES($1,$2,'execute','routine.execute',jsonb_build_object('run_id',$2::text))`, actor.TenantID, run.ID); err != nil {
		return err
	}
	// PostgreSQL delivers transactional notifications only after COMMIT, and
	// discards them on rollback. The durable outbox also survives a missed wake.
	wake, err := json.Marshal(map[string]string{"tenant_id": actor.TenantID, "run_id": run.ID})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('aeon_routine_intents',$1)`, string(wake)); err != nil {
		return err
	}
	o.Run = &run
	return nil
}
