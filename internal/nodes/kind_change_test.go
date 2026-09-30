// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPatchRefusesDifferentKind(t *testing.T) {
	p := newPrincipal(t, "kind-change")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	epicKind := kindBySlug(t, p, "epic")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Kind project","state":"active"}`)
	ticket := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Stay a ticket","fields":`+benefitFields+`}`)
	before := len(tenantEvents(t, p.TenantID))

	for _, body := range []string{
		`{"kind_id":"` + epicKind.ID + `","title":"Changed"}`,
		`{"type":"epic","title":"Changed"}`,
		`{"kind":"epic","title":"Changed"}`,
	} {
		status, raw := call(t, &p, http.MethodPatch, "/api/nodes/"+ticket.ID, body)
		if status != http.StatusConflict {
			t.Fatalf("patch %s: status %d %s", body, status, raw)
		}
		var errBody struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatal(err)
		}
		if errBody.Code != codeKindChangeNotAllowed {
			t.Fatalf("code %q body %s", errBody.Code, raw)
		}
	}

	status, raw := call(t, &p, http.MethodGet, "/api/nodes/"+ticket.ID, "")
	got := decode[nodeJSON](t, status, raw, http.StatusOK)
	if got.Title != "Stay a ticket" || got.KindID != ticketKind.ID {
		t.Fatalf("node changed: %#v", got)
	}
	if len(tenantEvents(t, p.TenantID)) != before {
		t.Fatalf("events %d, want %d", len(tenantEvents(t, p.TenantID)), before)
	}

	status, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+ticket.ID, `{"type":"ticket","title":"Renamed"}`)
	renamed := decode[nodeJSON](t, status, raw, http.StatusOK)
	if renamed.Title != "Renamed" || renamed.KindID != ticketKind.ID {
		t.Fatalf("same kind did not apply the title: %#v", renamed)
	}

	events := len(tenantEvents(t, p.TenantID))
	updated := renamed.UpdatedAt
	status, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+ticket.ID, `{"kind_id":"`+ticketKind.ID+`","kind":"ticket"}`)
	same := decode[nodeJSON](t, status, raw, http.StatusOK)
	if same.Title != "Renamed" || same.KindID != ticketKind.ID || !same.UpdatedAt.Equal(updated) {
		t.Fatalf("same kind was written: %#v", same)
	}
	if len(tenantEvents(t, p.TenantID)) != events {
		t.Fatal("same kind wrote an event")
	}

	status, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+ticket.ID, `{"type":"nope"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown type: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodGet, "/api/nodes/"+ticket.ID, "")
	still := decode[nodeJSON](t, status, raw, http.StatusOK)
	if still.Title != "Renamed" || still.KindID != ticketKind.ID {
		t.Fatalf("unknown type wrote: %#v", still)
	}
}
