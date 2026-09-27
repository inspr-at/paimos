// SPDX-License-Identifier: AGPL-3.0-only

package ticketwork

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestAgentWorkRoutePermission(t *testing.T) {
	perm, ok := authz.PermissionForPattern("GET /api/nodes/{nodeId}/agent-work")
	if !ok || perm != "nodes.read" {
		t.Fatalf("permission %q ok=%v", perm, ok)
	}
}

func TestSummarizeAgentWork(t *testing.T) {
	zero := summarize([]Session{knownSession("0", "0", "0", "estimated", 0, "known")})
	if zero.TokensState != "known" || str(zero.InputTokens) != "0" || str(zero.OutputTokens) != "0" || zero.CostState != "estimated" || str(zero.EstimatedCostUSD) != "0.000000000000" {
		t.Fatalf("reported zero must stay zero: %+v", zero)
	}
	mixed := summarize([]Session{
		knownSession("1000", "200", "1.000000000000", "estimated", 150, "known"),
		knownSession("50", "10", "2.500000000000", "provisional", 90, "ongoing"),
		{},
	})
	if mixed.TokensState != "partial" || str(mixed.InputTokens) != "1050" || str(mixed.OutputTokens) != "210" || mixed.UnknownTokenSessions != 1 {
		t.Fatalf("tokens %+v", mixed)
	}
	if mixed.CostState != "partial" || str(mixed.EstimatedCostUSD) != "3.500000000000" || mixed.UnknownCostSessions != 1 {
		t.Fatalf("cost %+v", mixed)
	}
	if mixed.DurationState != "partial" || mixed.DurationSeconds == nil || *mixed.DurationSeconds != 240 {
		t.Fatalf("duration %+v", mixed)
	}
	both := summarize([]Session{
		knownSession("1", "1", "0.000000000002", "estimated", 1, "known"),
		knownSession("1", "1", "0.000000000003", "provisional", 1, "known"),
	})
	if both.CostState != "provisional" || str(both.EstimatedCostUSD) != "0.000000000005" || both.TokensState != "known" {
		t.Fatalf("exact decimal sum with a provisional price: %+v", both)
	}
	empty := summarize(nil)
	if empty.SessionCount != 0 || empty.TokensState != "unknown" || empty.InputTokens != nil || empty.EstimatedCostUSD != nil || empty.DurationSeconds != nil || empty.Currency != "USD" {
		t.Fatalf("empty %+v", empty)
	}
	huge := strconv.FormatInt(math.MaxInt64, 10)
	overflow := summarize([]Session{
		knownSession(huge, "1", "1", "estimated", 1, "known"),
		knownSession("1", "1", "1", "estimated", 1, "known"),
	})
	if overflow.TokensState != "unknown" || overflow.InputTokens != nil || overflow.CostState != "estimated" {
		t.Fatalf("token overflow must not wrap: %+v", overflow)
	}
	wide := "999999999999999999.000000000000"
	money := summarize([]Session{
		knownSession("1", "1", wide, "estimated", 1, "known"),
		knownSession("1", "1", wide, "estimated", 1, "known"),
	})
	if money.CostState != "unknown" || money.EstimatedCostUSD != nil {
		t.Fatalf("cost overflow must not become a float: %+v", money)
	}
	negative := summarize([]Session{knownSession("-1", "5", "10", "estimated", 1, "known")})
	if negative.TokensState != "unknown" || negative.InputTokens != nil || negative.UnknownTokenSessions != 1 {
		t.Fatalf("negative tokens %+v", negative)
	}
}

