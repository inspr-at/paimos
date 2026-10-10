// SPDX-License-Identifier: AGPL-3.0-only

// Package routinebudget owns persisted, run-wide resource holds. Its internal
// transaction API grants no process, provider, review or release authority.
package routinebudget

import (
	"encoding/hex"
	"math"
	"strings"

	"github.com/inspr-at/paimos/internal/workorders"
)

// Amount uses actual tokens, paid micro-USD and subscription recovery time.
// Subscription list-price comparisons never enter PaidMicroUSD.
type Amount struct {
	Tokens       int64 `json:"tokens"`
	PaidMicroUSD int64 `json:"paid_microusd"`
	RecoveryMS   int64 `json:"recovery_ms"`
}

func (a Amount) valid() bool { return a.Tokens >= 0 && a.PaidMicroUSD >= 0 && a.RecoveryMS >= 0 }
func (a Amount) add(b Amount) (Amount, error) {
	if !a.valid() || !b.valid() || a.Tokens > math.MaxInt64-b.Tokens || a.PaidMicroUSD > math.MaxInt64-b.PaidMicroUSD || a.RecoveryMS > math.MaxInt64-b.RecoveryMS {
		return Amount{}, workorders.Fail(409, "budget_arithmetic_overflow")
	}
	return Amount{a.Tokens + b.Tokens, a.PaidMicroUSD + b.PaidMicroUSD, a.RecoveryMS + b.RecoveryMS}, nil
}
func (a Amount) sub(b Amount) (Amount, error) {
	if !a.valid() || !b.valid() || a.Tokens < b.Tokens || a.PaidMicroUSD < b.PaidMicroUSD || a.RecoveryMS < b.RecoveryMS {
		return Amount{}, workorders.Fail(409, "budget_accounting_conflict")
	}
	return Amount{a.Tokens - b.Tokens, a.PaidMicroUSD - b.PaidMicroUSD, a.RecoveryMS - b.RecoveryMS}, nil
}
func within(a, b Amount) bool {
	return a.Tokens <= b.Tokens && a.PaidMicroUSD <= b.PaidMicroUSD && a.RecoveryMS <= b.RecoveryMS
}

type Balance struct {
	RunID        string `json:"run_id"`
	Mode         string `json:"mode"`
	TokenCeiling *int64 `json:"token_ceiling,omitempty"`
	MoneyCeiling *int64 `json:"money_ceiling_microusd,omitempty"`
	RecoveryMS   int64  `json:"recovery_ceiling_ms"`
	Settled      Amount `json:"settled"`
	Held         Amount `json:"held"`
}

func (b Balance) reserve(a Amount) (Balance, error) {
	current, err := b.Settled.add(b.Held)
	if err != nil {
		return b, err
	}
	if b.TokenCeiling != nil && current.Tokens >= *b.TokenCeiling {
		return b, workorders.Fail(409, "token_budget_exhausted")
	}
	if b.MoneyCeiling != nil && current.PaidMicroUSD >= *b.MoneyCeiling {
		return b, workorders.Fail(409, "money_budget_exhausted")
	}
	next, err := b.Held.add(a)
	if err != nil {
		return b, err
	}
	total, err := b.Settled.add(next)
	if err != nil {
		return b, err
	}
	if b.TokenCeiling != nil && total.Tokens > *b.TokenCeiling {
		return b, workorders.Fail(409, "token_budget_exhausted")
	}
	if b.MoneyCeiling != nil && total.PaidMicroUSD > *b.MoneyCeiling {
		return b, workorders.Fail(409, "money_budget_exhausted")
	}
	if total.RecoveryMS > b.RecoveryMS {
		return b, workorders.Fail(409, "recovery_budget_exhausted")
	}
	b.Held = next
	return b, nil
}

type Reservation struct {
	RunID, AttemptID, ActionID, GrantKey string
	AgentRunID, AccountID                string
	Maximum                              Amount
}

type Grant struct {
	ID          string  `json:"id"`
	RunID       string  `json:"run_id"`
	AttemptID   string  `json:"attempt_id"`
	ActionID    string  `json:"action_id"`
	GrantKey    string  `json:"grant_key"`
	ParentID    *string `json:"parent_id,omitempty"`
	AgentRunID  string  `json:"agent_run_id"`
	AccountID   string  `json:"account_id"`
	Maximum     Amount  `json:"maximum"`
	State       string  `json:"state"`
	Usage       Amount  `json:"usage"`
	BillingMode string  `json:"billing_mode"`
}

// Settlement is supplied only by the qualified adapter/reconciler, never a
// routine-controlled assertion. Root grants additionally require process exit.
// ProvenUnused means a persisted intent demonstrably never reached the provider
// or process; a lost response or cancellation request is insufficient.
type Settlement struct {
	EventKey       string `json:"event_key"`
	Usage          Amount `json:"usage"`
	UsageComplete  bool   `json:"usage_complete"`
	TokensKnown    bool   `json:"tokens_known"`
	PaidCostKnown  bool   `json:"paid_cost_known"`
	ExitConfirmed  bool   `json:"exit_confirmed"`
	ProvenUnused   bool   `json:"proven_unused"`
	EvidenceDigest string `json:"evidence_digest"`
}

func key(s string) bool { return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s }
func pin(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (in Reservation) valid() bool {
	return workorders.UUID(in.RunID) && workorders.UUID(in.AttemptID) && workorders.UUID(in.ActionID) && workorders.UUID(in.AgentRunID) && workorders.UUID(in.AccountID) && key(in.GrantKey) && in.Maximum.valid() && in.Maximum.RecoveryMS > 0 && in.Maximum.RecoveryMS <= 36_000_000_000
}
