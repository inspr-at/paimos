// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type resetVendorFixture struct {
	result     ResetResult
	err        error
	calls      int
	undoCalls  int
	beforeCall func()
}

func (v *resetVendorFixture) Use(ctx context.Context, r ResetRequest) (ResetResult, error) {
	v.calls++
	if v.beforeCall != nil {
		v.beforeCall()
	}
	return v.result, v.err
}
func (v *resetVendorFixture) Undo(ctx context.Context, r ResetRequest) (ResetResult, error) {
	v.undoCalls++
	return v.result, v.err
}

type resetsFixture struct {
	owner, runner tenant.Principal
	account       Account
	module        *Module
	vendor        *resetVendorFixture
	token         string
	now           time.Time
	report        capacity.ResetReport
	window        capacity.Reading
}

func newResetsFixture(t *testing.T) resetsFixture {
	t.Helper()
	reset(t)
	f := resetsFixture{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	f.owner = makePrincipal(t, "resets", "person", "Owner", []string{"admin"})
	f.runner = addPrincipal(t, f.owner.TenantID, "agent", "Daemon", nil)
	codexProfile(t, f.owner)
	f.token = issueKey(t, f.runner, []string{"account.manage", "account.probe"})
	f.vendor = &resetVendorFixture{}
	f.module = NewWithResetVendor(appPool, f.vendor).(*Module)
	callStatus(t, f.module, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"reset-fixture","harness":"codex","daemon_id":"reset-daemon","label":"Resets"}`, 201, &f.account)
	ownFixtureAccount(t, f.owner, &f.account)
	yes := true
	f.window = capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 95, ResetsAt: f.now.Add(4 * 24 * time.Hour), ReadAt: f.now.Add(-time.Second), Source: "harness", OrdinaryUsageAllowed: &yes}
	f.report = capacity.ResetReport{ResetCredits: capacity.ResetCredits{Count: 2, ExpiresAt: []time.Time{f.now.Add(time.Hour), f.now.Add(24 * time.Hour)}, Source: "vendor"}, ReadAt: f.now.Add(-time.Second), BindingRevision: f.account.LinkRevision, UndoSupported: true}
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, f.now)
	if err := db.InTenant(ctx, appPool, f.owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET usage_probe_enabled=true,usage_probe_revision=link_revision,linked_at=$2::timestamptz-interval '1 hour',last_probe_at=$2,last_probe_ok=true WHERE id=$1`, f.account.ID, f.now); err != nil {
			return err
		}
		if err := ingestReadings(ctx, tx, f.runner, f.account.ID, []capacity.Reading{f.window}); err != nil {
			return err
		}
		a, err := getAccount(ctx, tx, f.account.ID)
		if err != nil {
			return err
		}
		return storeResetReport(ctx, tx, a, f.report, f.now)
	}); err != nil {
		t.Fatal(err)
	}
	resultReport := f.report
	resultReport.Count = 1
	resultReport.ExpiresAt = resultReport.ExpiresAt[1:]
	resultReport.ReadAt = f.now
	window := f.window
	window.UsedPercent = 0
	window.ReadAt = f.now
	until := f.now.Add(5 * time.Minute)
	f.vendor.result = ResetResult{Window: window, Report: resultReport, UndoUntil: &until}
	return f
}
func (f resetsFixture) path() string { return "/api/agent-accounts/" + f.account.ID }
func (f resetsFixture) body(confirmed bool) string {
	return fmt.Sprintf(`{"expected_count":2,"binding_revision":%d,"confirmed":%t}`, f.account.LinkRevision, confirmed)
}
func (f resetsFixture) seed(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, f.now)
	if err := db.InTenant(ctx, appPool, f.owner.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

// Risk: another person/key changes policy, stale writes overwrite the floor,
// or vendor data leak after sharing/rebinding/consent changes.
func TestResetPolicyReportingOwnershipRevisionAndPrivacy(t *testing.T) {
	f := newResetsFixture(t)
	peer := addPrincipal(t, f.owner.TenantID, "person", "Other admin", []string{"admin"})
	policy := `{"reset_policy":"auto_before_expiry","revision":0,"binding_revision":0}`
	callStatusAt(t, f.module, &peer, "", "PUT", f.path()+"/reset-policy", policy, f.now, 403, nil)
	callStatusAt(t, f.module, &f.runner, f.token, "PUT", f.path()+"/reset-policy", policy, f.now, 403, nil)
	callStatusAt(t, f.module, &f.owner, "", "PUT", f.path()+"/reset-policy", `{"reset_policy":"auto","revision":0,"binding_revision":0}`, f.now, 400, nil)
	var state resetState
	callStatusAt(t, f.module, &f.owner, "", "PUT", f.path()+"/reset-policy", policy, f.now, 200, &state)
	if state.Policy != "auto_before_expiry" || state.Revision != 1 || state.Credits == nil || state.Credits.Count != 2 || state.Plan == nil {
		t.Fatalf("wrong reset policy %+v", state)
	}
	callStatusAt(t, f.module, &f.owner, "", "PUT", f.path()+"/reset-policy", policy, f.now, 409, nil)
	var page overviewPage
	callStatusAt(t, f.module, &f.owner, "", "GET", "/api/agent-accounts/overview", "", f.now, 200, &page)
	if len(page.Accounts) != 1 || page.Accounts[0].Resets == nil || page.Accounts[0].ResetRevision != 1 {
		t.Fatal("overview lost reset data")
	}
	callStatusAt(t, f.module, &peer, "", "GET", "/api/agent-accounts/overview", "", f.now, 200, &page)
	if page.Accounts[0].Resets != nil || page.Accounts[0].ResetPlan != nil || page.Accounts[0].ResetPolicy != "suggest" {
		t.Fatal("private resets leaked")
	}
	f.seed(t, func(tx pgx.Tx) error {
		older := f.report
		older.ReadAt = older.ReadAt.Add(-time.Minute)
		older.Count = 1
		older.ExpiresAt = older.ExpiresAt[:1]
		a, err := getAccount(t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		if err := storeResetReport(t.Context(), tx, a, older, f.now); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE agent_accounts SET link_revision=link_revision+1 WHERE id=$1`, a.ID)
		return err
	})
	callStatusAt(t, f.module, &f.owner, "", "GET", "/api/agent-accounts/overview", "", f.now, 200, &page)
	if page.Accounts[0].Resets != nil || page.Accounts[0].ResetPolicy != "suggest" {
		t.Fatal("rebound account inherited resets/consent")
	}
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(false), f.now, 409, nil)
	if f.vendor.calls != 0 {
		t.Fatal("stale binding called vendor")
	}
}

// Risk: count races double-spend, fake success changes quota, irreversible use
// skips inline confirmation, or audit/Undo diverge from vendor results.
func TestResetUseAuditCountConfirmationAndVendorUndo(t *testing.T) {
	f := newResetsFixture(t)
	peer := addPrincipal(t, f.owner.TenantID, "person", "Other admin", []string{"admin"})
	callStatusAt(t, f.module, &peer, "", "POST", f.path()+"/resets/use", f.body(false), f.now, 403, nil)
	callStatusAt(t, f.module, &f.runner, f.token, "POST", f.path()+"/resets/use", f.body(false), f.now, 403, nil)
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", `{"expected_count":1,"binding_revision":0}`, f.now, 409, nil)
	f.module.resetVendor = nil
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(false), f.now, 422, nil)
	f.module.resetVendor = f.vendor
	f.seed(t, func(tx pgx.Tx) error {
		other := f.window
		other.WindowKind = "5h"
		other.WindowMinutes = 300
		other.UsedPercent = 17
		other.ResetsAt = f.now.Add(2 * time.Hour)
		return ingestReadings(context.WithValue(t.Context(), clockKey{}, f.now), tx, f.runner, f.account.ID, []capacity.Reading{f.window, other})
	})
	var result ResetResult
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(false), f.now, 200, &result)
	if result.ActionID == "" || result.AccountID != f.account.ID || result.Window.UsedPercent != 0 || result.Report.Count != 1 || f.vendor.calls != 1 {
		t.Fatalf("incorrect reset result %+v", result)
	}
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(false), f.now, 409, nil)
	f.seed(t, func(tx pgx.Tx) error {
		var count int
		var by string
		if err := tx.QueryRow(t.Context(), `SELECT count(*),min(after->>'by') FROM events WHERE type='account.reset_used' AND after->>'account_id'=$1`, f.account.ID).Scan(&count, &by); err != nil {
			return err
		}
		if count != 1 || by != "person" {
			return fmt.Errorf("audit count=%d by=%s", count, by)
		}
		readings, err := readCapacity(t.Context(), tx, f.account.ID, true)
		if err != nil {
			return err
		}
		if len(readings) != 2 {
			return errors.New("partial reset retired another vendor window")
		}
		for _, reading := range readings {
			if reading.WindowKind == "weekly" && reading.UsedPercent != 0 || reading.WindowKind == "5h" && reading.UsedPercent != 17 {
				return errors.New("fresh window not published or independent window changed")
			}
		}
		return nil
	})
	restored := f.report
	restored.ReadAt = f.now.Add(time.Second)
	restoredWindow := f.window
	restoredWindow.ReadAt = restored.ReadAt
	f.vendor.result = ResetResult{Report: restored, Window: restoredWindow}
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/"+result.ActionID+"/undo", `{"binding_revision":0}`, restored.ReadAt, 200, nil)
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/"+result.ActionID+"/undo", `{"binding_revision":0}`, restored.ReadAt, 409, nil)
	if f.vendor.calls != 1 || f.vendor.undoCalls != 1 {
		t.Fatal("duplicate vendor action")
	}
	f = newResetsFixture(t)
	f.report.UndoSupported = false
	f.vendor.result.UndoUntil = nil
	f.seed(t, func(tx pgx.Tx) error {
		raw := encoded(t, f.report)
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET reset_report=$2 WHERE id=$1`, f.account.ID, raw)
		return err
	})
	status, raw := callAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(false), f.now)
	if status != 409 || !strings.Contains(string(raw), "reset_confirmation_required") || f.vendor.calls != 0 {
		t.Fatal("irreversible reset did not require inline confirmation")
	}
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(true), f.now, 200, &result)
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/"+result.ActionID+"/undo", `{"binding_revision":0}`, f.now, 409, nil)
}

