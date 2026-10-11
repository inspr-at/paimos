// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"math/bits"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const RoutineProviderBudgetCapability = "routine_provider_budget_v1"

var (
	ErrBudgetEnforcementUnavailable = errors.New("budget_enforcement_unavailable")
	ErrRoutineBudgetBinding         = errors.New("routine_budget_binding_invalid")
	ErrRoutineCallReplay            = errors.New("routine_provider_call_replay")
	ErrRoutineUsageUnconfirmed      = errors.New("routine_provider_usage_unconfirmed")
	ErrRoutineBudgetOverflow        = errors.New("routine_budget_arithmetic_overflow")
)

// These wire projections mirror the persisted broker's JSON contract without
// importing its database and account packages into the local daemon.
type RoutineBudgetAmount struct {
	Tokens       int64 `json:"tokens"`
	PaidMicroUSD int64 `json:"paid_microusd"`
	RecoveryMS   int64 `json:"recovery_ms"`
}

type RoutineBudgetGrant struct {
	ID          string              `json:"id"`
	RunID       string              `json:"run_id"`
	AttemptID   string              `json:"attempt_id"`
	ActionID    string              `json:"action_id"`
	GrantKey    string              `json:"grant_key"`
	ParentID    *string             `json:"parent_id,omitempty"`
	AgentRunID  string              `json:"agent_run_id"`
	AccountID   string              `json:"account_id"`
	Maximum     RoutineBudgetAmount `json:"maximum"`
	State       string              `json:"state"`
	Usage       RoutineBudgetAmount `json:"usage"`
	BillingMode string              `json:"billing_mode"`
}

// RoutineBudgetSettlement carries qualified usage evidence; it grants no
// authority to settle a root hold without the broker's confirmed-exit checks.
type RoutineBudgetSettlement struct {
	EventKey       string              `json:"event_key"`
	Usage          RoutineBudgetAmount `json:"usage"`
	UsageComplete  bool                `json:"usage_complete"`
	TokensKnown    bool                `json:"tokens_known"`
	PaidCostKnown  bool                `json:"paid_cost_known"`
	ExitConfirmed  bool                `json:"exit_confirmed"`
	ProvenUnused   bool                `json:"proven_unused"`
	EvidenceDigest string              `json:"evidence_digest"`
}

// RoutineProviderPrice is an upper bound from the qualified adapter, not a
// model's estimate or subscription list price. Each category is disjoint;
// output includes reasoning and tool units cover ALL other billable fees.
type RoutineProviderPrice struct {
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	Currency             string `json:"currency"`
	SourceDigest         string `json:"source_digest"`
	InputPerMillion      int64  `json:"input_per_million"`
	OutputPerMillion     int64  `json:"output_per_million"`
	CacheReadPerMillion  int64  `json:"cache_read_per_million"`
	CacheWritePerMillion int64  `json:"cache_write_per_million"`
	ToolPerUnit          int64  `json:"tool_per_unit"`
}

// RoutineProviderBudget is issued by the authoritative runtime grant path.
// S11 owns issuance/claim authorization; this transport is never a credential.
// Remaining excludes every earlier settled or unresolved provider call.
type RoutineProviderBudget struct {
	Capability     string               `json:"capability"`
	RuntimeDigest  string               `json:"runtime_digest"`
	TenantID       string               `json:"tenant_id"`
	PrincipalID    string               `json:"principal_id"`
	Generation     string               `json:"generation"`
	ModelProfileID string               `json:"model_profile_id"`
	Mode           string               `json:"mode"`
	Grant          RoutineBudgetGrant   `json:"grant"`
	Remaining      RoutineBudgetAmount  `json:"remaining"`
	Price          RoutineProviderPrice `json:"price"`
	ExpiresAt      time.Time            `json:"expires_at"`
}

type RoutineBudgetQualification struct {
	Capability, RuntimeDigest, BillingMode string
	Price                                  RoutineProviderPrice
	Tokens, PaidMoney                      bool
}

