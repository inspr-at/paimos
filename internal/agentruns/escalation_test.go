// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/jackc/pgx/v5"
)

func TestEscalatedClaimRequiresFreshMeasuredRoom(t *testing.T) {
	f := setup(t)
	o := f.order(t, 100)
	v := f.run(t, o)
	ids := f.reserve(t, v)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET trace='{"escalation":{"episode_id":"fixture"}}' WHERE id=$1`, v.ID)
		return err
	})
	body := claimBody(ids)
	path := "/api/runs/" + v.ID + "/claim"
	request := func() {
		t.Helper()
		raw := `{"daemon_id":"daemon-test","daemon_generation":"generation-1","reservation_ids":["` + strings.Join(ids, `","`) + `"]}`
		w := f.request(f.agent, "POST", path, raw, f.token)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "fresh measured account room") {
			t.Fatalf("unknown/stale room: %d %s", w.Code, w.Body.String())
		}
	}
	request()
	var queued agentruns.Run
	f.call(t, f.agent, "GET", "/api/runs/"+v.ID, nil, 200, &queued)
	if queued.Status != "queued" || queued.StartedAt != nil {
		t.Fatal("refused claim changed run", queued)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_kind='5h',capacity_source='agentd',capacity_read_at=clock_timestamp()-interval '11 minutes' WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	request()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_read_at=clock_timestamp() WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, body, 200, &v)
	if v.Status != "starting" || v.StartedAt == nil {
		t.Fatal("fresh room did not admit claim", v)
	}
}