func TestApplyUsageGroupsModelsOnce(t *testing.T) {
	unicodeModel := strings.Repeat("界", 128)
	if got := normalizeModel(usageRow{model: unicodeModel}).Model; got != unicodeModel {
		t.Fatal("128 Unicode characters were dropped by the display normalizer")
	}
	seconds := int64(150)
	s := Session{DurationSeconds: &seconds, DurationState: "known", Models: []UsageModel{}}
	applyUsage(&s, []usageRow{
		{model: "gpt-test", input: ptr("1000"), output: ptr("200"), cached: ptr("40"), cost: ptr("1.000000000000"), price: ptr("1"), billing: "api"},
		{model: "gpt-test-mini", input: ptr("5"), output: ptr("1"), cached: ptr("0"), cost: ptr("0.250000000000"), price: ptr("1"), billing: "api"},
	})
	if s.DurationSeconds == nil || *s.DurationSeconds != 150 || s.DurationState != "known" {
		t.Fatalf("duration was multiplied: %+v", s)
	}
	if !s.UsageReported || len(s.Models) != 2 || s.ModelsTruncated || str(s.InputTokens) != "1005" || str(s.OutputTokens) != "201" || str(s.CachedInputTokens) != "40" {
		t.Fatalf("models were not grouped once: %+v", s)
	}
	if s.TokensState != "known" || s.CostState != "estimated" || str(s.EstimatedCostUSD) != "1.250000000000" {
		t.Fatalf("session rollup %+v", s)
	}
	partial := Session{Models: []UsageModel{}}
	applyUsage(&partial, []usageRow{
		{model: "priced", input: ptr("10"), output: ptr("2"), cached: ptr("1"), cost: ptr("0.500000000000"), provisional: true, price: ptr("4"), billing: "subscription", subscription: ptr("Reported team")},
		{model: "open", input: ptr("3"), output: nil, cached: nil, provisional: true, billing: "unknown"},
	})
	if partial.TokensState != "partial" || str(partial.InputTokens) != "10" || partial.UnknownTokenModels != 1 || partial.CostState != "partial" || str(partial.EstimatedCostUSD) != "0.500000000000" || partial.UnknownCostModels != 1 {
		t.Fatalf("partial model coverage %+v", partial)
	}
	if partial.Models[0].BillingMode != "subscription" || str(partial.Models[0].SubscriptionLabel) != "Reported team" || partial.Models[0].CostState != "estimated" || !partial.Models[0].Provisional || str(partial.Models[0].PriceVersion) != "4" {
		t.Fatalf("model metadata %+v", partial.Models[0])
	}
	var rows []usageRow
	for i := 0; i < maxModels+1; i++ {
		rows = append(rows, usageRow{model: fmt.Sprintf("m%02d", i), input: ptr("1"), output: ptr("1"), cached: ptr("0"), cost: ptr("0.010000000000"), price: ptr("1"), billing: "api"})
	}
	truncated := Session{Models: []UsageModel{}}
	applyUsage(&truncated, rows)
	if !truncated.ModelsTruncated || len(truncated.Models) != maxModels || truncated.TokensState == "known" || truncated.UnknownTokenModels != 1 {
		t.Fatalf("truncation %+v models=%d", truncated.TokensState, len(truncated.Models))
	}
	got, _ := new(big.Rat).SetString(str(truncated.EstimatedCostUSD))
	want, _ := new(big.Rat).SetString("0.320000000000")
	if got == nil || got.Cmp(want) != 0 {
		t.Fatalf("truncated cost %s", str(truncated.EstimatedCostUSD))
	}
}

