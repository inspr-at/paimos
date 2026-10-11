// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/routinebudget"
)

type qualifiedBudgetFixture struct {
	qualification    RoutineBudgetQualification
	budgeted, legacy int
}

func (*qualifiedBudgetFixture) Name() string { return "controlled-provider" }
func (a *qualifiedBudgetFixture) RoutineBudgetQualification(Profile) (RoutineBudgetQualification, error) {
	return a.qualification, nil
}
func (a *qualifiedBudgetFixture) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	a.legacy++
	return nil, nil
}
func (a *qualifiedBudgetFixture) StartBudgeted(_ context.Context, _ StartRequest, _ *RoutineCallBudget, _ func(AdapterEvent)) (Process, error) {
	a.budgeted++
	return nil, nil
}

// This broker retains dispatch intents and final receipts independently of the
// local controller, including a committed settlement whose reply was lost.
// S09's own package tests exercise the real DB broker and transactional fences.
type budgetBrokerFixture struct {
	mu        sync.Mutex
	grants    map[string]routinebudget.Grant
	receipts  map[string]routinebudget.Settlement
	writes    int
	loseReply bool
}

func (s *budgetBrokerFixture) ReserveRoutineCall(_ context.Context, b RoutineProviderBudget, key string, max routinebudget.Amount) (RoutineCallReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.grants[key]; ok {
		return RoutineCallReservation{Grant: g}, nil
	}
	root := b.Grant
	g := root
	g.ID, g.GrantKey, g.Maximum, g.ParentID = "call-"+key, key, max, &root.ID
	s.grants[key] = g
	return RoutineCallReservation{Grant: g, DispatchAllowed: true}, nil
}
func (s *budgetBrokerFixture) SettleRoutineCall(_ context.Context, run, id string, in routinebudget.Settlement) (routinebudget.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, g := range s.grants {
		if g.ID != id || g.RunID != run {
			continue
		}
		if prior, ok := s.receipts[id]; ok {
			if prior != in {
				return g, ErrRoutineCallReplay
			}
			return g, nil
		}
		if in.UsageComplete && in.TokensKnown && in.PaidCostKnown {
			g.State, g.Usage = "settled", in.Usage
			s.receipts[id] = in
			s.writes++
		} else {
			g.State = "unknown"
		}
		s.grants[key] = g
		if s.loseReply && g.State == "settled" {
			s.loseReply = false
			return routinebudget.Grant{}, errors.New("fixture settlement response lost")
		}
		return g, nil
	}
	return routinebudget.Grant{}, errors.New("fixture grant missing")
}

