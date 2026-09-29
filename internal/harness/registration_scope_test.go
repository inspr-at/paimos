// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRegistrationRequiresHeartbeatAndStopAuthority(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "registration-ref-" + uid(), "worker_lease": "registration-lease-" + uid()}
	f.agent.Scopes = []string{"harness.write"}
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.write'] WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	w := f.call(f.agent, "POST", base, body, "")
	expect(t, w, 403)
	if response := decode(t, w); response["reason_code"] != "missing_key_scope" || response["scope"] != "harness.worker" {
		t.Fatal("registration lacks missing heartbeat scope reason")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions`).Scan(&count)
		if count != 0 {
			t.Error("unclosable generation was registered")
		}
		return err
	})
	f.agent.Scopes = append(slices.Clone(f.agent.Scopes), "harness.worker")
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes)
		return err
	})
	expect(t, f.call(f.agent, "POST", base, body, ""), 201)
}
