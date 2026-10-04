// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func (f *fixture) grace(until time.Time) {
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(f.t.Context(), `INSERT INTO status_autopilot_upgrade(tenant_id,suggest_until) VALUES($1,$2)`, f.p.TenantID, until)
		return err
	})
}
func (f *fixture) proposals() []Proposal {
	f.t.Helper()
	var out struct {
		Items []Proposal `json:"items"`
	}
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/status-autopilot/proposals", "", 200).Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out.Items
}
func (f *fixture) confirm(p tenant.Principal, want int) {
	f.t.Helper()
	var s Settings
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/settings/status-autopilot", "", 200).Body.Bytes(), &s); err != nil {
		f.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"enabled": s.Enabled, "rules": s.Rules, "expected_revision": s.Revision, "confirm_upgrade": true})
	f.call(p, "PUT", "/api/settings/status-autopilot", string(body), want)
}

func TestUpgradeMigrationProtectsOnlyExistingTenants(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	existing := "10000000-0000-4000-8000-000000000001"
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1108_status_autopilot_upgrade.sql" {
			return nil
		}
		return db.InTenant(dbtest.Seed(t.Context()), d.App, existing, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'existing','Existing')`, existing)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	check := func(id string, want string) {
		t.Helper()
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			s, err := Load(t.Context(), tx)
			if err != nil {
				return err
			}
			if s.EffectiveMode != want {
				return fmt.Errorf("mode %s, want %s", s.EffectiveMode, want)
			}
			if want == "suggest" {
				if s.SuggestUntil == nil || time.Until(*s.SuggestUntil) < 23*time.Hour || time.Until(*s.SuggestUntil) > 24*time.Hour {
					return fmt.Errorf("invalid grace deadline")
				}
				if !deadline.IsZero() && !deadline.Equal(*s.SuggestUntil) {
					return fmt.Errorf("migration restarted grace")
				}
				deadline = *s.SuggestUntil
			} else if s.SuggestUntil != nil {
				return fmt.Errorf("new tenant protected")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check(existing, "suggest")
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	check(existing, "suggest")
	fresh := "20000000-0000-4000-8000-000000000002"
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, fresh, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'fresh','Fresh')`, fresh)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(fresh, "on")
}

func TestUpgradeSuggestsWithoutTicketMutationsUntilOwnerConfirms(t *testing.T) {
	f := setup(t)
	f.grace(time.Now().UTC().Add(24 * time.Hour))
	ids := []string{f.add("AUT-2", "work", "in_progress", 4, nil), f.add("AUT-3", "work", "delivered", 31, nil), f.add("AUT-4", "work", "new", 8, nil)}
	before := []node{f.state(ids[0]), f.state(ids[1]), f.state(ids[2])}
	f.call(f.p, "PUT", "/api/projects/"+f.project+"/status-autopilot", `{"mode":"on","expected_revision":0}`, 200)
	f.run(f.now)
	f.run(f.now)
	if got := len(f.proposals()); got != 3 {
		t.Fatalf("proposals: %d", got)
	}
	for i, id := range ids {
		after := f.state(id)
		if after.State != before[i].State || !after.Updated.Equal(before[i].Updated) || len(after.Marks) != 0 || len(f.changes(id)) != 0 {
			t.Fatal("Suggest mutated ticket")
		}
	}
	// An ordinary save does not confirm, even when the workspace remains On.
	var s Settings
	_ = json.Unmarshal(f.call(f.p, "GET", "/api/settings/status-autopilot", "", 200).Body.Bytes(), &s)
	body, _ := json.Marshal(map[string]any{"enabled": true, "rules": s.Rules, "expected_revision": s.Revision})
	f.call(f.p, "PUT", "/api/settings/status-autopilot", string(body), 200)
	f.run(f.now)
	if f.state(ids[0]).State != "in_progress" {
		t.Fatal("save bypassed grace")
	}
	agent := f.p
	agent.Kind = tenant.Agent
	f.confirm(agent, 403)
	admin := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person, Name: "Workspace admin"}
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Workspace admin') RETURNING id::text`, admin.TenantID).Scan(&admin.ID)
	})
	dbtest.BindRole(t, f.d, admin.TenantID, admin.ID, "admin")
	f.confirm(admin, 403) // settings.manage alone must not end the grace period.
	f.confirm(f.p, 200)
	f.run(f.now) // Same UTC day must rescan after confirmation.
	if f.state(ids[0]).State != "open" || f.state(ids[1]).State != "accepted" || !f.state(ids[2]).Marks["triage_list"] || len(f.proposals()) != 0 {
		t.Fatal("confirmation did not resume automatic rules")
	}
}

