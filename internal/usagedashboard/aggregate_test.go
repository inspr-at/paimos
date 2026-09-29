// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSessionSelectReadsUsageRowsOnly(t *testing.T) {
	query := sessionSelect()
	for _, forbidden := range []string{"run_telemetry", "agent_runs", "list_cost_micros", "cached_tokens", "subscription_covered", "s.model", "currency"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("query reads %s: %s", forbidden, query)
		}
	}
	for _, need := range []string{"u.model", "u.cached_input_tokens", "u.estimated_cost_usd", "u.billing_mode", "u.subscription_label", "s.created_at", "s.harness", "s.phase"} {
		if !strings.Contains(query, need) {
			t.Fatalf("query missing %s", need)
		}
	}
}

func TestTrimSessionsDropsOnlyTheLastSession(t *testing.T) {
	rows := make([]sessionRow, 0, (maxSessions+1)*2)
	for i := 0; i < maxSessions+1; i++ {
		id := fmt.Sprintf("s%05d", i)
		rows = append(rows, sessionRow{id: id}, sessionRow{id: id})
	}
	kept, truncated := trimSessions(rows)
	if !truncated || len(kept) != maxSessions*2 {
		t.Fatalf("kept %d truncated %v", len(kept), truncated)
	}
	last := fmt.Sprintf("s%05d", maxSessions-1)
	if kept[0].id != "s00000" || kept[len(kept)-1].id != last {
		t.Fatalf("bounds %s %s", kept[0].id, kept[len(kept)-1].id)
	}
	if _, truncated := trimSessions(rows[:maxSessions*2]); truncated {
		t.Fatal("exact cap was truncated")
	}
}

func TestAggregateCountsASessionOnceAcrossModels(t *testing.T) {
	at := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)
	ticket, key, title := "t1", "T-1", "Known ticket"
	alpha, beta := "alpha", "beta"
	plan := "Plan"
	sub, api := "subscription", "api"
	first, second := "0.000000400000", "0.000000400000"
	rows := []sessionRow{
		{id: "s1", projectID: "p", projectKey: "P", projectTitle: "Proj", ticketID: &ticket, ticketKey: &key, ticketTitle: &title, created: at, reported: true, model: &alpha, input: i64p(5), output: i64p(1), cached: i64p(1), cost: &first, billingMode: &sub, subscription: &plan},
		{id: "s1", projectID: "p", projectKey: "P", projectTitle: "Proj", ticketID: &ticket, ticketKey: &key, ticketTitle: &title, created: at, reported: true, model: &beta, input: i64p(7), output: i64p(2), cost: &second, provisional: true, billingMode: &api},
		{id: "s2", projectID: "p", projectKey: "P", projectTitle: "Proj", created: at.Add(time.Hour)},
	}
	total, _, models, harnesses, subs, trend, tickets, unknown, err := aggregate(rows)
	if err != nil {
		t.Fatal(err)
	}
	if total.Sessions != 2 || total.UsageRows != 2 || total.UnreportedSessions != 1 {
		t.Fatalf("sessions %+v", total)
	}
	if total.InputTokens == nil || *total.InputTokens != "12" || total.InputKnownRows != 2 || total.InputUnknownRows != 1 {
		t.Fatalf("input %+v", total)
	}
	if total.CachedInputTokens == nil || *total.CachedInputTokens != "1" || total.CachedInputKnownRows != 1 || total.CachedInputUnknownRows != 2 {
		t.Fatalf("cached %+v", total)
	}
	if total.EstimatedCostUSD == nil || *total.EstimatedCostUSD != "0.000000800000" || total.CostState != "partial" || total.ProvisionalRows != 1 || total.ProvisionalSessions != 1 {
		t.Fatalf("cost %+v", total)
	}
	if len(models) != 3 || models[0].Label != "alpha" || models[0].Sessions != 1 || models[1].Sessions != 1 || models[2].Label != "Unreported" {
		t.Fatalf("models %+v", models)
	}
	if len(harnesses) != 1 || harnesses[0].Label != "Unreported" || harnesses[0].Sessions != 2 {
		t.Fatalf("harness %+v", harnesses)
	}
	planGroup := findLabel(subs, "Plan")
	apiGroup := findLabel(subs, "API")
	if planGroup.Sessions != 1 || planGroup.BillingMode != "subscription" || planGroup.EstimatedCostUSD == nil || *planGroup.EstimatedCostUSD != "0.000000400000" {
		t.Fatalf("plan %+v", planGroup)
	}
	if apiGroup.Sessions != 1 || apiGroup.BillingMode != "api" || apiGroup.Sessions+planGroup.Sessions+findLabel(subs, "Unreported").Sessions != 3 {
		t.Fatalf("subscription session sum double-counted totals: %+v", subs)
	}
	if len(trend) != 1 || trend[0].Day != "2026-09-10" || trend[0].Group.Sessions != 2 || trend[0].Group.Label != "" {
		t.Fatalf("trend %+v", trend)
	}
	if unknown != 0 || len(tickets) != 1 || tickets[0].Key != "T-1" || tickets[0].Sessions != 1 || tickets[0].EstimatedCostUSD == nil || *tickets[0].EstimatedCostUSD != "0.000000800000" {
		t.Fatalf("tickets %d %+v", unknown, tickets)
	}
}

func findLabel(groups []UsageGroup, label string) UsageGroup {
	for _, group := range groups {
		if group.Label == label {
			return group
		}
	}
	return UsageGroup{}
}

func i64p(v int64) *int64 { return &v }
