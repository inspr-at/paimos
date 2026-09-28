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
	"github.com/jackc/pgx/v5"
)

const benefitFields = `{"pill_en":"Clear release notes","pill_de":"Verständliche Release Notes","benefit_en":"Tickets explain the benefit.","benefit_de":"Tickets erklären den Nutzen."}`

func TestTicketBenefitsCreationCompletionAndHistory(t *testing.T) {
	p := newPrincipal(t, "benefits")
	k := kindBySlug(t, p, "ticket")
	for _, key := range []string{"pill_en", "pill_de", "benefit_en", "benefit_de", "hide_from_release_notes"} {
		if !strings.Contains(string(k.FieldSchema), key) {
			t.Fatalf("seed missing %s", key)
		}
	}
	n := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Draft"}`)
	if len(n.Warnings) != 4 {
		t.Fatalf("warnings: %v", n.Warnings)
	}
	for _, patch := range []string{`{"state":"done"}`, `{"state":"done","fields":{"hide_from_release_notes":true}}`, `{"state":"done","fields":{"pill_en":"Too short"}}`} {
		code, raw := call(t, &p, "PATCH", "/api/nodes/"+n.ID, patch)
		if code != 422 || !strings.Contains(string(raw), "benefit_de") {
			t.Fatalf("gate: %d %s", code, raw)
		}
	}
	code, raw := call(t, &p, "POST", "/api/nodes", `{"kind_id":"`+k.ID+`","title":"Bypass","state":"done"}`)
	if code != 422 {
		t.Fatalf("create done: %d %s", code, raw)
	}
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"state":"done","fields":`+benefitFields+`}`)
	done := decode[nodeJSON](t, code, raw, 200)
	if done.State != "done" {
		t.Fatal(done)
	}
	// Already-done imports/history are not retroactively blocked or translated.
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{},"title":"Historical edit"}`)
	decode[nodeJSON](t, code, raw, 200)
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"state":"open"}`)
	decode[nodeJSON](t, code, raw, 200)
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"state":"done"}`)
	if code != 422 {
		t.Fatalf("reopened: %d %s", code, raw)
	}
	other := addPrincipal(t, "other-benefits")
	code, raw = call(t, &other, "PATCH", "/api/nodes/"+n.ID, `{"state":"done","fields":`+benefitFields+`}`)
	if code != 404 {
		t.Fatalf("foreign tenant: %d %s", code, raw)
	}
}
func TestTicketBenefitsBulkAndUndo(t *testing.T) {
	p := newPrincipal(t, "benefits-bulk")
	k := kindBySlug(t, p, "ticket")
	pk := kindBySlug(t, p, "project")
	a := mustNode(t, p, `{"kind_id":"`+pk.ID+`","title":"One"}`)
	b := mustNode(t, p, `{"kind_id":"`+pk.ID+`","title":"Two"}`)
	missing := mustNode(t, p, `{"kind_id":"`+k.ID+`","parent_id":"`+a.ID+`","title":"Missing"}`)
	ready := mustNode(t, p, `{"kind_id":"`+k.ID+`","parent_id":"`+b.ID+`","title":"Ready","fields":`+benefitFields+`}`)
	code, raw := call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q,%q],"state":"done"}`, missing.ID, ready.ID))
	result := decode[bulkResult](t, code, raw, 200)
	if len(result.Items) != 1 || result.Items[0].ID != ready.ID || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "benefit_de") {
		t.Fatalf("bulk: %s", raw)
	}
	// Undoing a reopen must not bypass the done gate on old history.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE id=$1`, missing.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code, raw = call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q],"state":"open"}`, missing.ID))
	result = decode[bulkResult](t, code, raw, 200)
	err := db.InTenant(tenant.WithPrincipal(t.Context(), p), appPool, p.TenantID, func(tx pgx.Tx) error {
		var before, after json.RawMessage
		if err := tx.QueryRow(t.Context(), `SELECT before,after FROM events WHERE id=$1`, *result.EventID).Scan(&before, &after); err != nil {
			return err
		}
		_, err := undoBulk(t.Context(), tx, p, events.Event{Before: before, After: after})
		return err
	})
	if err != events.ErrConflict {
		t.Fatalf("undo bypass: %v", err)
	}
}
func TestTicketBenefitsConcurrentFieldsAndCompletion(t *testing.T) {
	p := newPrincipal(t, "benefits-race")
	k := kindBySlug(t, p, "ticket")
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