func knownSession(in, out, cost, costState string, seconds int64, durationState string) Session {
	s := Session{TokensState: "unknown", CachedState: "unknown", CostState: "unknown", DurationState: durationState, Models: []UsageModel{}}
	if in != "" || out != "" {
		s.TokensState = "known"
		if in != "" {
			s.InputTokens = &in
		}
		if out != "" {
			s.OutputTokens = &out
		}
	}
	if cost != "" {
		s.CostState = costState
		s.EstimatedCostUSD = &cost
	}
	if durationState != "unknown" {
		s.DurationSeconds = &seconds
	}
	return s
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr(s string) *string { return &s }

func TestTicketAgentWork(t *testing.T) {
	f := newWorkFixture(t)
	epic, ticket, task := uid(), uid(), uid()
	epic2, ticket2 := uid(), uid()
	hiddenProject, hiddenTicket := uid(), uid()
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	f.node(t, epic, "epic", "TW1-2", "Epic", f.project)
	f.node(t, ticket, "ticket", "TW1-3", "Ticket", epic)
	f.node(t, task, "task", "TW1-4", "Task", ticket)
	f.node(t, epic2, "epic", "TW1-5", "Other epic", f.project)
	f.node(t, ticket2, "ticket", "TW1-6", "Sibling", epic2)
	f.node(t, hiddenProject, "project", "TW1-7", "Hidden", "")
	f.node(t, hiddenTicket, "ticket", "TW1-8", "Hidden ticket", hiddenProject)
	f.bindGuest(t, f.project)

	s1, s2, s3 := uid(), uid(), uid()
	s4, s5, s6, sHidden := uid(), uid(), uid(), uid()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	f.session(t, s1, f.project, epic, "codex", "gpt-test", "high", past, past.Add(150*time.Second), past, "stopped")
	f.session(t, s2, f.project, ticket, "claude", "claude-test", "low", time.Now().Add(-90*time.Second), time.Time{}, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "working")
	f.session(t, s3, f.project, task, "cursor", "", "", past, past.Add(60*time.Second), past, "stopped")
	f.session(t, s4, f.project, ticket2, "pi", "pi-test", "medium", past, past.Add(10*time.Second), past, "stopped")
	f.session(t, s5, hiddenProject, ticket, "grok", "grok-test", "high", past, past.Add(5*time.Second), past, "stopped")
	f.session(t, s6, f.project, "", "codex", "unbound", "low", past, past.Add(5*time.Second), past, "stopped")
	f.session(t, sHidden, hiddenProject, hiddenTicket, "codex", "hidden-model", "high", past, past.Add(5*time.Second), past, "stopped")
	f.usage(t, f.person, s1, "gpt-test", "1000", "200", "40", "1.000000000000", false, "1", "api", "")
	f.usage(t, f.person, s1, "gpt-test-mini", "5", "1", "0", "0.250000000000", false, "1", "api", "")
	f.usage(t, f.person, s2, "claude-test", "50", "10", "4", "2.500000000000", true, "2", "subscription", "Reported team")
	f.usage(t, f.person, s3, "cursor-fast", "", "", "", "", true, "", "unknown", "")
	f.usage(t, f.person, s4, "pi-test", "4242", "1", "0", "4.242000000000", false, "1", "api", "")
	f.usage(t, f.person, s5, "grok-test", "777", "1", "0", "0.777000000000", false, "1", "api", "")
	foreignProject, foreignTicket := uid(), uid()
	f.nodeFor(t, f.foreign, foreignProject, "project", "TW1-9", "Foreign", "")
	f.nodeFor(t, f.foreign, foreignTicket, "ticket", "TW1-10", "Foreign ticket", foreignProject)
	f.sessionFor(t, f.foreign, s1, foreignProject, foreignTicket, "codex", "gpt-test", "high", past, past.Add(5*time.Second), past, "stopped")
	f.usage(t, f.foreign, s1, "gpt-test", "999999", "1", "0", "9.999990000000", false, "1", "api", "")

	guestEpic := f.get(t, f.guest, epic, "")
	if !guestEpic.UsageAvailable || guestEpic.Currency != "USD" || guestEpic.Kind != "epic" || !guestEpic.IncludesDescendants || guestEpic.ListTruncated || guestEpic.ScopeTruncated {
		t.Fatalf("epic header %+v", guestEpic)
	}
	if guestEpic.Totals.SessionCount != 3 || guestEpic.Totals.TokensState != "partial" || str(guestEpic.Totals.InputTokens) != "1055" || str(guestEpic.Totals.OutputTokens) != "211" {
		t.Fatalf("guest epic tokens %+v", guestEpic.Totals)
	}
	if guestEpic.Totals.CostState != "partial" || str(guestEpic.Totals.EstimatedCostUSD) != "3.750000000000" || guestEpic.Totals.UnknownCostSessions != 1 || guestEpic.Totals.UnknownTokenSessions != 1 {
		t.Fatalf("guest epic cost %+v", guestEpic.Totals)
	}
	if guestEpic.Totals.CachedState != "partial" || str(guestEpic.Totals.CachedInputTokens) != "44" {
		t.Fatalf("cached %+v", guestEpic.Totals)
	}
	if guestEpic.Totals.DurationState != "ongoing" || guestEpic.Totals.DurationSeconds == nil || *guestEpic.Totals.DurationSeconds < 290 || *guestEpic.Totals.DurationSeconds > 330 {
		t.Fatalf("duration counted once per session: %+v", guestEpic.Totals)
	}
	byID := map[string]Session{}
	for _, s := range guestEpic.Sessions {
		byID[s.ID] = s
		if s.ID == s4 || s.ID == s5 || s.ID == s6 || s.ID == sHidden {
			t.Fatalf("leaked session %s", s.ID)
		}
	}
	if len(byID) != 3 {
		t.Fatalf("sessions %+v", guestEpic.Sessions)
	}
	if byID[s1].Harness != "codex" || str(byID[s1].Model) != "gpt-test" || byID[s1].ModelState != "known" || str(byID[s1].Effort) != "high" || byID[s1].DurationState != "known" || byID[s1].DurationSeconds == nil || *byID[s1].DurationSeconds != 150 {
		t.Fatalf("s1 %+v", byID[s1])
	}
	if len(byID[s1].Models) != 2 || str(byID[s1].EstimatedCostUSD) != "1.250000000000" || byID[s1].CostState != "estimated" || str(byID[s1].InputTokens) != "1005" || byID[s1].Models[0].BillingMode != "api" {
		t.Fatalf("s1 usage %+v", byID[s1])
	}
	if byID[s2].CostState != "provisional" || byID[s2].Models[0].BillingMode != "subscription" || str(byID[s2].Models[0].SubscriptionLabel) != "Reported team" || byID[s2].DurationState != "ongoing" || byID[s2].TicketKey != "TW1-3" || str(byID[s2].Models[0].PriceVersion) != "2" {
		t.Fatalf("s2 %+v", byID[s2])
	}
	if byID[s3].ModelState != "missing" || byID[s3].EffortState != "missing" || byID[s3].TokensState != "unknown" || byID[s3].InputTokens != nil || byID[s3].CostState != "unknown" || byID[s3].EstimatedCostUSD != nil || !byID[s3].UsageReported || len(byID[s3].Models) != 1 || byID[s3].Models[0].Model != "cursor-fast" {
		t.Fatalf("unknown counters must stay null: %+v", byID[s3])
	}
	raw := f.body(t, f.guest, epic, "")
	if numberField.Match(raw) {
		t.Fatalf("token or cost encoded as a JSON number: %s", raw)
	}
	if regexp.MustCompile(`4242|999999|"777"`).Match(raw) {
		t.Fatalf("sibling, other tenant or hidden project usage leaked: %s", raw)
	}

	guestTicket := f.get(t, f.guest, ticket, "")
	if guestTicket.Totals.SessionCount != 2 || str(guestTicket.Totals.InputTokens) != "50" || guestTicket.Totals.CostState != "partial" || str(guestTicket.Totals.EstimatedCostUSD) != "2.500000000000" {
		t.Fatalf("ticket should roll up its task only: %+v", guestTicket.Totals)
	}
	for _, s := range guestTicket.Sessions {
		if s.ID == s1 || s.ID == s5 {
			t.Fatalf("ticket included %s", s.ID)
		}
	}
	sibling := f.get(t, f.guest, ticket2, "")
	if sibling.Totals.SessionCount != 1 || sibling.Sessions[0].ID != s4 || str(sibling.Totals.InputTokens) != "4242" || str(sibling.Totals.EstimatedCostUSD) != "4.242000000000" {
		t.Fatalf("sibling %+v", sibling)
	}
	if f.status(t, f.guest, hiddenTicket, "") != http.StatusNotFound || f.status(t, f.foreign, epic, "") != http.StatusNotFound || f.status(t, f.guest, f.project, "") != http.StatusNotFound {
		t.Fatal("hidden, foreign or non-work node was readable")
	}
	if f.status(t, f.guest, epic, "0") != http.StatusBadRequest || f.status(t, f.guest, epic, "81") != http.StatusBadRequest || f.status(t, f.guest, "not-a-uuid", "") != http.StatusBadRequest {
		t.Fatal("bad limit or id was accepted")
	}
	limited := f.get(t, f.guest, epic, "1")
	if !limited.ListTruncated || limited.Totals.SessionCount != 1 || limited.Sessions[0].ID != s2 || str(limited.Totals.EstimatedCostUSD) != "2.500000000000" || limited.Totals.CostState != "provisional" {
		t.Fatalf("bounded list %+v totals %+v", limited.Sessions, limited.Totals)
	}

	admin := f.get(t, f.person, epic, "")
	seenHidden := false
	for _, s := range admin.Sessions {
		if s.ID == s5 {
			seenHidden = true
		}
		if s.ID == s4 || s.ID == s6 || s.ID == sHidden {
			t.Fatalf("admin epic included %s", s.ID)
		}
	}
	if !seenHidden || admin.Totals.SessionCount != 4 || str(admin.Totals.InputTokens) != "1832" {
		t.Fatalf("admin should see the session whose project is hidden from the guest: %+v", admin.Totals)
	}

	if err := f.usageErr(t, f.person, s1, "gpt-test", "9", "9", "0", "0.009000000000", false, "1", "api", ""); err == nil {
		t.Fatal("a second row for the same session and model was inserted")
	}
	again := f.get(t, f.guest, epic, "")
	for _, s := range again.Sessions {
		if s.ID == s1 && (str(s.InputTokens) != "1005" || str(s.EstimatedCostUSD) != "1.250000000000" || len(s.Models) != 2) {
			t.Fatalf("same-model conflict changed the sum: %+v", s)
		}
	}
	if str(again.Totals.InputTokens) != "1055" || str(again.Totals.EstimatedCostUSD) != "3.750000000000" {
		t.Fatalf("retry changed the sum: %+v", again.Totals)
	}
}

func TestTicketAgentWorkWithoutUsageRows(t *testing.T) {
	f := newWorkFixture(t)
	ticket := uid()
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	f.node(t, ticket, "ticket", "TW1-2", "Ticket", f.project)
	sid := uid()
	past := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	f.session(t, sid, f.project, ticket, "codex", "gpt-test", "", past, past.Add(time.Minute), past, "stopped")
	report := f.get(t, f.person, ticket, "")
	if !report.UsageAvailable || report.Totals.SessionCount != 1 || report.Sessions[0].ModelState != "known" || report.Sessions[0].EffortState != "missing" || report.Sessions[0].UsageReported {
		t.Fatalf("%+v", report)
	}
	if len(report.Sessions[0].Models) != 0 || report.Sessions[0].InputTokens != nil || report.Sessions[0].EstimatedCostUSD != nil || report.Totals.TokensState != "unknown" || report.Totals.CostState != "unknown" || report.Totals.InputTokens != nil || report.Totals.EstimatedCostUSD != nil {
		t.Fatalf("missing usage became a number: %+v", report)
	}
	if report.Sessions[0].DurationSeconds == nil || *report.Sessions[0].DurationSeconds != 60 || report.Totals.DurationState != "known" {
		t.Fatalf("duration still comes from the session: %+v", report.Sessions[0])
	}
}

func TestTicketAgentWorkDepthCapIsVisible(t *testing.T) {
	f := newWorkFixture(t)
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	root := uid()
	f.node(t, root, "epic", "TW1-2", "Epic", f.project)
	f.bindGuest(t, f.project)
	parent := root
	for depth := 1; depth <= maxDepth; depth++ {
		child := uid()
		f.node(t, child, "task", fmt.Sprintf("TW1-%d", depth+2), "Child", parent)
		parent = child
	}
	deep := uid()
	f.node(t, deep, "task", "TW1-20", "Beyond cap", parent)
	f.session(t, uid(), f.project, deep, "codex", "gpt-test", "high", time.Now().Add(-time.Minute), time.Time{}, time.Now(), "working")
	report := f.get(t, f.guest, root, "")
	if !report.ScopeTruncated || report.Totals.SessionCount != 0 || len(report.Sessions) != 0 {
		t.Fatalf("visible depth-nine work must be disclosed as truncated: %+v", report)
	}
	// The same depth-nine child is invisible to this caller. Its existence
	// must not leak through scope_truncated.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET kind_id=k.id FROM node_kinds k
			WHERE nodes.tenant_id=$1 AND nodes.id=$2 AND k.tenant_id=$1 AND k.slug='project'`, f.person.TenantID, deep)
		return err
	})
	report = f.get(t, f.guest, root, "")
	if report.ScopeTruncated || report.Totals.SessionCount != 0 {
		t.Fatalf("hidden depth-nine child leaked: %+v", report)
	}
}

func TestTicketAgentWorkRealUsageModelLength(t *testing.T) {
	f := newWorkFixture(t)
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	ticket, sid := uid(), uid()
	f.node(t, ticket, "ticket", "TW1-2", "Ticket", f.project)
	past := time.Date(2020, 1, 4, 0, 0, 0, 0, time.UTC)
	f.session(t, sid, f.project, ticket, "codex", "short-model", "high", past, past.Add(time.Minute), past, "stopped")
	model := strings.Repeat("m", 128)
	f.usage(t, f.person, sid, model, "12", "3", "1", "0.010000000000", false, "1", "api", "")
	report := f.get(t, f.person, ticket, "")
	if len(report.Sessions) != 1 || len(report.Sessions[0].Models) != 1 || report.Sessions[0].Models[0].Model != model || str(report.Totals.InputTokens) != "12" || str(report.Totals.EstimatedCostUSD) != "0.010000000000" {
		t.Fatalf("real US1 model length or price FK was lost: %+v", report)
	}
}

func TestTicketAgentWorkProjectGrantAndAgentScope(t *testing.T) {
	f := newWorkFixture(t)
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	ticket := uid()
	f.node(t, ticket, "ticket", "TW1-2", "Ticket", f.project)
	f.bindGuest(t, f.project)
	const route = "GET /api/nodes/{nodeId}/agent-work"
	for _, p := range []tenant.Principal{f.guest, f.agent} {
		if p.Kind == tenant.Agent {
			if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
				SELECT $1,$2,r.id,'project',$3 FROM roles r WHERE r.tenant_id=$1 AND r.key='guest'`, p.TenantID, p.ID, f.project); err != nil {
				t.Fatal(err)
			}
		}
		ctx := authz.BindPool(tenant.WithPrincipal(t.Context(), p), f.db.App)
		if err := authz.RequirePattern(ctx, route, authz.Scope{}); err == nil {
			t.Fatalf("%s was allowed without a project scope", p.Kind)
		}
		scope, ok, err := authz.ResolveRouteScope(ctx, f.db.App, route, "/api/nodes/"+ticket+"/agent-work")
		if err != nil || !ok || scope.ProjectID != f.project {
			t.Fatalf("project resolution for %s: %+v %v %v", p.Kind, scope, ok, err)
		}
		if p.Kind == tenant.Agent {
			if err := authz.RequirePattern(ctx, route, scope); err == nil {
				t.Fatal("unscoped agent key was allowed")
			}
			p.Scopes = []string{"nodes.read"}
			ctx = authz.BindPool(tenant.WithPrincipal(t.Context(), p), f.db.App)
		}
		if err := authz.RequirePattern(ctx, route, scope); err != nil {
			t.Fatalf("project-scoped %s should be allowed: %v", p.Kind, err)
		}
		if f.status(t, p, ticket, "") != http.StatusOK {
			t.Fatalf("project-scoped %s could not load its ticket", p.Kind)
		}
	}
}

