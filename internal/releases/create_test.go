// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestNativeReleaseCreationAtomicReplayAndIsolation(t *testing.T) {
	f := ticketSetup(t)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=now() WHERE release_node_id=$1`, f.release)
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
	if denied.Code != 404 || !strings.Contains(denied.Body.String(), "project or release not found") {
		t.Fatalf("tenant fence: %d %s", denied.Code, denied.Body.String())
	}
	agent := f.request(f.agent, http.MethodPost, path, body("agent", ticket))
	if agent.Code != 403 || !strings.Contains(agent.Body.String(), "person required") {
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
	list := f.request(f.person, http.MethodGet, path, "")
	var listed struct {
		Releases  []planningRelease `json:"releases"`
		Truncated bool              `json:"truncated"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || list.Code != 200 || len(listed.Releases) != 2 || listed.Truncated || listed.Releases[0].ID != first.Walker.ReleaseID {
		t.Fatalf("native list: %d %s (%v)", list.Code, list.Body.String(), err)
	}
	page := f.request(f.person, http.MethodGet, path+"?before_number=2", "")
	if err := json.Unmarshal(page.Body.Bytes(), &listed); err != nil || page.Code != 200 || len(listed.Releases) != 1 || listed.Releases[0].ID != f.release {
		t.Fatalf("keyset page: %d %s (%v)", page.Code, page.Body.String(), err)
	}
	for _, cursor := range []string{"0", "2147483648", "999999999999999999999999999999"} {
		if got := f.request(f.person, http.MethodGet, path+"?before_number="+cursor, ""); got.Code != 400 || !strings.Contains(got.Body.String(), "invalid before_number") {
			t.Fatalf("invalid cursor: %d %s", got.Code, got.Body.String())
		}
	}
	conflict := f.request(f.person, http.MethodPost, path, body("native", closed))
	if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), "idempotency key") {
		t.Fatalf("key conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=now() WHERE release_node_id=$1`, first.Walker.ReleaseID)
		return err
	})
	nextTicket := f.existing("ticket", f.project, "Next planning work", "open")
	next := membershipOK(t, f.request(f.person, http.MethodPost, path, body("next", nextTicket)))
	if next.Walker.ReleaseID == first.Walker.ReleaseID {
		t.Fatal("new request reused the prior release")
	}
	prior := membershipOK(t, f.request(f.person, http.MethodPost, path, request))
	if prior.Walker.ReleaseID != first.Walker.ReleaseID || prior.EventID != first.EventID {
		t.Fatalf("replay returned the later current release: %+v", prior)
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
