// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

func moveRequest(p tenant.Principal, id, body string, headers ...string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	r := httptest.NewRequest(http.MethodPost, "/api/nodes/"+id+"/move", strings.NewReader(body))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	for _, header := range headers {
		r.Header.Add("If-Unmodified-Since", header)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func assertMoveCurrent(t *testing.T, w *httptest.ResponseRecorder, want nodeJSON) {
	t.Helper()
	got := decode[struct {
		Error string   `json:"error"`
		Node  nodeJSON `json:"node"`
	}](t, w.Code, w.Body.Bytes(), http.StatusPreconditionFailed)
	if got.Error != "node has changed" {
		t.Fatalf("error = %q", got.Error)
	}
	// Compare JSON to ignore time location representations after a round trip.
	a, _ := json.Marshal(got.Node)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("current node = %s, want %s", a, b)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("conflict response must not be cached")
	}
}

func TestMovePreconditions(t *testing.T) {
	p := newPrincipal(t, "move-preconditions")
	k := kindBySlug(t, p, "project")
	parent := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Parent"}`)
	n := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Move me"}`)
	body := `{"parent_id":"` + parent.ID + `"}`
	before := tenantEvents(t, p.TenantID)
	for _, headers := range [][]string{{""}, {"bad-date"}, {n.UpdatedAt.Format(time.RFC3339Nano), n.UpdatedAt.Format(time.RFC3339Nano)}} {
		w := moveRequest(p, n.ID, body, headers...)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("malformed headers %v: %d %s", headers, w.Code, w.Body.String())
		}
	}
	for _, at := range []time.Time{n.UpdatedAt.Add(-time.Hour), n.UpdatedAt.Add(time.Hour)} {
		assertMoveCurrent(t, moveRequest(p, n.ID, body, at.Format(time.RFC3339Nano)), n)
	}
	if after := tenantEvents(t, p.TenantID); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected move changed events")
	}
	w := moveRequest(p, n.ID, body, n.UpdatedAt.Format(time.RFC3339Nano))
	moved := decode[nodeJSON](t, w.Code, w.Body.Bytes(), http.StatusOK)
	if moved.ParentID == nil || *moved.ParentID != parent.ID || !moved.UpdatedAt.After(n.UpdatedAt) {
		t.Fatalf("move result: %+v", moved)
	}
	assertMoveCurrent(t, moveRequest(p, n.ID, `{"parent_id":null}`, n.UpdatedAt.Format(time.RFC3339Nano)), moved)
	status, raw := call(t, &p, http.MethodGet, "/api/nodes/"+n.ID, "")
	current := decode[nodeJSON](t, status, raw, http.StatusOK)
	if !reflect.DeepEqual(current, moved) {
		t.Fatal("stale move changed node")
	}
	w = moveRequest(p, n.ID, `{"parent_id":null}`)
	current = decode[nodeJSON](t, w.Code, w.Body.Bytes(), http.StatusOK)
	if current.ParentID != nil || !current.UpdatedAt.After(moved.UpdatedAt) {
		t.Fatalf("unconditional move: %+v", current)
	}
	assertMoveCurrent(t, moveRequest(p, n.ID, body, moved.UpdatedAt.Format(time.RFC3339Nano)), current)
	changes := tenantEvents(t, p.TenantID)[len(before):]
	if len(changes) != 2 {
		t.Fatalf("move events = %d, want 2", len(changes))
	}
	for _, ev := range changes {
		if ev.Type != evNodeMoved || ev.Before == nil || ev.After == nil || ev.Actor != p.ID || ev.NodeID == nil || *ev.NodeID != n.ID {
			t.Fatalf("move event: %+v", ev)
		}
	}
}

func TestConcurrentMovesPrecondition(t *testing.T) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, http.TimeFormat} {
		t.Run(layout, func(t *testing.T) {
			p := newPrincipal(t, "move-concurrent")
			k := kindBySlug(t, p, "project")
			n := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Move me"}`)
			parents := []nodeJSON{
				mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"One"}`),
				mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Two"}`),
			}
			start := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			for _, parent := range parents {
				go func() {
					<-start
					results <- moveRequest(p, n.ID, `{"parent_id":"`+parent.ID+`"}`, n.UpdatedAt.UTC().Format(layout))
				}()
			}
			close(start)
			a, b := <-results, <-results
			if a.Code == http.StatusPreconditionFailed {
				a, b = b, a
			}
			winner := decode[nodeJSON](t, a.Code, a.Body.Bytes(), http.StatusOK)
			assertMoveCurrent(t, b, winner)
			var moves int
			for _, ev := range tenantEvents(t, p.TenantID) {
				if ev.Type == evNodeMoved {
					moves++
				}
			}
			if moves != 1 {
				t.Fatalf("concurrent moves wrote %d events", moves)
			}
		})
	}
}

