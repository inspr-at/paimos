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
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/tenant"
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
	Instruction               string `json:"instruction"`
	RepositoryMutationAllowed *bool  `json:"repository_mutation_allowed"`
	RequestDigest             string `json:"request_digest"`
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
