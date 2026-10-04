// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/jackc/pgx/v5"
)

const benefitFields = `{"pill_en":"Clear release notes","pill_de":"Verständliche Release Notes","benefit_en":"Tickets explain the benefit.","benefit_de":"Tickets erklären den Nutzen."}`

func TestTicketBenefitsCreationCompletionAndHistory(t *testing.T) {
	for _, state := range []string{"done", "accepted", "delivered", "custom-complete", " CUSTOM--COMPLETE "} {
		t.Run(state, func(t *testing.T) {
			p := newPrincipal(t, "benefits")
			k := kindBySlug(t, p, "work")
			setBenefitStateCatalog(t, p.TenantID, k.ID)
			for _, key := range []string{"pill_en", "pill_de", "benefit_en", "benefit_de", "hide_from_release_notes"} {
				if !strings.Contains(string(k.FieldSchema), key) {
					t.Fatalf("seed missing %s", key)
				}
			}
			n := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Draft"}`)
			if len(n.Warnings) != 4 {
				t.Fatalf("warnings: %v", n.Warnings)
			}
			for _, fields := range []string{`{}`, `{"hide_from_release_notes":true}`, `{"pill_en":"Too short"}`} {
				code, raw := call(t, &p, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q,"fields":%s}`, state, fields))
				if code != 422 || !strings.Contains(string(raw), `"error":"before done:`) || !strings.Contains(string(raw), "benefit_de") || !strings.Contains(string(raw), `"code":"benefit_required"`) {
					t.Fatalf("gate: %d %s", code, raw)
				}
			}
			code, raw := call(t, &p, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q}`, state))
			if code != 422 || !strings.Contains(string(raw), `"code":"benefit_required"`) || !strings.Contains(string(raw), "benefit_de") {
				t.Fatalf("state-only patch: %d %s", code, raw)
			}
			code, raw = call(t, &p, "GET", "/api/nodes/"+n.ID, "")
			if unchanged := decode[nodeJSON](t, code, raw, 200); unchanged.State != n.State || !unchanged.UpdatedAt.Equal(n.UpdatedAt) {
				t.Fatal("rejected completion changed draft", unchanged)
			}
			code, raw = call(t, &p, "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Bypass","state":%q}`, k.ID, state))
			if code != 422 || !strings.Contains(string(raw), `"code":"benefit_required"`) || !strings.Contains(string(raw), "benefit_de") {
				t.Fatalf("create completed: %d %s", code, raw)
			}
			created := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Complete at creation","state":%q,"fields":%s}`, k.ID, state, benefitFields))
			if created.State != state {
				t.Fatal(created)
			}
			code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q,"fields":%s}`, state, benefitFields))
			if completed := decode[nodeJSON](t, code, raw, 200); completed.State != state {
				t.Fatal(completed)
			}
			// Already-completed imports/history are not retroactively blocked or translated.
			code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{},"title":"Historical edit"}`)
			decode[nodeJSON](t, code, raw, 200)
			for _, after := range []string{"done", "accepted", "delivered", "custom-complete", " CUSTOM--COMPLETE "} {
				code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q,"title":"Completed historical edit"}`, after))
				historical := decode[nodeJSON](t, code, raw, 200)
				if historical.State != after || historical.Title != "Completed historical edit" || string(historical.Fields) != "{}" {
					t.Fatal("historical edit changed benefits", historical)
				}
			}
			code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"state":"open"}`)
			decode[nodeJSON](t, code, raw, 200)
			code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q}`, state))
			if code != 422 || !strings.Contains(string(raw), `"code":"benefit_required"`) {
				t.Fatalf("reopened: %d %s", code, raw)
			}
			other := addPrincipal(t, "other-benefits")
			code, raw = call(t, &other, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q,"fields":%s}`, state, benefitFields))
			if code != 404 {
				t.Fatalf("foreign tenant: %d %s", code, raw)
			}
		})
	}
}
func TestTicketBenefitsBulkAndUndo(t *testing.T) {
	for _, state := range []string{"done", "accepted", "delivered", "custom-complete", "CUSTOM--COMPLETE"} {
		t.Run(state, func(t *testing.T) {
			p := newPrincipal(t, "benefits-bulk")
			k := kindBySlug(t, p, "work")
			setBenefitStateCatalog(t, p.TenantID, k.ID)
			pk := kindBySlug(t, p, "project")
			a := mustNode(t, p, `{"kind_id":"`+pk.ID+`","title":"One"}`)
			b := mustNode(t, p, `{"kind_id":"`+pk.ID+`","title":"Two"}`)
			missing := mustNode(t, p, `{"kind_id":"`+k.ID+`","parent_id":"`+a.ID+`","title":"Missing"}`)
			ready := mustNode(t, p, `{"kind_id":"`+k.ID+`","parent_id":"`+b.ID+`","title":"Ready","fields":`+benefitFields+`}`)
			code, raw := call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q,%q],"state":%q}`, missing.ID, ready.ID, state))
			result := decode[bulkResult](t, code, raw, 200)
			if len(result.Items) != 1 || result.Items[0].ID != ready.ID || result.Items[0].State != state || len(result.Skipped) != 1 || result.Skipped[0].ID != missing.ID || !strings.Contains(result.Skipped[0].Reason, "benefit_de") || result.Skipped[0].Code != ticketbenefits.RequiredCode {
				t.Fatalf("bulk: %s", raw)
			}
			// Seed historical data; all tested changes and reversals go through HTTP.
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET state=$2 WHERE id=$1`, missing.ID, state)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			undo := func(eventID int64, want int) {
				t.Helper()
				code, raw := callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p, "POST", fmt.Sprintf("/api/events/%d/undo", eventID), "")
				if code != want {
					t.Fatalf("undo: %d %s", code, raw)
				}
			}
			// Historical complete-to-complete changes, and undoing them, stay editable.
			after := "done"
			if state == "done" {
				after = "accepted"
			}
			code, raw = call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q],"state":%q}`, missing.ID, after))
			result = decode[bulkResult](t, code, raw, 200)
			if len(result.Items) != 1 || result.Items[0].State != after || result.EventID == nil {
				t.Fatalf("historical bulk: %s", raw)
			}
			undo(*result.EventID, http.StatusCreated)
			// Undoing a reopen must not bypass completion requirements on old history.
			code, raw = call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q,%q],"state":"open"}`, missing.ID, ready.ID))
			result = decode[bulkResult](t, code, raw, 200)
			if len(result.Items) != 2 || result.EventID == nil {
				t.Fatalf("reopen: %s", raw)
			}
			undo(*result.EventID, http.StatusConflict)
			for _, id := range []string{missing.ID, ready.ID} {
				code, raw = call(t, &p, "GET", "/api/nodes/"+id, "")
				if n := decode[nodeJSON](t, code, raw, 200); n.State != "open" {
					t.Fatal("rejected undo partially restored the batch", n)
				}
			}
		})
	}
}