func TestMovePreconditionCycleAndTenantIsolation(t *testing.T) {
	p := newPrincipal(t, "move-cycle")
	k := kindBySlug(t, p, "project")
	root := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Root"}`)
	child := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Child","parent_id":"`+root.ID+`"}`)
	before := tenantEvents(t, p.TenantID)
	for _, parent := range []string{root.ID, child.ID} {
		w := moveRequest(p, root.ID, `{"parent_id":"`+parent+`"}`, root.UpdatedAt.Format(time.RFC3339Nano))
		if w.Code != http.StatusConflict {
			t.Fatalf("cycle: %d %s", w.Code, w.Body.String())
		}
	}
	other := addPrincipal(t, "move-other")
	ok := kindBySlug(t, other, "project")
	foreign := mustNode(t, other, `{"kind_id":"`+ok.ID+`","title":"Foreign"}`)
	for _, at := range []time.Time{root.UpdatedAt, root.UpdatedAt.Add(-time.Hour)} {
		w := moveRequest(other, root.ID, `{"parent_id":null}`, at.Format(time.RFC3339Nano))
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), root.ID) {
			t.Fatalf("foreign node: %d %s", w.Code, w.Body.String())
		}
	}
	w := moveRequest(p, root.ID, `{"parent_id":"`+foreign.ID+`"}`, root.UpdatedAt.Format(time.RFC3339Nano))
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign parent: %d %s", w.Code, w.Body.String())
	}
	w = moveRequest(p, root.ID, `{"parent_id":null,"before_id":"`+foreign.ID+`"}`, root.UpdatedAt.Format(time.RFC3339Nano))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("foreign sibling: %d %s", w.Code, w.Body.String())
	}
	if after := tenantEvents(t, p.TenantID); !reflect.DeepEqual(before, after) {
		t.Fatal("refused cycle or cross-tenant move changed events")
	}
	status, raw := call(t, &p, http.MethodGet, "/api/nodes/"+root.ID, "")
	current := decode[nodeJSON](t, status, raw, http.StatusOK)
	if !reflect.DeepEqual(root, current) {
		t.Fatal("refused move changed root")
	}
}

func TestSiblingPlacementOrdersPositionsNumerically(t *testing.T) {
	// Risk: placement read sibling positions in text order ("10" before "9"),
	// so every append from the eleventh child on tied at 10 and fell back to
	// UUID order, and before_id and moves picked the wrong neighbours.
	p := newPrincipal(t, "sibling-order")
	project := kindBySlug(t, p, "project")
	work := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Root"}`)
	var want []string
	for range 12 {
		want = append(want, mustNode(t, p, `{"kind_id":"`+work.ID+`","title":"Child","parent_id":"`+root.ID+`"}`).ID)
	}
	// The eleventh child holds position 10, which sorts first as text.
	inserted := mustNode(t, p, `{"kind_id":"`+work.ID+`","title":"Inserted","parent_id":"`+root.ID+`","before_id":"`+want[10]+`"}`)
	want = append(want[:10:10], append([]string{inserted.ID}, want[10:]...)...)
	status, body := call(t, &p, http.MethodPost, "/api/nodes/"+want[0]+"/move", `{"parent_id":"`+root.ID+`"}`)
	decode[nodeJSON](t, status, body, http.StatusOK)
	want = append(want[1:], want[0])

	status, body = call(t, &p, http.MethodGet, "/api/nodes?parent_id="+root.ID, "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	got := make([]string, len(page.Items))
	for i, n := range page.Items {
		got[i] = n.ID
		if i == 0 {
			continue
		}
		previous, okPrevious := parseDecimal(page.Items[i-1].Position)
		current, okCurrent := parseDecimal(n.Position)
		if !okPrevious || !okCurrent || previous.Cmp(current) >= 0 {
			t.Fatalf("positions not strictly increasing at %d: %s then %s", i, page.Items[i-1].Position, n.Position)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sibling order %v, want %v", got, want)
	}
}