func TestTicketAgentWorkThroughAuthMiddleware(t *testing.T) {
	f := newWorkFixture(t)
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	ticket := uid()
	f.node(t, ticket, "ticket", "TW1-2", "Ticket", f.project)
	_, _, emptyKey, err := auth.OperatorCreateAgentKey(t.Context(), f.db.App, f.person.TenantID, "tw1-empty", f.agent.ID, []string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, readKey, err := auth.OperatorCreateAgentKey(t.Context(), f.db.App, f.person.TenantID, "tw1-reader", f.agent.ID, []string{"nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	module, err := auth.New(auth.Config{Env: "dev", SessionKey: make([]byte, 32)}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	server := &httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{New(f.db.App)}, Middleware: []func(http.Handler) http.Handler{module.Middleware}}
	request := func(key string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/nodes/"+ticket+"/agent-work", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if status := request(emptyKey); status != http.StatusForbidden {
		t.Fatalf("empty key status = %d, want 403", status)
	}
	if status := request(readKey); status != http.StatusOK {
		t.Fatalf("nodes.read key status = %d, want 200", status)
	}
}

func TestTicketAgentWorkSkipsRunTelemetry(t *testing.T) {
	f := newWorkFixture(t)
	ticket, order := uid(), uid()
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	f.node(t, ticket, "ticket", "TW1-2", "Ticket", f.project)
	f.node(t, order, "work_order", "TW1-9", "Order", f.project)
	run, sid := uid(), uid()
	past := time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.person.TenantID, order, f.person.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,status,input_tokens,output_tokens,cost_micros,started_at,ended_at)
			VALUES($1,$2,$3,$4,'completed',999999,999999,888888,$5,$5)`, f.person.TenantID, run, order, f.agent.ID, past); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,input_tokens_delta,output_tokens_delta,cost_micros_delta)
			VALUES($1,$2,1,'usage',777777,777777,666666)`, f.person.TenantID, run)
		return err
	})
	f.managedSession(t, sid, f.project, ticket, run, past)
	f.usage(t, f.person, sid, "gpt-test", "12", "3", "1", "0.010000000000", false, "1", "api", "")
	raw := f.body(t, f.person, ticket, "")
	if regexp.MustCompile(`999999|888888|777777|666666`).Match(raw) {
		t.Fatalf("managed run telemetry was added to session usage: %s", raw)
	}
	report := f.get(t, f.person, ticket, "")
	if report.Totals.SessionCount != 1 || str(report.Totals.InputTokens) != "12" || str(report.Totals.OutputTokens) != "3" || str(report.Totals.EstimatedCostUSD) != "0.010000000000" || report.Totals.DurationSeconds == nil || *report.Totals.DurationSeconds != 30 {
		t.Fatalf("usage-only totals %+v", report.Totals)
	}
}