// RoutineBudgetAdapter is a trusted implementation capability, never an
// advertised vendor flag. Qualification performs no vendor/network work. A
// qualified StartBudgeted MUST send every billable request (including retries,
// compaction, cache, tools and parallel requests) through budget.Call and enforce
// its immutable per-call maximum at the provider boundary. Cancellation and
// observeBudget cannot qualify it. No current native adapter implements this.
type RoutineBudgetAdapter interface {
	RoutineBudgetQualification(Profile) (RoutineBudgetQualification, error)
	StartBudgeted(context.Context, StartRequest, *RoutineCallBudget, func(AdapterEvent)) (Process, error)
}

type RoutineCallReservation struct {
	Grant RoutineBudgetGrant
	// True only for a persisted, new dispatch intent. A replay or unresolved
	// intent MUST return false, even if the held subgrant was found successfully.
	DispatchAllowed bool
}

// RoutineBudgetBroker is the authenticated S09/S11 integration seam. Each write
// rechecks live run/owner authority in the final fenced transaction. Reserve
// persists the call intent before returning; Settle uses S09 complete-evidence
// semantics. No provider/network work runs inside either DB transaction.
// Unknown transport outcomes retain holds. There is deliberately no fallback
// to ordinary run telemetry and no Remote implementation until S11 wires it.
type RoutineBudgetBroker interface {
	ReserveRoutineCall(context.Context, RoutineProviderBudget, string, RoutineBudgetAmount) (RoutineCallReservation, error)
	SettleRoutineCall(context.Context, string, string, RoutineBudgetSettlement) (RoutineBudgetGrant, error)
}

type RoutineCallMaximum struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ToolUnits int64
	RecoveryMS                                                              int64
}

type RoutineCallUsage struct {
	Counts                               RoutineCallMaximum
	PaidMicroUSD                         int64 // actual provider charge, never calculated list price
	Complete, TokensKnown, PaidCostKnown bool
	EvidenceDigest                       string
}

type RoutineCallPermit struct {
	GrantID, CallKey string
	Maximum          RoutineCallMaximum
	Price            RoutineProviderPrice
}

type routineCall struct {
	maximum RoutineBudgetAmount
	counts  RoutineCallMaximum
	grant   RoutineBudgetGrant
	final   *RoutineBudgetSettlement
}

// RoutineCallBudget is process-local enforcement backed by persisted subgrants.
// It is constructed once per fresh runtime grant, never restored from telemetry.
// The runtime journal and broker refuse re-dispatch of unresolved call intents.
type RoutineCallBudget struct {
	mu        sync.Mutex
	settleMu  contextMutex
	binding   RoutineProviderBudget
	broker    RoutineBudgetBroker
	now       func() time.Time
	allocated RoutineBudgetAmount // settled actual + every outstanding maximum
	calls     map[string]*routineCall
	stopped   error
}

func budgetKey(s string) bool {
	return s != "" && len(s) <= 128 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}
func budgetDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func validBudgetAmount(a RoutineBudgetAmount) bool {
	return a.Tokens >= 0 && a.PaidMicroUSD >= 0 && a.RecoveryMS >= 0
}
func amountWithin(a, b RoutineBudgetAmount) bool {
	return validBudgetAmount(a) && validBudgetAmount(b) && a.Tokens <= b.Tokens && a.PaidMicroUSD <= b.PaidMicroUSD && a.RecoveryMS <= b.RecoveryMS
}

