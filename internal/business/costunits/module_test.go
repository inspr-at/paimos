// SPDX-License-Identifier: AGPL-3.0-only

package costunits

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

const classicFields = `{"acceptance_criteria":"- [ ] billed","notes":"imported","priority":"normal","tags":["studio"],"estimate_hours":2.5,"estimate_lp":3,"budget_hours":8,"total_budget":1000,"start_date":"2026-01-02","end_date":"2026-01-09","release":"r1","sprint_ids":[99],"needs_review":false,"archived":false,"accepted_at":"2026-01-10T10:00:00Z","classic":{"source_id":"ppm","id":101,"issue_key":"PAI-101","type":"cost_unit","billing_code":"CU-INTERNAL"}}`

func TestCostUnitRates(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantA := insertTenant(t, database, "costs-a")
	tenantB := insertTenant(t, database, "costs-b")
	admin := insertPrincipal(t, database, tenantA, tenant.Person, "Ada", []string{"admin"})
	member := insertPrincipal(t, database, tenantA, tenant.Person, "Mae", nil)
	agent := insertPrincipal(t, database, tenantA, tenant.Agent, "Agent", []string{"admin"})
	other := insertPrincipal(t, database, tenantB, tenant.Person, "Bea", []string{"admin"})
	mux, _, plug := mount(t, database.App)
	nodeA := insertCostUnit(t, database, tenantA, "PAI-101", "Studio delivery", classicFields)
	nodeB := insertCostUnit(t, database, tenantA, "CU-7", "Support", `{"classic":{"source_id":"ppm","id":7,"type":"cost_unit"}}`)
	if key(t, database, nodeA) != "PAI-101" || key(t, database, nodeB) != "CU-7" {
		t.Fatal("imported keys changed before rating")
	}

	all := []string{fence.PermNodesContribute, fence.PermViewsProvide, fence.PermStepsApply}
	if status, body := putInstall(mux, admin, plug.Manifest.DigestSHA256, true, all); status != http.StatusOK {
		t.Fatalf("install %d %s", status, body)
	}
	if status, body := putInstall(mux, other, plug.Manifest.DigestSHA256, true, all); status != http.StatusOK {
		t.Fatalf("install B %d %s", status, body)
	}

	if status, _ := do(mux, nil, http.MethodGet, ratesPath(nodeA), ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous %d", status)
	}
	if status, _ := do(mux, &member, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "10", "12", "2026-01-01", "")); status != http.StatusForbidden {
		t.Fatal("member wrote")
	}
	if status, _ := do(mux, &agent, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "10", "12", "2026-01-01", "")); status != http.StatusForbidden {
		t.Fatal("agent wrote")
	}
	if countRates(t, database, tenantA) != 0 || countEvents(t, database, tenantA, eventCreated) != 0 {
		t.Fatal("rejected caller wrote a rate")
	}
	if status, _ := do(mux, &admin, http.MethodPost, "/api/cost-units/not-a-uuid/rates", rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusBadRequest {
		t.Fatal("bad id")
	}
	if status, _ := do(mux, &admin, http.MethodPost, ratesPath(nodeA), `{"unit":"hour"}`); status != http.StatusBadRequest {
		t.Fatal("short body")
	}
	if status, _ := do(mux, &admin, http.MethodPatch, ratesPath(nodeA), ""); status != http.StatusMethodNotAllowed {
		t.Fatal("patch allowed")
	}

	status, body := do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "125.50", "180", "2026-01-01", ""))
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	first := decodeRate(t, body)
	if first.Internal.String() != "125.5" || first.Bill.String() != "180" || first.Until != nil || first.Node != nodeA {
		t.Fatalf("rate %#v", first)
	}
	if key(t, database, nodeA) != "PAI-101" {
		t.Fatal("rating rekeyed the imported cost unit")
	}
	status, body = do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "125.5", "180.0", "2026-01-01", ""))
	replay := decodeRate(t, body)
	if status != http.StatusCreated || replay.ID != first.ID || countEvents(t, database, tenantA, eventCreated) != 1 {
		t.Fatalf("replay %d %s events %d", status, body, countEvents(t, database, tenantA, eventCreated))
	}
	if status, _ := do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "125.5", "190", "2026-01-01", "")); status != http.StatusConflict || countEvents(t, database, tenantA, eventCreated) != 1 {
		t.Fatal("amount rewrite")
	}
	if got := amountText(t, database, first.ID); got != "125.5000 180.0000" && got != "125.5 180" && !strings.HasPrefix(got, "125.5") {
		t.Fatalf("stored amount %s", got)
	}
	if !strings.Contains(amountText(t, database, first.ID), "180") {
		t.Fatalf("bill changed: %s", amountText(t, database, first.ID))
	}

	for _, tc := range []struct{ unit, currency, internal, bill, from, until string }{
		{"hour", "USD", "90", "140", "2026-01-01", ""},
		{"day", "EUR", "800", "1200", "2026-01-01", ""},
	} {
		if status, body = do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody(tc.unit, tc.currency, tc.internal, tc.bill, tc.from, tc.until)); status != http.StatusCreated {
			t.Fatalf("independent rate %s %s: %d %s", tc.unit, tc.currency, status, body)
		}
	}
	status, body = do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "130", "190", "2026-06-01", ""))
	if status != http.StatusCreated {
		t.Fatalf("next price %d %s", status, body)
	}
	if countEvents(t, database, tenantA, eventClosed) != 1 || countEvents(t, database, tenantA, eventCreated) != 4 {
		t.Fatalf("events created %d closed %d", countEvents(t, database, tenantA, eventCreated), countEvents(t, database, tenantA, eventClosed))
	}
	var closedUntil, closedInternal, closedBill string
	if err := database.Admin.QueryRow(ctx, `SELECT coalesce(after->>'effective_until',''), before->>'internal_amount', after->>'internal_amount' FROM events WHERE tenant_id=$1::uuid AND type=$2`, tenantA, eventClosed).Scan(&closedUntil, &closedInternal, &closedBill); err != nil {
		t.Fatal(err)
	}
	if closedUntil != "2026-06-01" || closedInternal != closedBill || closedInternal != "125.5" {
		t.Fatalf("close event until %s amounts %s %s", closedUntil, closedInternal, closedBill)
	}
	listStatus, listBody := do(mux, &admin, http.MethodGet, ratesPath(nodeA), "")
	listed := decodeRates(t, mustStatus(t, listStatus, listBody, http.StatusOK))
	jan := findRate(t, listed, "hour", "EUR", "2026-01-01")
	if jan.Until == nil || *jan.Until != "2026-06-01" || jan.Internal.String() != "125.5" {
		t.Fatalf("closed rate %#v", jan)
	}
	if findRate(t, listed, "hour", "USD", "2026-01-01").Until != nil || findRate(t, listed, "hour", "EUR", "2026-06-01").Until != nil {
		t.Fatal("unrelated or new interval was closed")
	}
	status, body = do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "125.50", "180", "2026-01-01", ""))
	if status != http.StatusCreated || decodeRate(t, body).ID != first.ID || countEvents(t, database, tenantA, eventCreated) != 4 {
		t.Fatal("retry after close wrote another event")
	}
	if status, _ = do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "1", "1", "2026-03-01", "")); status != http.StatusConflict {
		t.Fatal("overlap accepted")
	}
	for _, tc := range []struct{ internal, bill, from, until string }{
		{"10", "12", "2025-01-01", "2025-06-01"},
		{"0", "0", "2024-01-01", "2024-06-01"},
		{"0.0001", "0.0001", "2023-01-01", "2023-02-01"},
		{"1e-4", "2e-4", "2023-03-01", "2023-04-01"},
	} {
		status, body = do(mux, &admin, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", tc.internal, tc.bill, tc.from, tc.until))
		if status != http.StatusCreated {
			t.Fatalf("edge %s %d %s", tc.internal, status, body)
		}
	}
	tiny := decodeRate(t, body)
	if tiny.Internal.String() != "0.0001" || tiny.Bill.String() != "0.0002" {
		t.Fatalf("exponent normalized to %s %s", tiny.Internal, tiny.Bill)
	}
	for _, raw := range []string{
		rateBody("hour", "EUR", "0.00001", "1", "2022-01-01", "2022-02-01"),
		rateBody("hour", "EUR", "-1", "1", "2022-01-01", "2022-02-01"),
		rateBody("week", "EUR", "1", "1", "2022-01-01", ""),
		rateBody("hour", "eur", "1", "1", "2022-01-01", ""),
		rateBody("hour", "EUR", "1", "1", "2022-02-01", "2022-02-01"),
		rateBody("hour", "EUR", "1", "1", "2022-02-01", "2022-01-01"),
	} {
		if status, body = do(mux, &admin, http.MethodPost, ratesPath(nodeA), raw); status != http.StatusBadRequest {
			t.Fatalf("invalid %s -> %d %s", raw, status, body)
		}
	}

	err := db.InTenant(dbtest.Seed(ctx), database.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE cost_unit_rates SET bill_amount = bill_amount + 1 WHERE id = $1::uuid`, first.ID)
		return err
	})
	if err == nil {
		t.Fatal("historical amount changed")
	}
	err = db.InTenant(dbtest.Seed(ctx), database.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM cost_unit_rates WHERE id = $1::uuid`, first.ID)
		return err
	})
	if err == nil {
		t.Fatal("historical rate deleted")
	}
	err = db.InTenant(dbtest.Seed(ctx), database.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE cost_unit_rates SET effective_until = effective_until + 1 WHERE id = $1::uuid AND effective_until IS NOT NULL`, first.ID)
		return err
	})
	if err == nil {
		t.Fatal("closed interval extended")
	}

	if status, _ = do(mux, &other, http.MethodGet, ratesPath(nodeA), ""); status != http.StatusNotFound {
		t.Fatalf("other tenant get %d", status)
	}
	if status, _ = do(mux, &other, http.MethodPost, ratesPath(nodeA), rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusNotFound {
		t.Fatal("other tenant wrote")
	}
	var visible int
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM cost_unit_rates`).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Fatalf("tenant B sees %d (%v)", visible, err)
	}
	if err := database.App.QueryRow(ctx, `SELECT count(*) FROM cost_unit_rates`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("unset tenant sees %d (%v)", visible, err)
	}
	if countRates(t, database, tenantA) == 0 {
		t.Fatal("admin lost the rates")
	}

	if _, err := database.Admin.Exec(ctx, `UPDATE nodes SET deleted_at = now() WHERE id = $1::uuid`, nodeB); err != nil {
		t.Fatal(err)
	}
	if status, _ = do(mux, &admin, http.MethodGet, ratesPath(nodeB), ""); status != http.StatusNotFound {
		t.Fatal("deleted cost unit listed")
	}
	if status, _ = do(mux, &admin, http.MethodPost, ratesPath(nodeB), rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusNotFound {
		t.Fatal("deleted cost unit rated")
	}
	var ticketKind string
	if err := database.Admin.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE tenant_id=$1::uuid AND slug='work'`, tenantA).Scan(&ticketKind); err != nil {
		t.Fatal(err)
	}
	var ticket string
	if err := database.Admin.QueryRow(ctx, `INSERT INTO nodes(tenant_id, kind_id, key, title) VALUES($1::uuid,$2::uuid,'TKT-9','Not a cost') RETURNING id::text`, tenantA, ticketKind).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	if status, _ = do(mux, &admin, http.MethodPost, ratesPath(ticket), rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusNotFound {
		t.Fatal("ticket accepted a rate")
	}

	fresh := insertCostUnit(t, database, tenantA, "PAI-303", "Concurrent", `{"classic":{"id":303,"type":"cost_unit"}}`)
	bodyText := rateBody("item", "CHF", "3.25", "4", "2026-08-01", "")
	var wg sync.WaitGroup
	codes := make([]int, 2)
	payloads := make([][]byte, 2)
	ready := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			codes[i], payloads[i] = do(mux, &admin, http.MethodPost, ratesPath(fresh), bodyText)
		}(i)
	}
	close(ready)
	wg.Wait()
	if codes[0] != http.StatusCreated || codes[1] != http.StatusCreated {
		t.Fatalf("concurrent %d %s / %d %s", codes[0], payloads[0], codes[1], payloads[1])
	}
	if decodeRate(t, payloads[0]).ID != decodeRate(t, payloads[1]).ID {
		t.Fatal("concurrent retry created two rates")
	}
	if countEvents(t, database, tenantA, eventCreated) != 9 {
		t.Fatalf("created events %d", countEvents(t, database, tenantA, eventCreated))
	}
}

func TestRateWriteRollsBackWithTheEvent(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID := insertTenant(t, database, "costs-atomic")
	admin := insertPrincipal(t, database, tenantID, tenant.Person, "Ada", []string{"admin"})
	mux, mod, plug := mount(t, database.App)
	nodeID := insertCostUnit(t, database, tenantID, "PAI-1", "Atomic", classicFields)
	if status, body := putInstall(mux, admin, plug.Manifest.DigestSHA256, true, []string{fence.PermNodesContribute, fence.PermViewsProvide, fence.PermStepsApply}); status != http.StatusOK {
		t.Fatalf("install %d %s", status, body)
	}
	if status, body := do(mux, &admin, http.MethodPost, ratesPath(nodeID), rateBody("hour", "EUR", "10", "12", "2026-01-01", "")); status != http.StatusCreated {
		t.Fatalf("seed %d %s", status, body)
	}
	mod.appendEvent = func(context.Context, pgx.Tx, tenant.Principal, events.Change) (events.Event, error) {
		return events.Event{}, errors.New("append failed")
	}
	status, body := do(mux, &admin, http.MethodPost, ratesPath(nodeID), rateBody("hour", "EUR", "11", "13", "2026-07-01", ""))
	if status != http.StatusInternalServerError {
		t.Fatalf("failed append %d %s", status, body)
	}
	var until *string
	if err := database.Admin.QueryRow(ctx, `SELECT to_char(effective_until, 'YYYY-MM-DD') FROM cost_unit_rates WHERE cost_unit_node_id=$1::uuid`, nodeID).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if until != nil || countRates(t, database, tenantID) != 1 || countEvents(t, database, tenantID, eventCreated) != 1 || countEvents(t, database, tenantID, eventClosed) != 0 {
		t.Fatalf("rollback until %v rates %d", until, countRates(t, database, tenantID))
	}
	other := insertCostUnit(t, database, tenantID, "PAI-2", "Other", `{}`)
	if status, _ = do(mux, &admin, http.MethodPost, ratesPath(other), rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusInternalServerError || countRates(t, database, tenantID) != 1 {
		t.Fatal("failed first write persisted")
	}
}

func TestInstallationGates(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID := insertTenant(t, database, "costs-gates")
	admin := insertPrincipal(t, database, tenantID, tenant.Person, "Ada", []string{"admin"})
	mux, _, plug := mount(t, database.App)
	nodeID := insertCostUnit(t, database, tenantID, "PAI-9", "Gated", classicFields)
	if status, _ := do(mux, &admin, http.MethodGet, ratesPath(nodeID), ""); status != http.StatusConflict {
		t.Fatalf("uninstalled get %d", status)
	}
	if status, _ := do(mux, &admin, http.MethodPost, ratesPath(nodeID), rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusConflict || countRates(t, database, tenantID) != 0 {
		t.Fatal("uninstalled post wrote")
	}
	if status, body := putInstall(mux, admin, plug.Manifest.DigestSHA256, false, []string{fence.PermViewsProvide, fence.PermStepsApply}); status != http.StatusOK {
		t.Fatalf("disable %d %s", status, body)
	}
	if status, _ := do(mux, &admin, http.MethodGet, ratesPath(nodeID), ""); status != http.StatusConflict {
		t.Fatal("disabled get")
	}
	if status, body := putInstall(mux, admin, plug.Manifest.DigestSHA256, true, []string{fence.PermViewsProvide}); status != http.StatusOK {
		t.Fatalf("view grant %d %s", status, body)
	}
	if status, _ := do(mux, &admin, http.MethodGet, ratesPath(nodeID), ""); status != http.StatusOK {
		t.Fatal("view grant cannot read")
	}
	if status, _ := do(mux, &admin, http.MethodPost, ratesPath(nodeID), rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusForbidden || countRates(t, database, tenantID) != 0 {
		t.Fatal("view grant wrote")
	}
	if status, body := putInstall(mux, admin, plug.Manifest.DigestSHA256, true, []string{fence.PermStepsApply}); status != http.StatusOK {
		t.Fatalf("step grant %d %s", status, body)
	}
	if status, _ := do(mux, &admin, http.MethodGet, ratesPath(nodeID), ""); status != http.StatusForbidden {
		t.Fatal("step grant read")
	}
	if status, body := do(mux, &admin, http.MethodPost, ratesPath(nodeID), rateBody("hour", "EUR", "1", "1", "2026-01-01", "")); status != http.StatusCreated {
		t.Fatalf("step grant write %d %s", status, body)
	}
	if _, err := database.Admin.Exec(ctx, `UPDATE plugin_installations SET manifest_digest_sha256=$2 WHERE tenant_id=$1::uuid AND plugin_id=$3`, tenantID, strings.Repeat("ab", 32), PluginID); err != nil {
		t.Fatal(err)
	}
	if status, _ := do(mux, &admin, http.MethodGet, ratesPath(nodeID), ""); status != http.StatusConflict {
		t.Fatal("digest mismatch read")
	}
	if status, _ := do(mux, &admin, http.MethodPost, ratesPath(nodeID), rateBody("hour", "EUR", "2", "2", "2026-05-01", "")); status != http.StatusConflict {
		t.Fatal("digest mismatch wrote")
	}

	if status, body := putInstall(mux, admin, plug.Manifest.DigestSHA256, true, []string{fence.PermNodesContribute, fence.PermViewsProvide, fence.PermStepsApply}); status != http.StatusOK {
		t.Fatalf("restore %d %s", status, body)
	}
	host := plugins.NewWithRegistry(database.App, sealed(t, plug))
	facts := Facts{Operation: StepKey, CostUnitNodeID: nodeID, Live: true, Unit: "hour", Currency: "EUR", InternalAmount: "10", BillAmount: "12.5", EffectiveFrom: "2026-01-01", PersonAdmin: true}
	if _, err := host.Evaluate(ctx, admin, PluginID, plugins.StepRequest{Operation: StepKey, Payload: facts}); !errors.Is(err, plugins.ErrDenied) {
		t.Fatalf("evaluate %v", err)
	}
	if _, err := host.Request(ctx, admin, PluginID, plugins.StepRequest{Operation: StepKey, Payload: facts}); !errors.Is(err, plugins.ErrDenied) {
		t.Fatalf("request %v", err)
	}
	refused := facts
	refused.PersonAdmin = false
	decision, err := host.ApplyResult(ctx, admin, PluginID, plugins.StepResult{StepRequest: plugins.StepRequest{Operation: StepKey, Payload: refused}, ClaimedOutcome: OutcomeRecorded})
	if err != nil || decision.Proceed || decision.AdvancesRelease || decision.AdvancesAccess || decision.Blocker != fence.BlockerPolicyRefused {
		t.Fatalf("person gate %#v %v", decision, err)
	}
	refused = facts
	refused.Live = false
	decision, err = host.ApplyResult(ctx, admin, PluginID, plugins.StepResult{StepRequest: plugins.StepRequest{Operation: StepKey, Payload: refused}, ClaimedOutcome: OutcomeRecorded})
	if err != nil || decision.Proceed {
		t.Fatalf("live gate %#v %v", decision, err)
	}
	refused = facts
	refused.Overlap = true
	decision, err = host.ApplyResult(ctx, admin, PluginID, plugins.StepResult{StepRequest: plugins.StepRequest{Operation: StepKey, Payload: refused}, ClaimedOutcome: OutcomeRecorded})
	if err != nil || decision.Proceed {
		t.Fatalf("overlap gate %#v %v", decision, err)
	}
	decision, err = host.ApplyResult(ctx, admin, PluginID, plugins.StepResult{StepRequest: plugins.StepRequest{Operation: StepKey, Payload: facts}, ClaimedOutcome: OutcomeRecorded})
	if err != nil || !decision.Proceed || decision.Outcome != OutcomeRecorded || decision.AdvancesRelease || decision.AdvancesAccess || decision.Blocker != "" {
		t.Fatalf("admit %#v %v", decision, err)
	}
	if _, err = host.ApplyResult(ctx, admin, PluginID, plugins.StepResult{StepRequest: plugins.StepRequest{Operation: StepKey, Payload: "nope"}, ClaimedOutcome: OutcomeRecorded}); err == nil {
		t.Fatal("malformed facts accepted")
	}
}

func TestFieldSchemaAcceptsClassicCostUnit(t *testing.T) {
	database := dbtest.Open(t)
	tenantID := insertTenant(t, database, "costs-schema")
	admin := insertPrincipal(t, database, tenantID, tenant.Person, "Ada", []string{"admin"})
	plug, err := Plugin()
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	mux := http.NewServeMux()
	nodes.New(database.App, nil).Mount(mux)
	plugins.NewWithRegistry(database.App, reg).Mount(mux)
	mod := New(database.App, reg)
	mod.Mount(mux)
	kindBody, err := json.Marshal(struct {
		Slug        string          `json:"slug"`
		Label       string          `json:"label"`
		ShortPrefix string          `json:"short_prefix"`
		Icon        string          `json:"icon"`
		Allowed     json.RawMessage `json:"allowed_child_kinds"`
		Schema      json.RawMessage `json:"field_schema"`
	}{Slug: "cost_unit", Label: "Cost unit", ShortPrefix: "CU", Icon: "cost_unit", Allowed: json.RawMessage(`[]`), Schema: plug.Manifest.NodeKinds[0].FieldSchema})
	if err != nil {
		t.Fatal(err)
	}
	status, body := do(mux, &admin, http.MethodPost, "/api/kinds", string(kindBody))
	if status != http.StatusCreated {
		t.Fatalf("kind %d %s", status, body)
	}
	var kind struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &kind); err != nil {
		t.Fatal(err)
	}
	status, body = do(mux, &admin, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"key":"PAI-101","title":"Studio delivery","fields":%s}`, kind.ID, classicFields))
	if status != http.StatusCreated {
		t.Fatalf("classic node %d %s", status, body)
	}
	var node struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &node); err != nil {
		t.Fatal(err)
	}
	if node.Key != "PAI-101" {
		t.Fatalf("key %s", node.Key)
	}
	if status, body = putInstall(mux, admin, plug.Manifest.DigestSHA256, true, []string{fence.PermNodesContribute, fence.PermViewsProvide, fence.PermStepsApply}); status != http.StatusOK {
		t.Fatalf("install %d %s", status, body)
	}
	if status, body = do(mux, &admin, http.MethodPost, ratesPath(node.ID), rateBody("hour", "EUR", "125.50", "180", "2026-01-01", "")); status != http.StatusCreated {
		t.Fatalf("rate on schema-backed classic node %d %s", status, body)
	}
	if _, err := hostKinds(t, database.App, reg, admin); err != nil {
		t.Fatal(err)
	}
}