var numberField = regexp.MustCompile(`"(?:input_tokens|output_tokens|cached_input_tokens|estimated_cost_usd|price_version)":\s*-?\d`)

type workFixture struct {
	db                   *dbtest.DB
	mux                  *http.ServeMux
	person, guest, agent tenant.Principal
	foreign              tenant.Principal
	project              string
}

func newWorkFixture(t *testing.T) *workFixture {
	t.Helper()
	f := &workFixture{db: dbtest.Open(t), mux: http.NewServeMux(), project: uid()}
	f.person = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	f.guest = tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.agent = tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Agent}
	f.foreign = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.person, f.foreign} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'TW1')`, p.TenantID, "tw1-"+p.TenantID[:8])
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.person, f.guest, f.agent, f.foreign} {
		f.tx(t, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,'worker')`, p.TenantID, p.ID, p.Kind)
			return err
		})
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	dbtest.BindRole(t, f.db, f.foreign.TenantID, f.foreign.ID, "admin")
	New(f.db.App).Mount(f.mux)
	return f
}

func (f *workFixture) tx(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := f.txErr(t, p, fn); err != nil {
		t.Fatal(err)
	}
}

func (f *workFixture) txErr(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) error {
	t.Helper()
	return db.InTenant(dbtest.Seed(t.Context()), f.db.App, p.TenantID, fn)
}

