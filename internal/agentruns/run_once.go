// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// CreateInput adds person-requested choices to the existing authoritative writer.
// RequestDigest is supplied only by a composite server entry, never HTTP JSON.
type CreateInput struct {
	Agent                     string  `json:"agent_principal_id"`
	Profile                   string  `json:"model_profile_id"`
	Account                   *string `json:"requested_account_id"`
	Retry                     *string `json:"retry_of_run_id"`
	CapacityOverride          string  `json:"capacity_override"`
	RequestID                 *string `json:"request_id"`
	Instruction               *string `json:"instruction"`
	RepositoryMutationAllowed *bool   `json:"repository_mutation_allowed"`
	RequestDigest             string  `json:"-"`
	EffectiveInstruction      string  `json:"-"`
}

type runOnceBinding struct {
	Instruction               string         `json:"instruction"`
	RepositoryMutationAllowed *bool          `json:"repository_mutation_allowed"`
	RequestDigest             string         `json:"request_digest"`
	Ticket                    *runOnceTicket `json:"ticket,omitempty"`
}

// The launch revision is deliberately conservative: any intervening ticket
// edit preserves its status, including a person changing it away and back.
type runOnceTicket struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

func bindRunOnceTicketTx(ctx context.Context, tx pgx.Tx, order string) (*runOnceTicket, error) {
	var ticket runOnceTicket
	err := tx.QueryRow(ctx, `SELECT n.id::text,n.state,n.updated_at FROM nodes o
 JOIN nodes n ON n.id=o.parent_id AND n.tenant_id=o.tenant_id
 JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id
 WHERE o.id=$1 AND n.deleted_at IS NULL AND k.slug IN ('work','ticket','task')`, order).Scan(&ticket.ID, &ticket.State, &ticket.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // Standalone work orders have no ticket to close.
	}
	return &ticket, err
}

