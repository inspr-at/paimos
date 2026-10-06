// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"testing"
)

func TestNativeReleaseCreationAtomicReplayAndIsolation(t *testing.T) {
	f := ticketSetup(t)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released' WHERE release_node_id=$1`, f.release)
		return err
	})
	ticket := f.existing("ticket", f.project, "Independent planning", "open")
	closed := f.existing("ticket", f.project, "Closed", "done")
	path := "/api/projects/" + f.project + "/releases"
	body := func(key string, ids ...string) string {
		raw, _ := json.Marshal(releaseInput{Key: key, IDs: ids})
		return string(raw)
	}
	failed := f.request(f.person, http.MethodPost, path, body("closed", closed))
	if failed.Code != 409 || !strings.Contains(failed.Body.String(), "closed tickets") {
		t.Fatalf("closed: %d %s", failed.Code, failed.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_releases WHERE project_node_id=$1`, f.project).Scan(&count)
		if count != 1 {
			t.Errorf("rejected release persisted: %d", count)
		}
		return err
	})
	denied := f.request(f.other, http.MethodPost, path, body("other", ticket))
	if denied.Code != 403 {
		t.Fatalf("tenant fence: %d %s", denied.Code, denied.Body.String())
	}
	agent := f.request(f.agent, http.MethodPost, path, body("agent", ticket))
	if agent.Code != 403 {
		t.Fatalf("person fence: %d %s", agent.Code, agent.Body.String())
	}
	request := body("native", ticket)
	first := membershipOK(t, f.request(f.person, http.MethodPost, path, request))
	if first.Walker.ReleaseID == f.release || len(first.Walker.Tickets) != 1 || !first.Walker.Tickets[0].Included {
		t.Fatalf("new release: %+v", first)
	}
	replay := membershipOK(t, f.request(f.person, http.MethodPost, path, request))
	if replay.Walker.ReleaseID != first.Walker.ReleaseID || replay.EventID != first.EventID {
		t.Fatalf("replay drift: %+v", replay)
	}
	conflict := f.request(f.person, http.MethodPost, path, body("native", closed))
	if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), "idempotency key") {
		t.Fatalf("key conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_gates WHERE project_node_id=$1`, f.project).Scan(&count)
		if count != 0 {
			t.Errorf("Flow gate initialized: %d", count)
		}
		return err
	})
}
