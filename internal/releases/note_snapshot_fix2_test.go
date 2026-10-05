// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/jackc/pgx/v5"
)

func TestFix2HistoricalSnapshotSurvivesAdoption(t *testing.T) {
	for _, versioned := range []bool{true, false} {
		name := "unversioned"
		if versioned {
			name = "versioned"
		}
		t.Run(name, func(t *testing.T) {
			f := ticketSetup(t)
			id := f.existing("ticket", f.project, "Captured member", "open")
			membershipOK(t, f.addExisting([]string{id}, 1, false))
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"pill_en":"Frozen notes","pill_de":"Erfasste Notizen","benefit_en":"Original captured text.","benefit_de":"Der erfasste Text bleibt."}' WHERE id=$1`, id); err != nil {
					return err
				}
				if versioned {
					if _, err := tx.Exec(t.Context(), `UPDATE journey_releases SET version='260928120000.0.0',version_scheme='inspr-calendar-v2' WHERE release_node_id=$1`, f.release); err != nil {
						return err
					}
				}
				_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=clock_timestamp() WHERE release_node_id=$1`, f.release)
				return err
			})
			path := "/api/projects/" + f.project + "/releases/" + f.release + "/note-snapshot"
			before := f.request(f.person, http.MethodGet, path, "")
			if before.Code != http.StatusOK {
				t.Fatalf("before adoption: %d %s", before.Code, before.Body.String())
			}
			var original releasehistory.NoteSnapshot
			if err := json.Unmarshal(before.Body.Bytes(), &original); err != nil {
				t.Fatal(err)
			}
			if !original.Frozen || original.MembershipSource != releasehistory.MembershipSource || len(original.Tickets) != 1 || original.Tickets[0].ID != id || !strings.Contains(string(original.Tickets[0].Fields), "Original captured text.") {
				t.Fatalf("missing historical fixture: %s", before.Body.String())
			}
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by,next_sequence) VALUES($1,$2,$3,2)`, f.person.TenantID, f.project, f.person.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,state,rank,version,version_scheme,released_at,origin)
 SELECT tenant_id,project_node_id,release_node_id,number,'released','V',version,version_scheme,released_at,'adopted_released' FROM journey_releases WHERE release_node_id=$1`, f.release); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"pill_en":"LIVE EDIT"}' WHERE id=$1`, id)
				return err
			})
			after := f.request(f.person, http.MethodGet, path, "")
			if after.Code != http.StatusOK {
				t.Fatalf("after adoption: %d %s", after.Code, after.Body.String())
			}
			var a, b any
			if err := json.Unmarshal(before.Body.Bytes(), &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(after.Body.Bytes(), &b); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("adoption changed frozen notes or provenance: before=%s after=%s", before.Body.String(), after.Body.String())
			}
			for _, invalid := range []struct{ project, release string }{{f.feature, f.release}, {f.project, f.feature}} {
				wrong := f.request(f.person, http.MethodGet, "/api/projects/"+invalid.project+"/releases/"+invalid.release+"/note-snapshot", "")
				if wrong.Code != http.StatusNotFound {
					t.Fatalf("wrong binding: %d %s", wrong.Code, wrong.Body.String())
				}
			}
			if foreign := f.request(f.other, http.MethodGet, path, ""); foreign.Code != http.StatusNotFound {
				t.Fatalf("foreign tenant: %d %s", foreign.Code, foreign.Body.String())
			}
		})
	}
}