func TestProposalApplyDismissAndStaleGuards(t *testing.T) {
	t.Setenv("AEON_STATUS_AUTOPILOT", "suggest")
	f := setup(t)
	applyID := f.add("AUT-2", "work", "in_progress", 4, nil)
	dismissID := f.add("AUT-3", "work", "in_progress", 4, nil)
	staleID := f.add("AUT-4", "work", "delivered", 31, nil)
	backlogID := f.add("AUT-5", "work", "backlog", 91, nil)
	f.run(f.now)
	items := f.proposals()
	if len(items) != 4 {
		t.Fatalf("proposals %+v", items)
	}
	for _, item := range items {
		path := fmt.Sprintf("/api/status-autopilot/proposals/%d", item.EventID)
		switch item.NodeID {
		case applyID:
			f.call(f.p, "PUT", path, `{"action":"apply"}`, 200)
			f.call(f.p, "PUT", path, `{"action":"apply"}`, 409)
		case dismissID, backlogID:
			f.call(f.p, "PUT", path, `{"action":"dismiss"}`, 200)
		case staleID:
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET human_check='Review',updated_at=clock_timestamp() WHERE id=$1`, staleID)
				return err
			})
			f.call(f.p, "PUT", path, `{"action":"apply"}`, 409)
		}
	}
	if f.state(applyID).State != "open" || f.state(dismissID).State != "in_progress" || f.state(staleID).State != "delivered" {
		t.Fatal("incorrect resolution")
	}
	if items := f.proposals(); len(items) != 1 || !items[0].ChangedSince || items[0].Applicable {
		t.Fatalf("stale guard %+v", items)
	}
	t.Setenv("AEON_STATUS_AUTOPILOT", "on")
	f.run(f.now)
	if f.state(dismissID).State != "in_progress" {
		t.Fatal("On replayed dismissed episode")
	}
	if len(f.changes(applyID)) != 1 || !f.changes(applyID)[0].Undoable {
		t.Fatal("Apply not audited/undoable")
	}
	f.run(f.now.Add(181 * 24 * time.Hour))
	if f.state(backlogID).Marks["cancel_suggested"] {
		t.Fatal("dismissal created a new Backlog inactivity anchor")
	}
}

func TestGraceExpiryRescansWithinSameDay(t *testing.T) {
	f := setup(t)
	f.grace(f.now.Add(12 * time.Hour))
	id := f.add("AUT-2", "work", "in_progress", 4, nil)
	f.run(f.now)
	f.run(f.now.Add(12*time.Hour - time.Nanosecond))
	if f.state(id).State != "in_progress" {
		t.Fatal("grace expired early")
	}
	f.run(f.now.Add(12 * time.Hour))
	if f.state(id).State != "open" {
		t.Fatal("deadline did not resume same-day rules")
	}
}

func TestServerModesCapProjectsAndPublication(t *testing.T) {
	for _, mode := range []string{"off", "suggest", "on"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AEON_STATUS_AUTOPILOT", mode)
			f := setup(t)
			progress := f.add("AUT-2", "work", "in_progress", 4, nil)
			done := f.add("AUT-3", "work", "done", 15, nil)
			fresh := f.add("AUT-4", "work", "new", 8, nil)
			f.call(f.p, "PUT", "/api/projects/"+f.project+"/status-autopilot", `{"mode":"on","expected_revision":0}`, 200)
			release := f.release([]string{done})
			f.tx(func(tx pgx.Tx) error { return PublishTx(t.Context(), tx, f.p.TenantID, release) })
			f.run(f.now)
			if mode == "on" {
				if f.state(progress).State != "open" || f.state(done).State != "delivered" || !f.state(fresh).Marks["triage_list"] {
					t.Fatal("new tenant default changed")
				}
			} else {
				if f.state(progress).State != "in_progress" || f.state(done).State != "done" || len(f.state(fresh).Marks) != 0 {
					t.Fatal("server cap bypassed")
				}
				items := f.proposals()
				if mode == "off" && len(items) != 0 || mode == "suggest" && len(items) < 3 {
					t.Fatalf("mode %s proposals %+v", mode, items)
				}
				for _, item := range items {
					if item.Rule == "publish" {
						f.call(f.p, "PUT", fmt.Sprintf("/api/status-autopilot/proposals/%d", item.EventID), `{"action":"apply"}`, 200)
					}
				}
				if mode == "suggest" && f.state(done).State != "delivered" {
					t.Fatal("manual publication Apply failed")
				}
			}
		})
	}
}

func TestUpgradePublicationWaitsAndResumesAfterConfirmation(t *testing.T) {
	f := setup(t)
	f.grace(time.Now().UTC().Add(24 * time.Hour))
	id := f.add("AUT-2", "work", "done", 15, nil)
	release := f.release([]string{id})
	f.tx(func(tx pgx.Tx) error { return PublishTx(t.Context(), tx, f.p.TenantID, release) })
	if f.state(id).State != "done" || len(f.proposals()) != 1 {
		t.Fatal("publication bypassed upgrade")
	}
	f.confirm(f.p, 200)
	f.run(time.Now().UTC().Add(2 * time.Minute))
	if f.state(id).State != "delivered" {
		t.Fatal("publication work lost during Suggest")
	}
}

func TestProposalResolutionPermissionsAndTenantIsolation(t *testing.T) {
	t.Setenv("AEON_STATUS_AUTOPILOT", "suggest")
	f := setup(t)
	f.add("AUT-2", "work", "in_progress", 4, nil)
	f.run(f.now)
	item := f.proposals()[0]
	path := fmt.Sprintf("/api/status-autopilot/proposals/%d", item.EventID)
	agent := f.p
	agent.Kind = tenant.Agent
	f.call(agent, "PUT", path, `{"action":"apply"}`, 403)
	f.call(agent, "PUT", path, `{"action":"dismiss"}`, 403)
	other := tenant.Principal{TenantID: "20000000-0000-4000-8000-000000000002", Kind: tenant.Person, Name: "Other owner"}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, other.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'other','Other')`, other.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other owner') RETURNING id::text`, other.TenantID).Scan(&other.ID)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, other.TenantID, other.ID, "owner")
	var out struct {
		Items []Proposal `json:"items"`
	}
	if err := json.Unmarshal(f.call(other, "GET", "/api/status-autopilot/proposals", "", 200).Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 0 {
		t.Fatal("foreign proposals visible")
	}
	f.call(other, "PUT", path, `{"action":"dismiss"}`, 404)
	t.Setenv("AEON_STATUS_AUTOPILOT", "off")
	if f.proposals()[0].Applicable {
		t.Fatal("server Off advertised Apply")
	}
	f.call(f.p, "PUT", path, `{"action":"apply"}`, 403)
	f.call(f.p, "PUT", path, `{"action":"dismiss"}`, 200)
}

func TestSuggestPublicationFullBatchYieldsAndRetainsHumanChecks(t *testing.T) {
	t.Setenv("AEON_STATUS_AUTOPILOT", "suggest")
	f := setup(t)
	ids := []string{}
	for i := range batchSize + 1 {
		ids = append(ids, f.add(fmt.Sprintf("AUT-%d", i+2), "work", "done", 15, nil))
	}
	human := "Review"
	checked := f.add("AUT-100", "work", "done", 15, &human)
	ids = append(ids, checked)
	f.release(ids)
	f.run(time.Now().UTC())
	for _, id := range ids {
		if f.state(id).State != "done" || len(f.state(id).Marks) != 0 {
			t.Fatal("Suggest mutated release ticket")
		}
	}
	t.Setenv("AEON_STATUS_AUTOPILOT", "on")
	f.run(time.Now().UTC().Add(2 * time.Minute))
	for _, id := range ids[:len(ids)-1] {
		if f.state(id).State != "delivered" {
			t.Fatal("publication cursor lost a proposal")
		}
	}
	if f.state(checked).State != "done" {
		t.Fatal("pending human check delivered")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='status_autopilot.skipped'`, checked).Scan(&count)
		if err == nil && count != 1 {
			return fmt.Errorf("human check skip audit count %d", count)
		}
		return err
	})
}