func TestWorkBenefitsCausalUndoRejectsIncompleteCompletion(t *testing.T) {
	for _, state := range []string{"done", "custom-complete"} {
		for _, fields := range []string{`{}`, `{"pill_en":"Clear release notes","pill_de":"Verständliche Release Notes","benefit_en":"Tickets explain the benefit."}`} {
			for _, method := range []string{"GET", "POST"} {
				t.Run(fmt.Sprintf("%s/%s/%s", state, fields, method), func(t *testing.T) {
					p := newPrincipal(t, "benefits-causal-undo")
					k := kindBySlug(t, p, "work")
					setBenefitStateCatalog(t, p.TenantID, k.ID)
					parent := workNodeTest(t, p, "", "open")
					enableWorkStatusTest(t, p)
					child := workNodeTest(t, p, parent.ID, state)
					// Historical completed leaves remain editable without benefits.
					code, raw := call(t, &p, "PATCH", "/api/nodes/"+child.ID, fmt.Sprintf(`{"fields":%s}`, fields))
					decode[nodeJSON](t, code, raw, http.StatusOK)
					// The current leaf has valid benefits; Undo must validate the
					// historical fields it would restore, rather than these fields.
					code, raw = call(t, &p, "PATCH", "/api/nodes/"+child.ID, `{"state":"open","fields":`+benefitFields+`}`)
					decode[nodeJSON](t, code, raw, http.StatusOK)
					id, cause := lastDerivedTest(t, p, parent.ID)
					if currentWorkTest(t, p, parent.ID).State != "open" || currentWorkTest(t, p, child.ID).State != "open" {
						t.Fatal("fixture did not reopen the leaf and derive its parent")
					}
					// Capture complete rows (including benefit jobs and revisions)
					// plus the audit log, so a rejection cannot partially mutate them.
					snapshot := func() (json.RawMessage, json.RawMessage) {
						t.Helper()
						var nodes, audit json.RawMessage
						if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
							if err := tx.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM nodes n WHERE id=ANY($1::uuid[])`, []string{parent.ID, child.ID}).Scan(&nodes); err != nil {
								return err
							}
							return tx.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e`).Scan(&audit)
						}); err != nil {
							t.Fatal(err)
						}
						return nodes, audit
					}
					beforeNodes, beforeAudit := snapshot()
					code, raw = causalCallTest(t, p, method, id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
					if code != http.StatusConflict || !strings.Contains(string(raw), `"code":"conflict"`) || !strings.Contains(string(raw), events.ErrConflict.Error()) {
						t.Errorf("incomplete completion must reject causal Undo: %d %s", code, raw)
					}
					afterNodes, afterAudit := snapshot()
					if !sameJSON(beforeNodes, afterNodes) || !sameJSON(beforeAudit, afterAudit) {
						t.Error("rejected causal Undo changed the leaf, parent, benefit job, revision or audit log")
					}
				})
			}
		}
	}
}

func TestTicketBenefitsConcurrentFieldsAndCompletion(t *testing.T) {
	p := newPrincipal(t, "benefits-race")
	k := kindBySlug(t, p, "work")
	n := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Race","fields":`+benefitFields+`}`)
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, body := range []string{`{"state":"done"}`, `{"fields":{}}`} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := httptest.NewRequest("PATCH", "/api/nodes/"+n.ID, strings.NewReader(body))
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
			r.Header.Set("If-Unmodified-Since", n.UpdatedAt.Format(time.RFC3339Nano))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[412] != 1 {
		t.Fatal(counts)
	}
}

// The same catalog drives parent derivation and successful leaf completion.
func setBenefitStateCatalog(t *testing.T, tenantID, kindID string) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{states}', '[{"state":"custom-complete","category":"done"},{"state":"done","category":"done"}]') WHERE id=$1`, kindID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBenefitCompletionCategoryOverridesLiteralNames(t *testing.T) {
	p := newPrincipal(t, "benefit-overrides")
	k := kindBySlug(t, p, "work")
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{states}','[{"state":"done","category":"open"},{"state":"ready","category":"done"}]') WHERE id=$1`, k.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A configured Open category on a literal completion name is not completion.
	n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Open category","state":"done"}`, k.ID))
	code, raw := call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"state":"ready"}`)
	if code != 422 || !strings.Contains(string(raw), `"code":"benefit_required"`) {
		t.Fatalf("category override bypassed leaf gate: %d %s", code, raw)
	}
}