func newRoutineCallBudget(r StartRequest, adapter Adapter, broker RoutineBudgetBroker, now func() time.Time) (*RoutineCallBudget, error) {
	if r.Run.RoutineBudget == nil {
		return nil, nil
	}
	a, ok := adapter.(RoutineBudgetAdapter)
	if !ok || broker == nil {
		return nil, ErrBudgetEnforcementUnavailable
	}
	g := *r.Run.RoutineBudget // freeze the server transport before async work
	q, err := a.RoutineBudgetQualification(r.Profile)
	if err != nil || q.Capability != RoutineProviderBudgetCapability || !q.Tokens ||
		(g.Mode == "money" || g.Mode == "both") && (!q.PaidMoney || q.BillingMode != "api") {
		return nil, ErrBudgetEnforcementUnavailable
	}
	if now == nil {
		now = time.Now
	}
	root := g.Grant
	if g.Capability != q.Capability || !budgetDigest(g.RuntimeDigest) || g.RuntimeDigest != q.RuntimeDigest ||
		g.Price != q.Price || !budgetDigest(g.Price.SourceDigest) || !budgetKey(g.Price.Provider) || g.Price.Model != r.Profile.Model || !budgetKey(g.Price.Model) || g.Price.Currency != "USD" ||
		!budgetKey(g.TenantID) || !budgetKey(g.PrincipalID) || !budgetKey(g.ModelProfileID) || !budgetKey(r.Run.ID) || g.TenantID != r.TenantID || g.PrincipalID != r.PrincipalID || g.Generation != r.Generation || !budgetKey(g.Generation) || g.ModelProfileID != r.Profile.ID ||
		!budgetKey(root.ID) || !budgetKey(root.RunID) || !budgetKey(root.AttemptID) || !budgetKey(root.ActionID) || !budgetKey(root.GrantKey) ||
		root.AgentRunID != r.Run.ID || root.AccountID != r.Run.requestedAccount() || !budgetKey(root.AccountID) || root.ParentID != nil || root.State != "held" || root.BillingMode != q.BillingMode ||
		!amountWithin(g.Remaining, root.Maximum) || root.Maximum.RecoveryMS <= 0 || !now().Before(g.ExpiresAt) {
		return nil, ErrRoutineBudgetBinding
	}
	switch g.Mode {
	case "off", "tokens", "money", "both":
	default:
		return nil, ErrRoutineBudgetBinding
	}
	if q.BillingMode != "subscription" && q.BillingMode != "api" {
		return nil, ErrBudgetEnforcementUnavailable
	}
	p := g.Price
	if p.InputPerMillion < 0 || p.OutputPerMillion < 0 || p.CacheReadPerMillion < 0 || p.CacheWritePerMillion < 0 || p.ToolPerUnit < 0 {
		return nil, ErrRoutineBudgetBinding
	}
	if q.BillingMode == "subscription" && (p.InputPerMillion != 0 || p.OutputPerMillion != 0 || p.CacheReadPerMillion != 0 || p.CacheWritePerMillion != 0 || p.ToolPerUnit != 0 || root.Maximum.PaidMicroUSD != 0) {
		return nil, ErrRoutineBudgetBinding
	}
	b := &RoutineCallBudget{binding: g, broker: broker, now: now, calls: map[string]*routineCall{}}
	return b, b.RequireRemaining()
}

// roundedFee computes ceil(units*rate/divisor), rejecting overflow before work.
func roundedFee(units, rate int64, divisor uint64) (int64, error) {
	if units < 0 || rate < 0 {
		return 0, ErrRoutineBudgetBinding
	}
	hi, lo := bits.Mul64(uint64(units), uint64(rate))
	if hi >= divisor {
		return 0, ErrRoutineBudgetOverflow
	}
	q, rem := bits.Div64(hi, lo, divisor)
	if q > math.MaxInt64 || rem != 0 && q == math.MaxInt64 {
		return 0, ErrRoutineBudgetOverflow
	}
	if rem != 0 {
		q++
	}
	return int64(q), nil
}