// settleRunOnceTx runs under the existing tenant/tree/access fence and the
// target -> order -> run locks. It never appends an event before resource work
// finishes. The run lock and durable receipt make both telemetry and cancel
// replays idempotent without a new table or a second transaction.
func settleRunOnceTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run, report Telemetry, pending *[]events.Change) error {
	if !v.RunOnce || !terminal(v.Status) {
		return nil
	}
	var trace struct {
		RunOnce *runOnceBinding `json:"run_once"`
	}
	if err := json.Unmarshal(v.Trace, &trace); err != nil {
		return err
	}
	if trace.RunOnce == nil {
		return errInvalidRunOnceBinding
	}
	current, err := bindRunOnceTicketTx(ctx, tx, v.OrderID)
	if err != nil {
		return err
	}
	ticket := trace.RunOnce.Ticket
	if ticket == nil {
		ticket = current // Historical runs can record results, never auto-close.
	}
	if ticket == nil {
		return nil
	}
	var recorded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE node_id=$1 AND type='run_once.settled' AND after->>'run_id'=$2)`, ticket.ID, v.ID).Scan(&recorded); err != nil || recorded {
		return err
	}
	target, err := workorders.EntryTicketTx(ctx, tx, ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // A person-deleted ticket is never resurrected.
	}
	if err != nil {
		return err
	}
	reason := v.Status
	if report.ErrorCode != "" {
		reason = report.ErrorCode
	}
	var entry *workorders.EntryRecord
	closed := false
	if v.Status == "completed" {
		reason, entry, err = runOnceCloseCheckTx(ctx, tx, p, v, report, trace.RunOnce.Ticket, current, target)
		if err != nil {
			return err
		}
		if reason == "" {
			var before, after json.RawMessage
			if err := tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, ticket.ID).Scan(&before); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `UPDATE nodes SET state='done',status_autopilot='{}',updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING to_jsonb(nodes)`, ticket.ID).Scan(&after); err != nil {
				return err
			}
			metadata, err := json.Marshal(map[string]any{"run_id": v.ID, "entry_checks": entry})
			if err != nil {
				return err
			}
			*pending = append(*pending, events.Change{NodeID: &ticket.ID, Type: "node.updated", Before: before, After: after, Metadata: metadata})
			closed = true
		}
	}
	resultURL := "/agents?run=" + v.ID
	body := fmt.Sprintf("Run once [result](%s): %s.", resultURL, v.Status)
	if v.OutcomeDetail != nil {
		body += " Outcome: " + *v.OutcomeDetail + "."
	}
	if closed {
		body += "\n\nTicket moved to Done."
	} else {
		body += "\n\nTicket status unchanged. Reason: " + reason + "."
	}
	*pending = append(*pending,
		events.Change{NodeID: &ticket.ID, Type: "run_once.settled", After: map[string]any{"run_id": v.ID, "work_order_id": v.OrderID, "status": v.Status, "outcome_detail": v.OutcomeDetail, "result_url": resultURL, "ticket_closed": closed, "reason": reason}},
		events.Change{NodeID: &ticket.ID, Type: "comment.created", After: map[string]any{"body_markdown": body, "run_id": v.ID}},
	)
	return nil
}

func runOnceCloseCheckTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run, report Telemetry, launch, current *runOnceTicket, target workorders.EntryTicket) (string, *workorders.EntryRecord, error) {
	if report.ProcessState == "unconfirmed" {
		return "process_exit_unconfirmed", nil, nil
	}
	if launch == nil {
		return "launch_revision_unavailable", nil, nil
	}
	if current == nil || current.ID != launch.ID {
		return "ticket_binding_changed", nil, nil
	}
	if target.State != launch.State || !target.Revision.Equal(launch.UpdatedAt) {
		return "ticket_changed", nil, nil
	}
	var kindID string
	var leaf bool
	if err := tx.QueryRow(ctx, `SELECT kind_id::text,CASE WHEN $2='work' THEN aeon_work_leaf(id) ELSE NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds k ON k.id=c.kind_id AND k.tenant_id=c.tenant_id WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND k.slug IN ('work','ticket','task')) END FROM nodes n WHERE id=$1`, target.ID, target.Kind).Scan(&kindID, &leaf); err != nil {
		return "", nil, err
	}
	if !leaf {
		return "not_a_leaf", nil, nil
	}
	category, err := workorders.EntryStatusTx(ctx, tx, kindID, target.State)
	if err != nil {
		return "", nil, err
	}
	if workqueue.Terminal(category) {
		return "ticket_already_terminal", nil, nil
	}
	done, err := workorders.EntryStatusTx(ctx, tx, kindID, "done")
	if err != nil {
		return "", nil, err
	}
	if done != "done" {
		return "done_status_unavailable", nil, nil
	}
	if v.ActingPersonID == nil {
		return "permission_revoked", nil, nil
	}
	actor := tenant.Principal{TenantID: p.TenantID, ID: *v.ActingPersonID, Kind: tenant.Person}
	scope := authz.Scope{}
	if target.ProjectID != nil {
		scope.ProjectID = *target.ProjectID
	}
	if err := authz.RequireTx(ctx, tx, actor, "nodes.write", scope); errors.Is(err, authz.ErrForbidden) {
		return "permission_revoked", nil, nil
	} else if err != nil {
		return "", nil, err
	}
	fields, err := json.Marshal(target.Fields)
	if err != nil {
		return "", nil, err
	}
	if len(ticketbenefits.PillIssues(fields)) > 0 {
		return "invalid_completion_copy", nil, nil
	}
	entry, err := workorders.RequireEntryTx(ctx, tx, p, target, "done", nil)
	var refused *workorders.EntryFailure
	if errors.As(err, &refused) {
		return "entry_checks_failed", nil, nil
	}
	return "", entry, err
}

func (in CreateInput) mutationAllowed() bool {
	return in.RepositoryMutationAllowed != nil && *in.RepositoryMutationAllowed
}

func validateRunOnce(p tenant.Principal, in CreateInput) error {
	if in.RequestID == nil && in.Instruction == nil && in.RepositoryMutationAllowed == nil {
		return nil
	}
	if p.Kind != tenant.Person {
		return workorders.Fail(403, "only a person can request a one-off run")
	}
	if in.RequestID == nil || !workorders.UUID(*in.RequestID) {
		return workorders.Fail(400, "one-off request_id required")
	}
	if !workorders.UUID(in.Agent) || !workorders.UUID(in.Profile) || in.Account != nil && !workorders.UUID(*in.Account) {
		return workorders.Fail(400, "agent, model profile and valid account required")
	}
	if in.Instruction != nil && (!utf8.ValidString(*in.Instruction) || len(*in.Instruction) > 8000 || utf8.RuneCountInString(*in.Instruction) > 2000) {
		return workorders.Fail(400, "one-off instruction exceeds 2000 characters")
	}
	if in.Retry != nil {
		return workorders.Fail(400, "one-off requests cannot change a retry lineage")
	}
	return nil
}

// RunRequestDigest binds retries to both the context and exact submitted options.
// The tenant-scoped request UUID derives the run UUID; its unique key remains
// the durable idempotency fence without exposing another tenant's UUIDs.
func RunRequestDigest(context string, input any) string {
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(append(append([]byte(context), 0), raw...))
	return hex.EncodeToString(sum[:])
}

func requestedRunID(tenantID string, requestID *string) *string {
	if requestID == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(strings.ToLower(tenantID) + "\x00" + strings.ToLower(*requestID)))
	// UUIDv8: a server-defined, tenant-scoped name, never the caller's global id.
	sum[6] = (sum[6] & 0x0f) | 0x80
	sum[8] = (sum[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	return &id
}

// FindRequestedRunTx requires the caller's tenant/tree fence and current context
// authorization. It never reuses a run from another person or different input.
func FindRequestedRunTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in CreateInput) (*Run, error) {
	if err := validateRunOnce(p, in); err != nil {
		return nil, err
	}
	if in.RequestID == nil {
		return nil, nil
	}
	v, err := load(ctx, tx, *requestedRunID(p.TenantID, in.RequestID), false)
	if errors.Is(err, pgx.ErrNoRows) {
		// Preserve retries of pre-expansion one-off runs in this tenant. RLS
		// prevents a legacy id belonging to another tenant from being observed.
		v, err = load(ctx, tx, *in.RequestID, false)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	person, err := workqueue.ResolveRunPersonTx(ctx, tx, p, "personal", "")
	if err != nil {
		return nil, err
	}
	var binding struct {
		RunOnce *runOnceBinding `json:"run_once"`
	}
	if json.Unmarshal(v.Trace, &binding) != nil || binding.RunOnce == nil || binding.RunOnce.RequestDigest != in.RequestDigest ||
		v.ActingPersonID == nil || *v.ActingPersonID != person {
		return nil, workorders.Fail(409, "one-off request_id is already bound to another context, person or options")
	}
	return &v, nil
}

// CreatePersonRunTx composes the same writer used by POST work-order/runs.
// The caller holds tenant/tree/pairing fences and appends these deferred events
// only after its leaf and work-order mutations have acquired every resource lock.
func CreatePersonRunTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, order string, in CreateInput) (Run, []func() error, error) {
	if p.Kind != tenant.Person {
		return Run{}, nil, workorders.Fail(403, "person required")
	}
	var pending []func() error
	result, err := createRunTx(ctx, tx, p, order, in, "", &pending)
	if err != nil {
		return Run{}, nil, err
	}
	switch v := result.(type) {
	case Run:
		return v, pending, nil
	case *Run:
		return *v, pending, nil
	case error:
		return Run{}, nil, v
	default:
		return Run{}, nil, workorders.Fail(409, "ordinary run admission refused this start")
	}
}