// Risk: a vendor timeout causes a blind second spend or falsely writes a
// success event; automatic use executes under revoked/mismatched consent.
func TestResetUnknownOutcomeAndFinalAuthorization(t *testing.T) {
	f := newResetsFixture(t)
	f.vendor.err = errors.New("vendor reply lost")
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(false), f.now, 502, nil)
	callStatusAt(t, f.module, &f.owner, "", "POST", f.path()+"/resets/use", f.body(false), f.now, 409, nil)
	if f.vendor.calls != 1 {
		t.Fatal("uncertain vendor operation repeated")
	}
	f.seed(t, func(tx pgx.Tx) error {
		var correct bool
		err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM account_reset_actions WHERE state='unknown') AND NOT EXISTS(SELECT 1 FROM events WHERE type='account.reset_used')`).Scan(&correct)
		if err == nil && !correct {
			return errors.New("timeout claimed success")
		}
		return err
	})
	f = newResetsFixture(t)
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, f.now)
	action, err := f.module.prepareReset(ctx, f.owner, f.account.ID, 0, 2, false, "person")
	if err != nil {
		t.Fatal(err)
	}
	f.seed(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=NULL WHERE id=$1`, f.account.ID)
		return err
	})
	_, err = f.module.executeReset(ctx, f.owner, action, false)
	var httpErr *httpError
	if !errors.As(err, &httpErr) || httpErr.status != 403 || f.vendor.calls != 0 {
		t.Fatalf("final owner check failed: %v", err)
	}
}

