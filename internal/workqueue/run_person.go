// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var ErrActingPersonBinding = errors.New("acting person binding refused")

// PersonAdmissionError is a stable, value-free refusal. It carries no caller
// supplied identity or content. Callers roll back their resource savepoint
// before recording a refusal, and flush successful binding events last.
type PersonAdmissionError struct{ Code string }

func (e *PersonAdmissionError) Error() string     { return e.Code }
func (e *PersonAdmissionError) ErrorCode() string { return e.Code }
func (e *PersonAdmissionError) HTTPStatus() int {
	if e.Code == "permission_revoked" {
		return 403
	}
	return 409
}
func (e *PersonAdmissionError) Unwrap() error { return authz.ErrForbidden }

// PersonAdmissionTx rolls back refused resource work but commits a bounded
// audit receipt. Returning the error as a result lets HTTP writers emit it
// after commit, just like an obsolete reservation's committed release.
func PersonAdmissionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, source string, fn func(pgx.Tx) (any, error)) (any, error) {
	step, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer step.Rollback(ctx)
	out, err := fn(step)
	var refusal *PersonAdmissionError
	if !errors.As(err, &refusal) {
		if err != nil {
			return nil, err
		}
		return out, step.Commit(ctx)
	}
	if err := step.Rollback(ctx); err != nil {
		return nil, err
	}
	if _, err := events.Append(ctx, tx, p, events.Change{Type: "run.admission_refused", After: map[string]string{"reason": refusal.Code, "source_rule": source}}); err != nil {
		return nil, err
	}
	return refusal, nil
}

// ResolveRunPersonTx accepts only explicit server provenance. An API key's
// issuer, the pairing approver and model preferences are never candidates.
func ResolveRunPersonTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, rule, sourceID string) (string, error) {
	var person *string
	var err error
	switch rule {
	case "personal":
		if p.Kind == tenant.Person {
			err = tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active'`, p.TenantID, p.ID).Scan(&person)
		}
		if p.Kind == tenant.Agent && p.KeyPersonID != "" {
			value := p.KeyPersonID
			person = &value
		}
	case "routine":
		err = tx.QueryRow(ctx, `SELECT d.owner_principal_id::text FROM recurrences r JOIN recurrence_definitions d ON d.tenant_id=r.tenant_id AND d.recurrence_id=r.id WHERE r.tenant_id=$1 AND r.id=$2`, p.TenantID, sourceID).Scan(&person)
	case "lead":
		err = tx.QueryRow(ctx, `SELECT owner_principal_id::text FROM project_leads WHERE tenant_id=$1 AND project_id=$2`, p.TenantID, sourceID).Scan(&person)
	case "review", "fix", "child", "recovery", "vendor_handoff":
		err = tx.QueryRow(ctx, `SELECT acting_person_id::text FROM agent_runs WHERE tenant_id=$1 AND id=$2`, p.TenantID, sourceID).Scan(&person)
	case "native_intent":
		err = tx.QueryRow(ctx, `SELECT owner_person_id::text FROM native_session_intents WHERE tenant_id=$1 AND id=$2`, p.TenantID, sourceID).Scan(&person)
	default:
		return "", &PersonAdmissionError{Code: "run_person_unresolved"}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if person == nil {
		return "", &PersonAdmissionError{Code: "run_person_unresolved"}
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active' AND linked_to IS NULL)`, p.TenantID, *person).Scan(&active); err != nil {
		return "", err
	}
	if !active {
		return "", &PersonAdmissionError{Code: "run_person_unresolved"}
	}
	return *person, nil
}

// SetActingPersonTx is the S1-01 single write-once seam, shared below the run,
// harness and recurrence packages to avoid dependency cycles. The caller owns
// the tenant/tree access fence, authorization and resource locks. It appends
// the returned event only after its final resource write. Replays emit none.
func SetActingPersonTx(ctx context.Context, tx pgx.Tx, tenantID, runID, personID, rule string) (*events.Change, error) {
	var current *string
	var order string
	if err := tx.QueryRow(ctx, `SELECT acting_person_id::text,work_order_id::text FROM agent_runs WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`, tenantID, runID).Scan(&current, &order); err != nil {
		return nil, err
	}
	if current != nil {
		if *current != personID {
			return nil, ErrActingPersonBinding
		}
		return nil, nil
	}
	result, err := tx.Exec(ctx, `UPDATE agent_runs SET acting_person_id=$3 WHERE tenant_id=$1 AND id=$2 AND acting_person_id IS NULL AND EXISTS (SELECT 1 FROM principals p WHERE p.tenant_id=$1 AND p.id=$3 AND p.kind='person' AND p.status='active' AND p.linked_to IS NULL)`, tenantID, runID, personID)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, ErrActingPersonBinding
	}
	return &events.Change{NodeID: &order, Type: "run.person_bound", After: map[string]string{"run_id": runID, "person_id": personID, "source_rule": rule}}, nil
}

// RequireRunPersonTx adds S1-02's live person ceiling to S1-16's computer
// start check in the final claim transaction. Null is the rollout's explicit
// legacy case; never repair it from requester, preferences or machine owner.
func RequireRunPersonTx(ctx context.Context, tx pgx.Tx, daemon tenant.Principal, runID string) error {
	var person, project *string
	if err := tx.QueryRow(ctx, `SELECT r.acting_person_id::text,n.project_id::text FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id WHERE r.tenant_id=$1 AND r.id=$2`, daemon.TenantID, runID).Scan(&person, &project); err != nil {
		return err
	}
	if person == nil {
		return nil
	}
	if daemon.KeyPersonID != "" && daemon.KeyPersonID != *person {
		return &PersonAdmissionError{Code: "permission_revoked"}
	}
	var canonical bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active' AND linked_to IS NULL)`, daemon.TenantID, *person).Scan(&canonical); err != nil {
		return err
	}
	if !canonical {
		return &PersonAdmissionError{Code: "permission_revoked"}
	}
	projectID := ""
	if project != nil {
		projectID = *project
	}
	actor := tenant.Principal{ID: *person, TenantID: daemon.TenantID, Kind: tenant.Person}
	if err := authz.RequireTx(ctx, tx, actor, "nodes.read", authz.Scope{ProjectID: projectID}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return &PersonAdmissionError{Code: "permission_revoked"}
		}
		return err
	}
	if err := authz.RequireRunStartOnComputerTx(ctx, tx, daemon, runID); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return &PersonAdmissionError{Code: "permission_revoked"}
		}
		return err
	}
	return nil
}