func routineBudgetFixture(t *testing.T, mode string, tokens, money int64) (*RoutineCallBudget, *budgetBrokerFixture, StartRequest, *qualifiedBudgetFixture) {
	t.Helper()
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	price := RoutineProviderPrice{Provider: "fixture", Model: "fixture-model", Currency: "USD", SourceDigest: strings.Repeat("a", 64), InputPerMillion: 1_000_000, OutputPerMillion: 1_000_000, CacheReadPerMillion: 1_000_000, CacheWritePerMillion: 1_000_000, ToolPerUnit: 20}
	grant := routinebudget.Grant{ID: "root", RunID: "routine", AttemptID: "attempt", ActionID: "action", GrantKey: "actor", AgentRunID: "agent-run", AccountID: "account", Maximum: routinebudget.Amount{Tokens: tokens, PaidMicroUSD: money, RecoveryMS: 100000}, State: "held", BillingMode: "api"}
	transport := &RoutineProviderBudget{Capability: RoutineProviderBudgetCapability, RuntimeDigest: strings.Repeat("b", 64), TenantID: "tenant", PrincipalID: "agent", Generation: "generation", ModelProfileID: "profile", Mode: mode, Grant: grant, Remaining: grant.Maximum, Price: price, ExpiresAt: now.Add(time.Hour)}
	r := StartRequest{TenantID: "tenant", PrincipalID: "agent", Generation: "generation", Profile: Profile{ID: "profile", Model: price.Model}, Run: Run{ID: "agent-run", RequestedAccountID: "account", RoutineBudget: transport}}
	a := &qualifiedBudgetFixture{qualification: RoutineBudgetQualification{Capability: transport.Capability, RuntimeDigest: transport.RuntimeDigest, BillingMode: "api", Price: price, Tokens: true, PaidMoney: true}}
	broker := &budgetBrokerFixture{grants: map[string]routinebudget.Grant{}, receipts: map[string]routinebudget.Settlement{}}
	b, err := newRoutineCallBudget(r, a, broker, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return b, broker, r, a
}
func completeBudgetUsage(counts RoutineCallMaximum, paid int64) RoutineCallUsage {
	return RoutineCallUsage{Counts: counts, PaidMicroUSD: paid, Complete: true, TokensKnown: true, PaidCostKnown: true, EvidenceDigest: strings.Repeat("c", 64)}
}

// Risk: parallel billable calls, hidden cache/tool fees or telemetry loss can
// overspend a held actor grant; duplicate replies can release or charge twice.
func TestRoutineProviderBudgetBeforeCallsAndSettlement(t *testing.T) {
	maximum := RoutineCallMaximum{InputTokens: 10, OutputTokens: 10, CacheReadTokens: 10, CacheWriteTokens: 10, ToolUnits: 1, RecoveryMS: 10000}
	for _, tc := range []struct {
		mode          string
		tokens, money int64
		reason        string
	}{
		{"tokens", 80, 1000, "token_budget_exhausted"},
		{"money", 1000, 120, "money_budget_exhausted"},
		{"both", 100, 120, "money_budget_exhausted"},
		{"both", 80, 1000, "token_budget_exhausted"},
	} {
		t.Run(fmt.Sprintf("%s-%s", tc.mode, tc.reason), func(t *testing.T) {
			b, broker, r, a := routineBudgetFixture(t, tc.mode, tc.tokens, tc.money)
			wire, err := json.Marshal(r.Run)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Run
			if err = json.Unmarshal(wire, &decoded); err != nil || decoded.RoutineBudget == nil || *decoded.RoutineBudget != *r.Run.RoutineBudget {
				t.Fatalf("grant transport: %v", err)
			}
			if _, err = startRoutineBudgetAdapter(t.Context(), a, r, b, func(AdapterEvent) {}); err != nil || a.budgeted != 1 || a.legacy != 0 {
				t.Fatalf("budgeted dispatch: %v", err)
			}
			entered := make(chan RoutineCallPermit, 2)
			release := make(chan struct{})
			done := make(chan error, 2)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var providerCalls atomic.Int64
			for _, key := range []string{"one", "two"} {
				go func() {
					done <- b.Call(ctx, key, maximum, func(ctx context.Context, p RoutineCallPermit) (RoutineCallUsage, error) {
						providerCalls.Add(1)
						entered <- p
						select {
						case <-release:
							return completeBudgetUsage(maximum, 60), nil
						case <-ctx.Done():
							return RoutineCallUsage{}, ctx.Err()
						}
					})
				}()
			}
			for range 2 {
				select {
				case p := <-entered:
					if p.Maximum != maximum || p.Price.ToolPerUnit != 20 || p.GrantID == "" {
						t.Fatal("incomplete provider maximum")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			// Both calls are inside their provider callbacks at this barrier.
			b.mu.Lock()
			held := b.allocated
			b.mu.Unlock()
			if held.Tokens != 80 || held.PaidMicroUSD != 120 || !amountWithin(held, b.binding.Remaining) {
				t.Fatalf("parallel maximum: %+v", held)
			}
			err = b.Call(ctx, "next", RoutineCallMaximum{InputTokens: 1, RecoveryMS: 1000}, func(context.Context, RoutineCallPermit) (RoutineCallUsage, error) {
				providerCalls.Add(1)
				return RoutineCallUsage{}, nil
			})
			remainingErr := b.RequireRemaining()
			if err == nil || err.Error() != tc.reason || remainingErr == nil || remainingErr.Error() != tc.reason || providerCalls.Load() != 2 {
				t.Fatalf("next call must refuse before provider: %v calls=%d", err, providerCalls.Load())
			}
			close(release)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			if err = b.Reconcile(ctx, "one", completeBudgetUsage(maximum, 60)); err != nil {
				t.Fatal(err)
			}
			if broker.writes != 2 || len(broker.grants) != 2 {
				t.Fatalf("duplicate receipt changed ledger: writes=%d grants=%d", broker.writes, len(broker.grants))
			}
			if err = b.Reconcile(ctx, "one", completeBudgetUsage(maximum, 59)); !errors.Is(err, ErrRoutineCallReplay) {
				t.Fatalf("changed receipt: %v", err)
			}
		})
	}
	t.Run("partial-and-lost-receipts", func(t *testing.T) {
		b, broker, _, _ := routineBudgetFixture(t, "both", 100, 200)
		var calls int
		partial := completeBudgetUsage(RoutineCallMaximum{InputTokens: 5}, 5)
		partial.Complete = false
		if err := b.Call(t.Context(), "partial", maximum, func(context.Context, RoutineCallPermit) (RoutineCallUsage, error) { calls++; return partial, nil }); !errors.Is(err, ErrRoutineUsageUnconfirmed) {
			t.Fatalf("partial receipt: %v", err)
		}
		if b.allocated.Tokens != 40 || b.allocated.PaidMicroUSD != 60 || broker.grants["partial"].State != "unknown" || broker.writes != 0 {
			t.Fatal("partial usage released its maximum")
		}
		if err := b.Call(t.Context(), "partial", maximum, func(context.Context, RoutineCallPermit) (RoutineCallUsage, error) { calls++; return partial, nil }); !errors.Is(err, ErrRoutineCallReplay) || calls != 1 {
			t.Fatalf("call replay: %v", err)
		}
		broker.loseReply = true
		final := completeBudgetUsage(RoutineCallMaximum{InputTokens: 5}, 5)
		if err := b.Reconcile(t.Context(), "partial", final); err == nil || err.Error() != "fixture settlement response lost" {
			t.Fatalf("lost reply: %v", err)
		}
		if b.allocated.Tokens != 40 || b.allocated.PaidMicroUSD != 60 || broker.writes != 1 {
			t.Fatal("lost acknowledgement released local hold")
		}
		if err := b.Reconcile(t.Context(), "partial", final); err != nil {
			t.Fatal(err)
		}
		if b.allocated.Tokens != 5 || b.allocated.PaidMicroUSD != 5 || broker.writes != 1 {
			t.Fatal("receipt replay charged twice")
		}
		if err := b.Call(t.Context(), "lost-usage", maximum, func(context.Context, RoutineCallPermit) (RoutineCallUsage, error) {
			calls++
			return RoutineCallUsage{}, errors.New("fixture provider reply lost")
		}); err == nil || err.Error() != "fixture provider reply lost" {
			t.Fatalf("lost usage: %v", err)
		}
		if b.allocated.Tokens != 45 || b.allocated.PaidMicroUSD != 65 || broker.grants["lost-usage"].State != "held" {
			t.Fatal("lost usage treated as zero")
		}
	})
	t.Run("price-identity-and-billing", func(t *testing.T) {
		_, broker, r, a := routineBudgetFixture(t, "both", 100, 200)
		for _, change := range []func(*RoutineProviderBudget){
			func(g *RoutineProviderBudget) { g.Price.Currency = "EUR" },
			func(g *RoutineProviderBudget) { g.Price.Provider = "another-provider" },
			func(g *RoutineProviderBudget) { g.Price.Model = "another-model" },
			func(g *RoutineProviderBudget) { g.Price.SourceDigest = strings.Repeat("d", 64) },
			func(g *RoutineProviderBudget) { g.Price.ToolPerUnit = 0 },
			func(g *RoutineProviderBudget) { g.RuntimeDigest = strings.Repeat("d", 64) },
			func(g *RoutineProviderBudget) { g.Generation = "other-generation" },
			func(g *RoutineProviderBudget) { g.Grant.AccountID = "other-account" },
			func(g *RoutineProviderBudget) { g.Remaining.Tokens++ },
			func(g *RoutineProviderBudget) { g.Grant.State = "unknown" },
		} {
			altered := *r.Run.RoutineBudget
			change(&altered)
			copy := r
			copy.Run.RoutineBudget = &altered
			if _, err := newRoutineCallBudget(copy, a, broker, func() time.Time { return altered.ExpiresAt.Add(-time.Minute) }); !errors.Is(err, ErrRoutineBudgetBinding) {
				t.Fatalf("wrong authority/price accepted: %v", err)
			}
		}
		for _, mode := range []string{"money", "both"} {
			r.Run.RoutineBudget.Mode = mode
			r.Run.RoutineBudget.Grant.BillingMode = "subscription"
			a.qualification.BillingMode = "subscription"
			a.qualification.PaidMoney = false
			if _, err := newRoutineCallBudget(r, a, broker, time.Now); !errors.Is(err, ErrBudgetEnforcementUnavailable) {
				t.Fatalf("subscription paid budget: %v", err)
			}
		}
		a.qualification.BillingMode = "unknown"
		r.Run.RoutineBudget.Mode = "tokens"
		r.Run.RoutineBudget.Grant.BillingMode = "unknown"
		if _, err := newRoutineCallBudget(r, a, broker, func() time.Time { return r.Run.RoutineBudget.ExpiresAt.Add(-time.Minute) }); !errors.Is(err, ErrBudgetEnforcementUnavailable) {
			t.Fatalf("unknown billing: %v", err)
		}
		if len(broker.grants) != 0 {
			t.Fatal("invalid provenance reached broker")
		}
		if _, err := maximum.amount(RoutineProviderPrice{InputPerMillion: math.MaxInt64, ToolPerUnit: math.MaxInt64}); !errors.Is(err, ErrRoutineBudgetOverflow) {
			t.Fatalf("fee overflow: %v", err)
		}
	})
	t.Run("subscription-token-only-and-expiry", func(t *testing.T) {
		_, broker, r, a := routineBudgetFixture(t, "tokens", 100, 0)
		price := r.Run.RoutineBudget.Price
		price.InputPerMillion, price.OutputPerMillion, price.CacheReadPerMillion, price.CacheWritePerMillion, price.ToolPerUnit = 0, 0, 0, 0, 0
		r.Run.RoutineBudget.Price = price
		r.Run.RoutineBudget.Grant.BillingMode = "subscription"
		a.qualification.Price, a.qualification.BillingMode, a.qualification.PaidMoney = price, "subscription", false
		now := r.Run.RoutineBudget.ExpiresAt.Add(-time.Minute)
		b, err := newRoutineCallBudget(r, a, broker, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		if err = b.Call(t.Context(), "subscription", maximum, func(context.Context, RoutineCallPermit) (RoutineCallUsage, error) {
			calls++
			return completeBudgetUsage(maximum, 0), nil
		}); err != nil {
			t.Fatal(err)
		}
		if b.allocated.PaidMicroUSD != 0 || broker.grants["subscription"].Usage.PaidMicroUSD != 0 {
			t.Fatal("subscription tokens became paid dollars")
		}
		now = r.Run.RoutineBudget.ExpiresAt
		if err = b.Call(t.Context(), "expired", maximum, func(context.Context, RoutineCallPermit) (RoutineCallUsage, error) {
			calls++
			return RoutineCallUsage{}, nil
		}); !errors.Is(err, ErrRoutineBudgetBinding) || calls != 1 || len(broker.grants) != 1 {
			t.Fatalf("expired grant reached provider/broker: %v", err)
		}
	})
	t.Run("persisted-intent-replay-and-rounding", func(t *testing.T) {
		b, broker, r, a := routineBudgetFixture(t, "both", 100, 200)
		calls := 0
		provider := func(context.Context, RoutineCallPermit) (RoutineCallUsage, error) {
			calls++
			return RoutineCallUsage{}, errors.New("fixture provider reply lost")
		}
		if err := b.Call(t.Context(), "durable", maximum, provider); err == nil {
			t.Fatal("lost provider reply reported success")
		}
		fresh, err := newRoutineCallBudget(r, a, broker, func() time.Time { return r.Run.RoutineBudget.ExpiresAt.Add(-time.Minute) })
		if err != nil {
			t.Fatal(err)
		}
		if err = fresh.Call(t.Context(), "durable", maximum, provider); !errors.Is(err, ErrRoutineCallReplay) || calls != 1 || len(broker.grants) != 1 {
			t.Fatalf("persisted replay dispatched twice: %v calls=%d", err, calls)
		}
		amount, err := maximum.amount(RoutineProviderPrice{InputPerMillion: 1, OutputPerMillion: 1, CacheReadPerMillion: 1, CacheWritePerMillion: 1, ToolPerUnit: 7})
		if err != nil || amount.Tokens != 40 || amount.PaidMicroUSD != 11 {
			t.Fatalf("all fractional token fees must round upward plus tools: %+v %v", amount, err)
		}
	})
}

// Risk: an unsupported native process can spend before late usage telemetry
// reveals that it never had an enforceable cap, especially in combined mode.
func TestRoutineProviderBudgetRefusesNativeBeforeLaunch(t *testing.T) {
	adapters := []Adapter{&CodexAdapter{}, &ClaudeAdapter{}, &PiAdapter{}, &CursorAdapter{}, &GrokAdapter{}, &ACPAdapter{Harness: Gemini}}
	for _, adapter := range adapters {
		for _, mode := range []string{"tokens", "money", "both"} {
			t.Run(adapter.Name()+"-"+mode, func(t *testing.T) {
				s, api, _ := testSupervisor(t)
				s.adapters[api.profile.Harness] = adapter
				run := api.run
				run.RoutineBudget = &RoutineProviderBudget{Mode: mode}
				if err := s.StartRun(t.Context(), run); !errors.Is(err, ErrBudgetEnforcementUnavailable) {
					t.Fatalf("supervisor prelaunch refusal: %v", err)
				}
				if api.claims != 0 || len(api.routeAccounts) != 0 || len(s.runs) != 0 {
					t.Fatal("unsupported budget reached route/claim/journal")
				}
				if _, err := adapter.Start(t.Context(), StartRequest{Run: run}, func(AdapterEvent) {}); !errors.Is(err, ErrBudgetEnforcementUnavailable) {
					t.Fatalf("direct adapter prelaunch refusal: %v", err)
				}
			})
		}
	}
}
