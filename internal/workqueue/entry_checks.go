// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Queue consumers share the lower-level status policy without making the
// automatic-status package depend on its own queue projection.
type EntryRule = workorders.EntryRule
type EntryDefinition = workorders.EntryDefinition
type EntryPerson = workorders.EntryPerson
type EntryResult = workorders.EntryResult
type EntryBlock = workorders.EntryBlock
type EntryRecord = workorders.EntryRecord
type EntryRefusal = workorders.EntryRefusal
type EntryChecks = workorders.EntryChecks
type EntryFailure = workorders.EntryFailure
type EntryTicket = workorders.EntryTicket

func ValidateEntryRules(checks map[string][]EntryRule) error {
	return workorders.ValidateEntryRules(checks)
}
func EntryDefinitionTx(ctx context.Context, tx pgx.Tx, project *string, inherit bool) (*EntryDefinition, error) {
	return workorders.EntryDefinitionTx(ctx, tx, project, inherit)
}
func EntryTicketTx(ctx context.Context, tx pgx.Tx, id string) (EntryTicket, error) {
	return workorders.EntryTicketTx(ctx, tx, id)
}
func EntryStatusTx(ctx context.Context, tx pgx.Tx, kind, state string) (string, error) {
	return workorders.EntryStatusTx(ctx, tx, kind, state)
}
func EntryOwnerTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, t EntryTicket, r EntryRule) (*EntryPerson, error) {
	return workorders.EntryOwnerTx(ctx, tx, p, t, r)
}
func EvaluateEntryTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, t EntryTicket, d *EntryDefinition, status string) (EntryBlock, error) {
	return workorders.EvaluateEntryTx(ctx, tx, p, t, d, status)
}
func RequireEntryTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, t EntryTicket, status string, reason *string) (*EntryRecord, error) {
	return workorders.RequireEntryTx(ctx, tx, p, t, status, reason)
}

// Eligibility and estimate advice retain the published queue shape; readiness
// gaps all come from the same Ready definition as status writes.
func DefinitionReadinessTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, t EntryTicket, legacy Readiness) (Readiness, error) {
	d, err := EntryDefinitionTx(ctx, tx, t.ProjectID, true)
	if err != nil {
		return legacy, err
	}
	b, err := EvaluateEntryTx(ctx, tx, p, t, d, "in_progress")
	if err != nil {
		return legacy, err
	}
	legacy.Missing = []string{}
	if !legacy.Queueable {
		legacy.Missing = append(legacy.Missing, "status")
	}
	for _, r := range b.Checks {
		if r.Applicable && !r.Passed {
			gap := r.ID
			switch gap {
			case "estimate_set":
				gap = "estimate"
			case "acceptance_present":
				gap = "criteria"
			case "blocker_named":
				gap = "blocker"
			}
			legacy.Missing = append(legacy.Missing, gap)
		}
	}
	legacy.Ready = legacy.Queueable && len(legacy.Missing) == 0
	return legacy, nil
}