func hostKinds(t *testing.T, pool *pgxpool.Pool, reg *plugins.Registry, admin tenant.Principal) ([]plugins.NodeKind, error) {
	t.Helper()
	return plugins.NewWithRegistry(pool, reg).NodeKinds(t.Context(), admin, PluginID)
}

func mount(t *testing.T, pool *pgxpool.Pool) (*http.ServeMux, *Module, plugins.Plugin) {
	t.Helper()
	plug, err := Plugin()
	if err != nil {
		t.Fatal(err)
	}
	reg := sealed(t, plug)
	mux := http.NewServeMux()
	plugins.NewWithRegistry(pool, reg).Mount(mux)
	mod := New(pool, reg)
	mod.Mount(mux)
	return mux, mod, plug
}

func sealed(t *testing.T, plug plugins.Plugin) *plugins.Registry {
	t.Helper()
	reg := plugins.NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	return reg
}

func insertTenant(t *testing.T, database *dbtest.DB, slug string) string {
	t.Helper()
	var id string
	if err := database.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug, name) VALUES($1, $1) RETURNING id::text`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertPrincipal(t *testing.T, database *dbtest.DB, tenantID string, kind tenant.PrincipalKind, name string, roles []string) tenant.Principal {
	t.Helper()
	if roles == nil {
		roles = []string{}
	}
	p := tenant.Principal{TenantID: tenantID, Kind: kind, Name: name, Roles: roles}
	if err := database.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id, kind, name, roles) VALUES($1::uuid, $2, $3, $4) RETURNING id::text`, tenantID, string(kind), name, roles).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, database, tenantID, p.ID)
	return p
}

