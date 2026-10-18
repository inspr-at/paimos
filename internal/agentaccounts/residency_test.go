// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

type evidenceFixture struct {
	classes map[string]string
	expires time.Time
}

func (f evidenceFixture) ResidencyClass(_ context.Context, _ pgx.Tx, a Account, _ string) (string, string, *time.Time, error) {
	return f.classes[a.ID], "test-evidence", &f.expires, nil
}
func TestResidencyEvidenceDefaultsAndExpires(t *testing.T) {
	now := time.Now()
	accounts := []Account{{ID: "cloud"}, {ID: "eu"}, {ID: "local"}}
	ctx := WithResidencyClassifier(t.Context(), evidenceFixture{map[string]string{"cloud": "any", "eu": "eu", "local": "local"}, now.Add(time.Hour)})
	kept, empty, err := applyResidency(ctx, nil, accounts, "profile", "eu", now)
	if err != nil || empty || len(kept) != 2 || kept[0].ID != "eu" {
		t.Fatal(kept, empty, err)
	}
	kept, empty, err = applyResidency(ctx, nil, accounts, "profile", "local", now)
	if err != nil || empty || len(kept) != 1 || kept[0].ID != "local" {
		t.Fatal(kept, empty, err)
	}
	kept, empty, err = applyResidency(t.Context(), nil, accounts, "profile", "eu", now)
	if err != nil || !empty || len(kept) != 0 {
		t.Fatal("default evidence widened", kept, empty, err)
	}
	expired := WithResidencyClassifier(t.Context(), evidenceFixture{map[string]string{"eu": "eu", "local": "local"}, now})
	kept, empty, err = applyResidency(expired, nil, accounts, "profile", "eu", now)
	if err != nil || !empty || len(kept) != 0 {
		t.Fatal("expired evidence accepted", kept, empty, err)
	}
	if _, empty, err := applyResidency(ctx, nil, nil, "profile", "eu", now); err != nil || empty {
		t.Fatal("already empty mislabeled", empty, err)
	}
}
func TestResidencyFenceMapsStarterLinkedAfterQueue(t *testing.T) {
	reset(t)
	alice, runner, profile, token, mod := groupFixture(t)
	bob := addPrincipal(t, alice.TenantID, "person", "Canonical Bob", []string{"admin"})
	cloud := groupAccount(t, mod, alice, runner, token, "cloud", "daemon-a", "Cloud", "test")
	euAccount := groupAccount(t, mod, alice, runner, token, "eu", "daemon-a", "EU", "test")
	project, _, runID := insertTicketRun(t, alice, runner, profile)
	eu := "eu"
	seed(t, alice, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET prefs_person_id=$2 WHERE id=$1`, runID, alice.ID); err != nil {
			return err
		}
		if _, err := modelprefs.SaveScope(t.Context(), tx, bob, modelprefs.Scope{Level: "person", PersonID: &bob.ID, Residency: &eu}); err != nil {
			return err
		}
		before, err := modelprefs.RunRequirement(t.Context(), tx, runID)
		if err != nil {
			return err
		}
		if before.Residency != "any" {
			t.Fatal("Bob applied before link")
		}
		_, err = tx.Exec(t.Context(), `UPDATE principals SET linked_to=$2 WHERE id=$1`, alice.ID, bob.ID)
		return err
	})
	seed(t, alice, func(tx pgx.Tx) error {
		policy, err := modelprefs.RunRequirement(t.Context(), tx, runID)
		if err != nil {
			return err
		}
		if policy.Residency != "eu" || policy.CanonicalPersonID == nil || *policy.CanonicalPersonID != bob.ID {
			t.Fatal("pre-link stamp trusted", policy)
		}
		wait, err := WaitForRun(t.Context(), tx, runID)
		if err != nil {
			return err
		}
		if wait == nil || wait.Code != "residency" || wait.RunNowAllowed {
			t.Fatal("unqualified account spill", wait)
		}
		run, err := loadWaitRun(t.Context(), tx, runID)
		if err != nil {
			return err
		}
		ctx := WithResidencyClassifier(t.Context(), evidenceFixture{map[string]string{euAccount.ID: "eu"}, time.Now().Add(time.Hour)})
		kept, empty, err := narrowCandidates(ctx, tx, run, "codex", []Account{cloud, euAccount})
		if err != nil {
			return err
		}
		if empty || len(kept) != 1 || kept[0].ID != euAccount.ID {
			t.Fatal("live EU fence", kept, empty)
		}
		// A requested account is checked after residency; it cannot widen the fence.
		run.RequestedAccountID = &cloud.ID
		kept, empty, err = narrowCandidates(ctx, tx, run, "codex", []Account{cloud, euAccount})
		if err != nil {
			return err
		}
		if empty || len(kept) != 0 {
			t.Fatal("pin widened/mislabeled fence", kept, empty)
		}
		err = setRunTarget(t.Context(), tx, bob, runID, cloud.ID, "")
		if he, ok := err.(*httpError); !ok || he.code != "residency_unmet" || he.status != 409 {
			t.Fatalf("retarget should reject residency: %v", err)
		}
		// A canonical tightening reaches a run stored with the pre-link id.
		local := "local"
		if _, err := modelprefs.SaveScope(t.Context(), tx, bob, modelprefs.Scope{Level: "person", PersonID: &bob.ID, Residency: &local}); err != nil {
			return err
		}
		var stamp string
		if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, runID).Scan(&stamp); err != nil {
			return err
		}
		if stamp != "local" {
			t.Fatal("canonical restamp missed alias", stamp)
		}
		if _, err := modelprefs.SaveScope(t.Context(), tx, bob, modelprefs.Scope{Level: "person", PersonID: &bob.ID, Residency: &eu}); err != nil {
			return err
		}
		policy, err = modelprefs.RunRequirement(t.Context(), tx, runID)
		if err != nil {
			return err
		}
		if policy.Residency != "local" {
			t.Fatal("stamp loosened", policy)
		}
		// A project fence with no members keeps the historical offline wait code.
		var group string
		if err := tx.QueryRow(t.Context(), `INSERT INTO account_groups(tenant_id,harness,name) VALUES($1,'codex','Empty project fence') RETURNING id::text`, alice.TenantID).Scan(&group); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_group_projects(tenant_id,group_id,project_id) VALUES($1,$2,$3)`, alice.TenantID, group, project); err != nil {
			return err
		}
		wait, err = WaitForRun(t.Context(), tx, runID)
		if err != nil {
			return err
		}
		if wait == nil || wait.Code != "offline" {
			t.Fatal("empty project fence changed wait", wait)
		}
		return nil
	})
	// Even without a project grant, the route-only lookup retains the live floor
	// and restores visibility before later statements.
	err := db.InTenant(db.NoProjects(t.Context(), "residency test"), appPool, alice.TenantID, func(tx pgx.Tx) error {
		var before, after string
		if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.visible_projects')`).Scan(&before); err != nil {
			return err
		}
		policy, err := modelprefs.RunRequirement(t.Context(), tx, runID)
		if err != nil {
			return err
		}
		if policy.Residency != "local" {
			t.Fatal(policy)
		}
		if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.visible_projects')`).Scan(&after); err != nil {
			return err
		}
		if before != after {
			t.Fatal("visibility leaked")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