func (m RoutineCallMaximum) amount(p RoutineProviderPrice) (RoutineBudgetAmount, error) {
	out := RoutineBudgetAmount{RecoveryMS: m.RecoveryMS}
	if m.RecoveryMS < 0 || m.RecoveryMS > 36_000_000_000 {
		return out, ErrRoutineBudgetBinding
	}
	units := []int64{m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.ToolUnits}
	rates := []int64{p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion, p.ToolPerUnit}
	for i, n := range units {
		if n < 0 || i < 4 && out.Tokens > math.MaxInt64-n {
			return out, ErrRoutineBudgetOverflow
		}
		if i < 4 {
			out.Tokens += n
		}
		divisor := uint64(1_000_000)
		if i == 4 {
			divisor = 1
		}
		fee, err := roundedFee(n, rates[i], divisor)
		if err != nil {
			return out, err
		}
		if out.PaidMicroUSD > math.MaxInt64-fee {
			return out, ErrRoutineBudgetOverflow
		}
		out.PaidMicroUSD += fee
	}
	return out, nil
}

// RequireRemaining also gates later relevant actions. Once a dimension refuses
// a call, reconciliation cannot restart the exhausted actor's actions.
func (b *RoutineCallBudget) RequireRemaining() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remainingLocked(RoutineBudgetAmount{})
}
func (b *RoutineCallBudget) remainingLocked(next RoutineBudgetAmount) error {
	if b.stopped != nil {
		return b.stopped
	}
	if !b.now().Before(b.binding.ExpiresAt) {
		b.stopped = ErrRoutineBudgetBinding
	} else {
		left := b.binding.Remaining
		if next.Tokens > left.Tokens-b.allocated.Tokens || (b.binding.Mode == "tokens" || b.binding.Mode == "both") && b.allocated.Tokens >= left.Tokens {
			b.stopped = errors.New("token_budget_exhausted")
		} else if next.PaidMicroUSD > left.PaidMicroUSD-b.allocated.PaidMicroUSD || (b.binding.Mode == "money" || b.binding.Mode == "both") && b.allocated.PaidMicroUSD >= left.PaidMicroUSD {
			b.stopped = errors.New("money_budget_exhausted")
		} else if next.RecoveryMS > left.RecoveryMS-b.allocated.RecoveryMS || b.allocated.RecoveryMS >= left.RecoveryMS {
			b.stopped = errors.New("recovery_budget_exhausted")
		}
	}
	return b.stopped
}

