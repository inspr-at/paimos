// SPDX-License-Identifier: AGPL-3.0-only
package engineadmission

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RoutineFacts are independently observed server facts. The qualified reader
// uses bounded local/cache reads inside the write, never a shadow Decision or
// routine setting. Network refresh belongs outside the mutation transaction.
// The policy revision binds Arion's current freeze/CI admission policy.
type RoutineFacts struct {
	ProjectID      string
	PolicyRevision int64
	ObservedAt     time.Time
	MainGreen      *bool
	CIAllowed      *bool
	WorkPRs        *int
}
type RoutineFactsReader func(context.Context, pgx.Tx, string, string) (RoutineFacts, error)

// RequireRoutineTx is restricted to durable intent, and always evaluates the
// existing daily/dial/harness/account/host predicates afresh. Advisory mode and
// ScriptAllowed never grant execution. Missing current CI facts close starts.
func RequireRoutineTx(ctx context.Context, tx pgx.Tx, owner tenant.Principal, routine, run, harness string, estimate float64, reader RoutineFactsReader) error {
	var project string
	var bound bool
	if err := tx.QueryRow(ctx, `SELECT output_project_id::text,agent_run_id=$2::uuid AND owner_principal_id=$3::uuid AND execute_consent AND consent_revision=definition_revision FROM routine_runs WHERE id=$1`, routine, run, owner.ID).Scan(&project, &bound); err != nil {
		return err
	}
	if !bound {
		return workorders.Fail(409, "routine_intent_required")
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT CASE WHEN octet_length(snapshot::text)<=65536 THEN snapshot END FROM delivery_work_settings WHERE project_id=$1`, project).Scan(&raw)
	var policy struct {
		Freeze   bool  `json:"freeze"`
		Revision int64 `json:"revision"`
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && (raw == nil || json.Unmarshal(raw, &policy) != nil || policy.Revision < 0) {
		return workorders.Fail(409, "ci_policy_unreadable")
	}
	// Conservatively wait through a release freeze. A routine cannot create its
	// own release-set exception; ordinary delivery's off/shadow mode stays intact.
	if policy.Freeze {
		return workorders.Fail(409, "release_freeze")
	}
	if reader == nil {
		return workorders.Fail(409, "ci_admission_unavailable")
	}
	facts, err := reader(ctx, tx, owner.TenantID, project)
	// Sample after the local reader: an observation made inside this write is
	// fresh, rather than incorrectly classified as a future reading.
	var now time.Time
	if clockErr := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); clockErr != nil {
		return clockErr
	}
	if err != nil || facts.ProjectID != project || facts.PolicyRevision != policy.Revision || facts.ObservedAt.IsZero() || facts.ObservedAt.After(now) || now.Sub(facts.ObservedAt) > 30*time.Second || facts.MainGreen == nil || facts.CIAllowed == nil {
		return workorders.Fail(409, "ci_admission_unavailable")
	}
	if !*facts.MainGreen {
		return workorders.Fail(409, "main_red")
	}
	if !*facts.CIAllowed {
		return workorders.Fail(409, "ci_admission_wait")
	}
	if facts.WorkPRs == nil {
		return workorders.Fail(409, "wip_unreadable")
	}
	in := Request{Kind: "first_build", Harness: harness, Project: project, Estimate: estimate}
	if !in.valid() {
		return workorders.Fail(409, "admission_inputs_unreadable")
	}
	m := Module{}
	reason, _, err := m.evaluate(ctx, tx, owner, in, project, *facts.WorkPRs, nil, facts.ObservedAt, now, run)
	if err != nil {
		return err
	}
	if reason != "allowed" {
		return workorders.Fail(409, reason)
	}
	return nil
}
