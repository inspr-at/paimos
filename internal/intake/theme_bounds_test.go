// SPDX-License-Identifier: AGPL-3.0-only
package intake

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestSingleDraftDoesNotMaterializeProjectHistory(t *testing.T) {
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	var wanted string
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO intake_drafts(tenant_id,project_node_id,kind,requirement_kind,title,body,base_event_id,idempotency_key,proposed_by_principal_id)
   VALUES($1,$2,'requirement','functional','wanted','wanted',0,'wanted',$3) RETURNING id::text`, fx.tenantA, fx.projectA, fx.agent.ID).Scan(&wanted); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO intake_drafts(tenant_id,project_node_id,kind,requirement_kind,title,body,base_event_id,idempotency_key,proposed_by_principal_id)
   SELECT $1,$2,'requirement','functional','unrelated',repeat('x',65536),0,'history-'||g,$3 FROM generate_series(1,500) g`, fx.tenantA, fx.projectA, fx.agent.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO intake_draft_ticket_suggestions(tenant_id,project_node_id,draft_id,ordinal,title,estimated_hours)
          SELECT tenant_id,project_node_id,id,0,'suggestion',1 FROM intake_drafts WHERE project_node_id=$1`, fx.projectA)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	work := dbtest.ReadWork{}
	err = db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
		got, err := findDraftByID(t.Context(), dbtest.CountReads(tx, &work), fx.projectA, wanted)
		if err == nil && (got.ID != wanted || got.Body != "wanted" || len(got.Suggestions) != 1) {
			t.Fatal("wrong draft")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if work.Rows != 2 || work.Statements != 3 {
		t.Fatalf("single draft materialized unrelated history: %+v", work)
	}
}

// A growing transcript must remain readable; each collection continues on its
// own keyset, even after sources and drafts are already exhausted.
func TestIntakeHistoryContinuesBeyond200(t *testing.T) {
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	target, eventID := fx.nodeWithEvent(t, database, "memory", "MEM-98", "Target", "Body", fx.agent.ID)
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
		queries := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO intake_sources(tenant_id,project_node_id,kind,label,content_sha256,idempotency_key,created_by_principal_id)
			 SELECT $1,$2,'conversation','source-'||g,$3,'source-'||g,$4 FROM generate_series(1,201) g`, []any{fx.tenantA, fx.projectA, digest, fx.person.ID}},
			{`INSERT INTO intake_transcript_turns(tenant_id,project_node_id,source_id,ordinal,speaker,speaker_principal_id,body,idempotency_key)
			 SELECT $1,$2,(SELECT id FROM intake_sources WHERE project_node_id=$2 ORDER BY id LIMIT 1),g,'person',$3,'turn-'||g,'turn-'||g FROM generate_series(0,400) g`, []any{fx.tenantA, fx.projectA, fx.person.ID}},
			{`INSERT INTO intake_drafts(tenant_id,project_node_id,kind,title,body,base_event_id,idempotency_key,proposed_by_principal_id)
			 SELECT $1,$2,'brief','draft-'||g,'body-'||g,0,'draft-'||g,$3 FROM generate_series(1,201) g`, []any{fx.tenantA, fx.projectA, fx.agent.ID}},
			{`INSERT INTO intake_citations(tenant_id,project_node_id,draft_id,ordinal,source_id,locator)
			 SELECT tenant_id,project_node_id,id,0,(SELECT id FROM intake_sources WHERE project_node_id=$1 ORDER BY id LIMIT 1),'paragraph' FROM intake_drafts WHERE project_node_id=$1`, []any{fx.projectA}},
			{`INSERT INTO intake_draft_ticket_suggestions(tenant_id,project_node_id,draft_id,ordinal,title,estimated_hours)
			 SELECT tenant_id,project_node_id,id,0,title,1 FROM intake_drafts WHERE project_node_id=$1`, []any{fx.projectA}},
			{`INSERT INTO intake_draft_acceptances(tenant_id,project_node_id,draft_id,accepted_by_principal_id,target_node_id,event_id)
			 SELECT tenant_id,project_node_id,id,$2,$3,$4 FROM intake_drafts WHERE project_node_id=$1`, []any{fx.projectA, fx.person.ID, target, eventID}},
		}
		for _, q := range queries {
			if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(database.App).Mount(mux)
	path := "/api/projects/" + fx.projectA + "/intake"
	var firstCursor string
	for _, nodeID := range []string{"", target} {
		t.Run("node="+nodeID, func(t *testing.T) {
			seen := [3]map[string]bool{{}, {}, {}}
			cursor := ""
			pages := 0
			for {
				q := url.Values{}
				if nodeID != "" {
					q.Set("node_id", nodeID)
				}
				if cursor != "" {
					q.Set("after", cursor)
				}
				w := call(mux, fx.person, "", http.MethodGet, path+"?"+q.Encode(), "")
				if w.Code != http.StatusOK {
					t.Fatalf("history page %d: %d %s", pages, w.Code, w.Body.String())
				}
				var got snapshot
				decodeJSON(t, w, &got)
				if len(got.Sources) > 200 || len(got.Turns) > 200 || len(got.Drafts) > 200 {
					t.Fatal("unbounded history page")
				}
				if pages == 0 && nodeID == "" && (len(got.Sources) != 200 || len(got.Turns) != 200 || len(got.Drafts) != 200) {
					t.Fatal("first page lost history")
				}
				if pages == 2 && (len(got.Sources) != 0 || len(got.Drafts) != 0 || len(got.Turns) != 1) {
					t.Fatalf("completed collections repeated: %+v", got)
				}
				if nodeID != "" && (len(got.Sources) != 0 || len(got.Turns) != 0) {
					t.Fatal("node filter included unrelated history")
				}
				check := func(collection int, id string) {
					if seen[collection][id] {
						t.Fatalf("duplicate collection %d id %s", collection, id)
					}
					seen[collection][id] = true
				}
				for _, v := range got.Sources {
					check(0, v.ID)
				}
				for _, v := range got.Turns {
					check(1, v.ID)
				}
				for _, v := range got.Drafts {
					check(2, v.ID)
					if v.Status != "accepted" || v.TargetNodeID == nil || *v.TargetNodeID != target || len(v.Citations) != 1 || len(v.Suggestions) != 1 || v.Suggestions[0].Title != v.Title {
						t.Fatalf("draft association lost: %+v", v)
					}
				}
				pages++
				cursor = w.Header().Get("X-Next-Cursor")
				if nodeID == "" && pages == 1 {
					firstCursor = cursor
				}
				if cursor == "" {
					break
				}
				if pages >= 3 {
					t.Fatal("history did not terminate")
				}
			}
			want := [3]int{201, 401, 201}
			wantPages := 3
			if nodeID != "" {
				want = [3]int{0, 0, 201}
				wantPages = 2
			}
			for i, n := range want {
				if len(seen[i]) != n {
					t.Fatalf("collection %d: got %d, want %d", i, len(seen[i]), n)
				}
			}
			if pages != wantPages {
				t.Fatalf("pages: got %d, want %d", pages, wantPages)
			}
		})
	}
	for _, query := range []string{
		"after=" + url.QueryEscape(firstCursor) + "&node_id=" + target,
		"limit=0", "limit=201", "limit=1&limit=2", "after=bad", "after=",
	} {
		w := call(mux, fx.person, "", http.MethodGet, path+"?"+query, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid selector %q returned %d", query, w.Code)
		}
	}
	w := call(mux, fx.personB, "", http.MethodGet, "/api/projects/"+fx.projectB+"/intake?after="+url.QueryEscape(firstCursor), "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("cursor crossed tenant/project: %d", w.Code)
	}
}

