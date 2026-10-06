// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestAgentWorkSettingsRecordChangesAndReverseWrites(t *testing.T) {
	f := fixture(t)
	for _, item := range []struct {
		path, key     string
		initial, next any
	}{
		{"/api/settings/agent-activity", "mode", "agent_summary", "off"},
		{"/api/settings/eta-interval", "interval_minutes", 10, 240},
		{"/api/settings/heartbeat-lost", "heartbeat_lost_minutes", 15, 1440},
	} {
		t.Run(item.key, func(t *testing.T) {
			count := func() int {
				var n int
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='tenant.agent_work_settings_changed'`).Scan(&n)
				})
				return n
			}
			beforeCount := count()
			expect(t, f.call(f.agent, "PUT", item.path, map[string]any{item.key: item.next}, ""), 403)
			if count() != beforeCount {
				t.Fatal("denied write appended a settings event")
			}
			expect(t, f.call(f.person, "PUT", item.path, map[string]any{item.key: item.next, "expected_" + item.key: item.next}, ""), 409)
			if count() != beforeCount {
				t.Fatal("conflicting reverse write appended an event")
			}
			for _, change := range []struct{ before, after any }{{item.initial, item.next}, {item.next, item.initial}} {
				expect(t, f.call(f.person, "PUT", item.path, map[string]any{item.key: change.after, "expected_" + item.key: change.before}, ""), 200)
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var before, after []byte
					var actor string
					if err := tx.QueryRow(t.Context(), `SELECT actor_principal_id::text,before,after FROM events WHERE type='tenant.agent_work_settings_changed' ORDER BY id DESC LIMIT 1`).Scan(&actor, &before, &after); err != nil {
						return err
					}
					var prior, saved map[string]any
					if err := json.Unmarshal(before, &prior); err != nil {
						return err
					}
					if err := json.Unmarshal(after, &saved); err != nil {
						return err
					}
					normalized := func(v any) any {
						if n, ok := v.(int); ok {
							return float64(n)
						}
						return v
					}
					if actor != f.person.ID || len(prior) != 1 || len(saved) != 1 || prior[item.key] != normalized(change.before) || saved[item.key] != normalized(change.after) {
						t.Fatalf("wrong actor or settings snapshots: %s / %s / %s", actor, before, after)
					}
					return nil
				})
				expect(t, f.call(f.person, "PUT", item.path, map[string]any{item.key: change.after}, ""), 200)
			}
			if count() != beforeCount+2 {
				t.Fatal("changes/reverse writes must record exactly two events; unchanged values must not append")
			}
			f.tx(t, f.foreign, func(tx pgx.Tx) error {
				var n int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='tenant.agent_work_settings_changed'`).Scan(&n); err != nil {
					return err
				}
				if n != 0 {
					t.Fatal("settings audit leaked across tenants")
				}
				return nil
			})
		})
	}
}
