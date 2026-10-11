// SPDX-License-Identifier: AGPL-3.0-only
package accountuse_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/jackc/pgx/v5"
)

// Risk (AEON-1054 S4): a switch flipped in Settings › Accounts saves without an
// audit record, the record shows the requested rather than the stored values,
// or a stale screen's switch change is applied or audited. Each of the four
// switches goes through the HTTP route the UI uses.
func TestAccountUseSwitchChangesAreAuditedWithPersistedValues(t *testing.T) {
	f := setup(t)
	var start accountuse.Rules
	f.in(t, func(tx pgx.Tx) (err error) { start, err = accountuse.ReadRules(t.Context(), tx); return })
	if start.NewAccounts != "ask" || start.NewContexts != "ask" || start.NewProjects != "default" || start.NewModels != "allow" {
		t.Fatalf("production defaults changed: %+v", start)
	}
	steps := []accountuse.RuleValues{
		{NewAccounts: "allow", NewContexts: "ask", NewProjects: "default", NewModels: "allow"},
		{NewAccounts: "allow", NewContexts: "allow", NewProjects: "default", NewModels: "allow"},
		{NewAccounts: "allow", NewContexts: "allow", NewProjects: "holding", NewModels: "allow"},
		{NewAccounts: "allow", NewContexts: "allow", NewProjects: "holding", NewModels: "deny"},
	}
	revision := start.Revision
	for i, values := range steps {
		w := f.call(t, f.p, "PUT", "/api/account-use/rules", map[string]any{"expected_revision": revision, "new_accounts": values.NewAccounts, "new_contexts": values.NewContexts, "new_projects": values.NewProjects, "new_models": values.NewModels}, 200)
		var saved accountuse.Rules
		if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.RuleValues != values || saved.Revision != revision+1 {
			t.Fatalf("step %d: response %+v, want %+v at revision %d", i, saved, values, revision+1)
		}
		var events int
		var actor string
		var after accountuse.Rules
		var stored accountuse.Rules
		f.in(t, func(tx pgx.Tx) error {
			var raw []byte
			if err := tx.QueryRow(t.Context(), `SELECT count(*) OVER (),after,actor_principal_id::text FROM events WHERE type='account_use.rules_changed' ORDER BY id DESC LIMIT 1`).Scan(&events, &raw, &actor); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &after); err != nil {
				return err
			}
			var err error
			stored, err = accountuse.ReadRules(t.Context(), tx)
			return err
		})
		if events != i+1 || actor != f.p.ID || after.RuleValues != stored.RuleValues || after.Revision != stored.Revision || stored.RuleValues != values {
			t.Fatalf("step %d: %d audits by %s, audited %+v, stored %+v", i, events, actor, after, stored)
		}
		revision = saved.Revision
	}

	// A stale switch change (the screen still shows the first revision) is
	// refused: nothing is stored and nothing is audited.
	f.call(t, f.p, "PUT", "/api/account-use/rules", map[string]any{"expected_revision": start.Revision, "new_accounts": "ask", "new_contexts": "ask", "new_projects": "default", "new_models": "allow"}, 409)
	f.in(t, func(tx pgx.Tx) error {
		var events int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='account_use.rules_changed'`).Scan(&events); err != nil {
			return err
		}
		current, err := accountuse.ReadRules(t.Context(), tx)
		if err != nil {
			return err
		}
		if events != len(steps) || current.RuleValues != steps[len(steps)-1] || current.Revision != revision {
			t.Fatalf("stale switch change leaked: %d audits, %+v", events, current)
		}
		return nil
	})
}

// Risk (AEON-1173): a Settings rule save overwrites the model rule that Models'
// Auto-update saved, or an empty, invalid or stale partial write is stored.
// A save names only its rules; every other rule keeps its stored value, with
// the same revision guard and audit as a full write.
func TestAccountUseRuleSaveKeepsTheRulesItDoesNotName(t *testing.T) {
	f := setup(t)
	models := f.rules(t, accountuse.RuleValues{NewAccounts: "ask", NewContexts: "ask", NewProjects: "default", NewModels: "deny"})
	w := f.call(t, f.p, "PUT", "/api/account-use/rules", map[string]any{"expected_revision": models.Revision, "new_accounts": "allow"}, 200)
	var saved accountuse.Rules
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	want := accountuse.RuleValues{NewAccounts: "allow", NewContexts: "ask", NewProjects: "default", NewModels: "deny"}
	if saved.RuleValues != want || saved.Revision != models.Revision+1 {
		t.Fatalf("saved %+v at %d, want %+v at %d", saved.RuleValues, saved.Revision, want, models.Revision+1)
	}
	// Nothing named, an unknown value and a save from the screen before it are refused.
	f.call(t, f.p, "PUT", "/api/account-use/rules", map[string]any{"expected_revision": saved.Revision}, 400)
	f.call(t, f.p, "PUT", "/api/account-use/rules", map[string]any{"expected_revision": saved.Revision, "new_projects": "maybe"}, 400)
	f.call(t, f.p, "PUT", "/api/account-use/rules", map[string]any{"expected_revision": models.Revision, "new_contexts": "allow"}, 409)
	f.in(t, func(tx pgx.Tx) error {
		stored, err := accountuse.ReadRules(t.Context(), tx)
		if err != nil {
			return err
		}
		var auto bool
		var audits int
		var raw []byte
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT auto_add_profiles FROM model_refresh_settings),count(*) OVER (),after FROM events WHERE type='account_use.rules_changed' ORDER BY id DESC LIMIT 1`).Scan(&auto, &audits, &raw); err != nil {
			return err
		}
		var audited accountuse.Rules
		if err := json.Unmarshal(raw, &audited); err != nil {
			return err
		}
		if stored.RuleValues != want || stored.Revision != saved.Revision || auto || audits != 2 || audited.RuleValues != want {
			t.Fatalf("stored %+v at %d, Auto-update %v, %d audits, last %+v", stored.RuleValues, stored.Revision, auto, audits, audited.RuleValues)
		}
		return nil
	})
}