// Risk: automatic reads spend, suggestion mode spends, timing is early, or
// extra pace bypasses explicit Boost/floors and outlives the vendor window.
func TestResetAutomaticCaptureTimingAuditAndPace(t *testing.T) {
	f := newResetsFixture(t)
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, f.now)
	if err := f.module.automaticReset(ctx, f.owner.TenantID, f.account.ID); err != nil || f.vendor.calls != 0 {
		t.Fatal("suggest policy spent", err)
	}
	callStatusAt(t, f.module, &f.owner, "", "PUT", f.path()+"/reset-policy", `{"reset_policy":"auto_before_expiry","revision":0,"binding_revision":0}`, f.now, 200, nil)
	if err := f.module.automaticReset(ctx, f.owner.TenantID, f.account.ID); err != nil || f.vendor.calls != 0 {
		t.Fatal("auto spent before planned time", err)
	}
	// A fresh, fully used observation makes the plan due, without sleeping.
	f.window.UsedPercent = 100
	f.window.ReadAt = f.now
	f.report.ReadAt = f.now
	f.vendor.result.Report.ReadAt = f.now.Add(time.Second)
	f.vendor.result.Window.ReadAt = f.now.Add(time.Second)
	captureAt := f.now.Add(time.Second)
	var page overviewPage
	callStatusAt(t, f.module, &f.owner, "", "GET", "/api/agent-accounts/overview", "", f.now, 200, &page)
	if f.vendor.calls != 0 {
		t.Fatal("overview read spent credit")
	}
	f.window.Source = "agentd"
	body := encoded(t, usageReadingsWrite{readingsWrite: readingsWrite{[]capacity.Reading{f.window}}, UsageProbe: true, Revision: &f.account.LinkRevision, Resets: &f.report})
	callStatusAt(t, f.module, &f.runner, f.token, "POST", f.path()+"/readings", body, captureAt, 204, nil)
	if f.vendor.calls != 1 {
		t.Fatal("due automatic capture did not use reset")
	}
	f.seed(t, func(tx pgx.Tx) error {
		var by string
		var points float64
		if err := tx.QueryRow(t.Context(), `SELECT after->>'by',(after->>'raised_pace_points')::float8 FROM events WHERE type='account.reset_used'`).Scan(&by, &points); err != nil {
			return err
		}
		if by != "auto" || points <= 0 {
			return fmt.Errorf("wrong automatic audit %s %v", by, points)
		}
		a, err := getAccount(t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		active, err := activeResetPace(t.Context(), tx, a, captureAt)
		if err != nil {
			return err
		}
		if active != points {
			return errors.New("raised pace not stored")
		}
		readCtx := context.WithValue(t.Context(), clockKey{}, captureAt)
		snapshot, err := ReadPlanTx(readCtx, tx, f.owner)
		if err != nil {
			return err
		}
		daily := snapshot.DailyState["codex"].Accounts
		if len(daily) != 1 || daily[0].StartOfDayUsedPct == nil || *daily[0].StartOfDayUsedPct != 0 || daily[0].LimitUsedPct == nil || *daily[0].LimitUsedPct < 34 || *daily[0].LimitUsedPct > 36 {
			return fmt.Errorf("fresh reset daily baseline/pace wrong: %+v", daily)
		}
		active, err = activeResetPace(t.Context(), tx, a, f.window.ResetsAt)
		if err != nil {
			return err
		}
		if active != 0 {
			return errors.New("raised pace survived window reset")
		}
		return nil
	})
	account := agentplan.DailyAccount{UsedPct: agentplan.Number(10), StartOfDayUsedPct: agentplan.Number(0), FloorPct: agentplan.Number(20), ResetPacePoints: 25}
	settings := agentplan.DefaultDaily()
	if err := agentplan.ApplyDaily(&account, settings, 10, f.now); err != nil || *account.LimitUsedPct != 35 {
		t.Fatal("reset pace not applied", err)
	}
	settings.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 5, EnteredAs: "used", Until: f.now.Add(time.Hour)}
	if err := agentplan.ApplyDaily(&account, settings, 10, f.now); err != nil || *account.LimitUsedPct != 5 {
		t.Fatal("reset pace bypassed explicit lower boost", err)
	}
}