func (f *workFixture) node(t *testing.T, id, kind, key, title, parent string) {
	t.Helper()
	f.nodeFor(t, f.person, id, kind, key, title, parent)
}

func (f *workFixture) nodeFor(t *testing.T, p tenant.Principal, id, kind, key, title, parent string) {
	t.Helper()
	var parentArg any
	if parent != "" {
		parentArg = parent
	}
	f.tx(t, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id)
			SELECT $1,$2,$3,k.id,$4,$5 FROM node_kinds k WHERE k.slug=$6`, p.TenantID, id, key, title, parentArg, kind)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("node %s was not inserted", key)
		}
		return nil
	})
}

func (f *workFixture) bindGuest(t *testing.T, project string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		SELECT $1,$2,r.id,'project',$3 FROM roles r WHERE r.tenant_id=$1 AND r.key='guest'`, f.person.TenantID, f.guest.ID, project); err != nil {
		t.Fatal(err)
	}
}

func (f *workFixture) session(t *testing.T, id, project, ticket, harness, model, effort string, created, stopped, heartbeat time.Time, phase string) {
	t.Helper()
	f.sessionFor(t, f.person, id, project, ticket, harness, model, effort, created, stopped, heartbeat, phase)
}

func (f *workFixture) sessionFor(t *testing.T, p tenant.Principal, id, project, ticket, harness, model, effort string, created, stopped, heartbeat time.Time, phase string) {
	t.Helper()
	f.insertSession(t, p, id, project, ticket, harness, model, effort, created, stopped, heartbeat, phase, "unmanaged", "")
}