func insertCostUnit(t *testing.T, database *dbtest.DB, tenantID, key, title, fields string) string {
	t.Helper()
	var kindID, nodeID string
	err := database.Admin.QueryRow(t.Context(), `INSERT INTO node_kinds(tenant_id, slug, label, short_prefix, icon) VALUES($1::uuid, 'cost_unit', 'cost unit', 'CU', 'cost_unit') ON CONFLICT(tenant_id, slug) DO UPDATE SET label=node_kinds.label RETURNING id::text`, tenantID).Scan(&kindID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id, kind_id, key, title, fields) VALUES($1::uuid, $2::uuid, $3, $4, $5::jsonb) RETURNING id::text`, tenantID, kindID, key, title, fields).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	return nodeID
}

func putInstall(mux *http.ServeMux, p tenant.Principal, digest string, enabled bool, perms []string) (int, []byte) {
	raw, err := json.Marshal(map[string]any{"manifest_digest_sha256": digest, "enabled": enabled, "permissions": perms})
	if err != nil {
		return 0, []byte(err.Error())
	}
	return do(mux, &p, http.MethodPut, "/api/plugins/"+PluginID+"/installation", string(raw))
}

func do(mux *http.ServeMux, p *tenant.Principal, method, path, body string) (int, []byte) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if p != nil {
		req = req.WithContext(tenant.WithPrincipal(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func ratesPath(id string) string { return "/api/cost-units/" + id + "/rates" }

func rateBody(unit, currency, internal, bill, from, until string) string {
	end := "null"
	if until != "" {
		end = `"` + until + `"`
	}
	return fmt.Sprintf(`{"unit":%q,"currency":%q,"internal_amount":%s,"bill_amount":%s,"effective_from":%q,"effective_until":%s}`, unit, currency, internal, bill, from, end)
}

type wireRate struct {
	ID       string      `json:"id"`
	Node     string      `json:"cost_unit_node_id"`
	Unit     string      `json:"unit"`
	Currency string      `json:"currency"`
	Internal json.Number `json:"internal_amount"`
	Bill     json.Number `json:"bill_amount"`
	From     string      `json:"effective_from"`
	Until    *string     `json:"effective_until"`
}

func decodeRate(t *testing.T, body []byte) wireRate {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var rate wireRate
	if err := dec.Decode(&rate); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return rate
}

func decodeRates(t *testing.T, body []byte) []wireRate {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var rates []wireRate
	if err := dec.Decode(&rates); err != nil {
		t.Fatalf("decode list %s: %v", body, err)
	}
	return rates
}

func findRate(t *testing.T, rates []wireRate, unit, currency, from string) wireRate {
	t.Helper()
	for _, rate := range rates {
		if rate.Unit == unit && rate.Currency == currency && rate.From == from {
			return rate
		}
	}
	t.Fatalf("missing %s %s %s in %d rates", unit, currency, from, len(rates))
	return wireRate{}
}

func mustStatus(t *testing.T, status int, body []byte, want int) []byte {
	t.Helper()
	if status != want {
		t.Fatalf("status %d %s", status, body)
	}
	return body
}

func countRates(t *testing.T, database *dbtest.DB, tenantID string) int {
	t.Helper()
	var n int
	if err := database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM cost_unit_rates WHERE tenant_id=$1::uuid`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countEvents(t *testing.T, database *dbtest.DB, tenantID, typ string) int {
	t.Helper()
	var n int
	if err := database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1::uuid AND type=$2`, tenantID, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func key(t *testing.T, database *dbtest.DB, nodeID string) string {
	t.Helper()
	var got string
	if err := database.Admin.QueryRow(t.Context(), `SELECT key FROM nodes WHERE id=$1::uuid`, nodeID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func amountText(t *testing.T, database *dbtest.DB, rateID string) string {
	t.Helper()
	var internal, bill string
	if err := database.Admin.QueryRow(t.Context(), `SELECT internal_amount::text, bill_amount::text FROM cost_unit_rates WHERE id=$1::uuid`, rateID).Scan(&internal, &bill); err != nil {
		t.Fatal(err)
	}
	return internal + " " + bill
}