// Call reserves a complete maximum before invoking the provider. Independent
// calls may run in parallel; their outstanding maxima share one locked balance.
// Retrying this key never invokes the callback again, even after response loss.
func (b *RoutineCallBudget) Call(ctx context.Context, key string, maximum RoutineCallMaximum, provider func(context.Context, RoutineCallPermit) (RoutineCallUsage, error)) error {
	if !budgetKey(key) || provider == nil || maximum.RecoveryMS <= 0 {
		return ErrRoutineBudgetBinding
	}
	amount, err := maximum.amount(b.binding.Price)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	if _, ok := b.calls[key]; ok {
		b.mu.Unlock()
		return ErrRoutineCallReplay
	}
	if len(b.calls) >= 4096 {
		b.mu.Unlock()
		return errors.New("subgrant_count_exhausted")
	}
	if err := b.remainingLocked(amount); err != nil {
		b.mu.Unlock()
		return err
	}
	c := &routineCall{maximum: amount, counts: maximum}
	b.calls[key] = c
	b.allocated.Tokens += amount.Tokens
	b.allocated.PaidMicroUSD += amount.PaidMicroUSD
	b.allocated.RecoveryMS += amount.RecoveryMS
	b.mu.Unlock()
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	reservation, err := b.broker.ReserveRoutineCall(op, b.binding, key, amount)
	cancel()
	if err != nil {
		return err
	} // transport loss is not evidence of unused hold
	g, root := reservation.Grant, b.binding.Grant
	if !reservation.DispatchAllowed {
		return ErrRoutineCallReplay
	}
	if !budgetKey(g.ID) || g.ParentID == nil || *g.ParentID != root.ID || g.RunID != root.RunID || g.AttemptID != root.AttemptID || g.ActionID != root.ActionID || g.AgentRunID != root.AgentRunID || g.AccountID != root.AccountID || g.GrantKey != key || g.Maximum != amount || g.State != "held" || g.BillingMode != root.BillingMode {
		return ErrRoutineBudgetBinding
	}
	b.mu.Lock()
	c.grant = g
	expired := !b.now().Before(b.binding.ExpiresAt)
	b.mu.Unlock()
	if expired {
		return ErrRoutineBudgetBinding
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	callCtx, callCancel := context.WithTimeout(ctx, time.Duration(maximum.RecoveryMS)*time.Millisecond)
	defer callCancel()
	usage, err := provider(callCtx, RoutineCallPermit{GrantID: g.ID, CallKey: key, Maximum: maximum, Price: b.binding.Price})
	if err != nil {
		return err
	} // keep maximum until explicit reconciliation
	return b.Reconcile(ctx, key, usage)
}

// Reconcile accepts trusted provider evidence without re-dispatching a call.
// Partial/lost receipts retain the entire maximum. Complete replay settles once.
func (b *RoutineCallBudget) Reconcile(ctx context.Context, key string, usage RoutineCallUsage) error {
	if err := b.settleMu.LockContext(ctx); err != nil {
		return err
	}
	defer b.settleMu.Unlock()
	b.mu.Lock()
	c := b.calls[key]
	if c == nil || c.grant.ID == "" {
		b.mu.Unlock()
		return ErrRoutineBudgetBinding
	}
	m := c.counts
	b.mu.Unlock()
	u := usage.Counts
	if u.InputTokens > m.InputTokens || u.OutputTokens > m.OutputTokens || u.CacheReadTokens > m.CacheReadTokens || u.CacheWriteTokens > m.CacheWriteTokens || u.ToolUnits > m.ToolUnits || u.RecoveryMS > m.RecoveryMS || !budgetDigest(usage.EvidenceDigest) {
		return ErrRoutineBudgetBinding
	}
	actual, err := u.amount(b.binding.Price)
	if err != nil {
		return err
	}
	actual.PaidMicroUSD = usage.PaidMicroUSD
	if !amountWithin(actual, c.maximum) || b.binding.Grant.BillingMode == "subscription" && actual.PaidMicroUSD != 0 {
		return ErrRoutineBudgetBinding
	}
	in := RoutineBudgetSettlement{EventKey: key, Usage: actual, UsageComplete: usage.Complete, TokensKnown: usage.TokensKnown, PaidCostKnown: usage.PaidCostKnown, EvidenceDigest: usage.EvidenceDigest}
	if c.final != nil {
		if *c.final != in {
			return ErrRoutineCallReplay
		}
		return nil
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	g, err := b.broker.SettleRoutineCall(op, b.binding.Grant.RunID, c.grant.ID, in)
	cancel()
	if err != nil {
		return err
	}
	if !in.UsageComplete || !in.TokensKnown || !in.PaidCostKnown {
		return ErrRoutineUsageUnconfirmed
	}
	if g.ID != c.grant.ID || g.State != "settled" || g.Usage != actual {
		return ErrRoutineUsageUnconfirmed
	}
	b.mu.Lock()
	b.allocated.Tokens -= c.maximum.Tokens - actual.Tokens
	b.allocated.PaidMicroUSD -= c.maximum.PaidMicroUSD - actual.PaidMicroUSD
	b.allocated.RecoveryMS -= c.maximum.RecoveryMS - actual.RecoveryMS
	c.final = &in
	b.mu.Unlock()
	return nil
}

func startRoutineBudgetAdapter(ctx context.Context, adapter Adapter, r StartRequest, budget *RoutineCallBudget, observe func(AdapterEvent)) (Process, error) {
	if r.Run.RoutineBudget == nil {
		return adapter.Start(ctx, r, observe)
	}
	a, ok := adapter.(RoutineBudgetAdapter)
	if !ok || budget == nil {
		return nil, ErrBudgetEnforcementUnavailable
	}
	if err := budget.RequireRemaining(); err != nil {
		return nil, err
	}
	return a.StartBudgeted(ctx, r, budget, observe)
}