func (f *workFixture) managedSession(t *testing.T, id, project, ticket, run string, created time.Time) {
	t.Helper()
	stopped := created.Add(30 * time.Second)
	f.insertSession(t, f.person, id, project, ticket, "codex", "gpt-test", "low", created, stopped, created, "stopped", "managed", run)
}

func (f *workFixture) insertSession(t *testing.T, p tenant.Principal, id, project, ticket, harness, model, effort string, created, stopped, heartbeat time.Time, phase, management, run string) {
	t.Helper()
	workShape := "unknown"
	var ticketArg, stoppedArg, runArg any
	if ticket != "" {
		workShape = "ship"
		ticketArg = ticket
	}
	if !stopped.IsZero() {
		stoppedArg = stopped
	}
	if run != "" {
		runArg = run
	}
	var modelArg, effortArg any
	if model != "" {
		modelArg = model
	}
	if effort != "" {
		effortArg = effort
	}
	agent := f.agent.ID
	if p.TenantID == f.foreign.TenantID {
		agent = f.foreign.ID
	}
	f.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,ticket_node_id,run_id,harness,host,management,role,work_shape,ref_digest,lease_digest,model,reasoning_effort,display_label,phase,created_at,stopped_at,heartbeat_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,'test-host',$8,'worker',$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			p.TenantID, id, project, agent, ticketArg, runArg, harness, management, workShape, digest(id+"ref"), digest(id+"lease"), modelArg, effortArg, harness+" worker", phase, created, stoppedArg, heartbeat)
		return err
	})
}