func TestLongIntakeTranscriptContinuesBeyond200(t *testing.T) {
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
		var source string
		if err := tx.QueryRow(t.Context(), `INSERT INTO intake_sources(tenant_id,project_node_id,kind,label,content_sha256,idempotency_key,created_by_principal_id)
			VALUES($1,$2,'conversation','Long conversation',$3,'conversation',$4) RETURNING id::text`, fx.tenantA, fx.projectA, digest, fx.person.ID).Scan(&source); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO intake_transcript_turns(tenant_id,project_node_id,source_id,ordinal,speaker,speaker_principal_id,body,idempotency_key)
			SELECT $1,$2,$3,g,'person',$4,'turn-'||g,'turn-'||g FROM generate_series(0,400) g`, fx.tenantA, fx.projectA, source, fx.person.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(database.App).Mount(mux)
	cursor := ""
	ordinal := 0
	for page := 0; page < 3; page++ {
		query := ""
		if cursor != "" {
			query = "?after=" + url.QueryEscape(cursor)
		}
		w := call(mux, fx.person, "", http.MethodGet, "/api/projects/"+fx.projectA+"/intake"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("long transcript page %d: %d %s", page, w.Code, w.Body.String())
		}
		var got snapshot
		decodeJSON(t, w, &got)
		wantTurns := 200
		if page == 2 {
			wantTurns = 1
		}
		wantSources := 0
		if page == 0 {
			wantSources = 1
		}
		if len(got.Turns) != wantTurns || len(got.Sources) != wantSources || len(got.Drafts) != 0 {
			t.Fatalf("wrong page counts: sources=%d turns=%d drafts=%d", len(got.Sources), len(got.Turns), len(got.Drafts))
		}
		for _, turn := range got.Turns {
			if turn.Ordinal != ordinal {
				t.Fatalf("turn ordinal %d, want %d", turn.Ordinal, ordinal)
			}
			ordinal++
		}
		cursor = w.Header().Get("X-Next-Cursor")
		if (cursor == "") != (page == 2) {
			t.Fatal("wrong end-of-history signal")
		}
	}
	if ordinal != 401 {
		t.Fatalf("long transcript lost turns: %d", ordinal)
	}
}