func (f *workFixture) usage(t *testing.T, p tenant.Principal, session, model, in, out, cached, cost string, provisional bool, price, billing, sub string) {
	t.Helper()
	if err := f.usageErr(t, p, session, model, in, out, cached, cost, provisional, price, billing, sub); err != nil {
		t.Fatal(err)
	}
}

func (f *workFixture) usageErr(t *testing.T, p tenant.Principal, session, model, in, out, cached, cost string, provisional bool, price, billing, sub string) error {
	t.Helper()
	return f.txErr(t, p, func(tx pgx.Tx) error {
		if price != "" {
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million)
				VALUES($1,$2,$3::bigint,1,1,1) ON CONFLICT (tenant_id, model, version) DO NOTHING`, p.TenantID, model, price); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage(
			tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,price_version,estimated_cost_usd,billing_mode,subscription_label)
			VALUES($1,$2,$3,1,NULLIF($4,'')::bigint,NULLIF($5,'')::bigint,NULLIF($6,'')::bigint,$7,NULLIF($8,'')::bigint,NULLIF($9,'')::numeric,$10,NULLIF($11,''))`,
			p.TenantID, session, model, in, out, cached, provisional, price, cost, billing, sub)
		return err
	})
}

func (f *workFixture) get(t *testing.T, p tenant.Principal, nodeID, limit string) Report {
	t.Helper()
	w := f.call(p, nodeID, limit)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var report Report
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func (f *workFixture) body(t *testing.T, p tenant.Principal, nodeID, limit string) []byte {
	t.Helper()
	w := f.call(p, nodeID, limit)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func (f *workFixture) status(t *testing.T, p tenant.Principal, nodeID, limit string) int {
	t.Helper()
	return f.call(p, nodeID, limit).Code
}

func (f *workFixture) call(p tenant.Principal, nodeID, limit string) *httptest.ResponseRecorder {
	path := "/api/nodes/" + nodeID + "/agent-work"
	if limit != "" {
		path += "?limit=" + limit
	}
	r := httptest.NewRequest(http.MethodGet, path, bytes.NewReader(nil)).WithContext(tenant.WithPrincipal(context.Background(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func uid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
