// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestHumanCheckValidationAndAttribution(t *testing.T) {
	for _, raw := range []string{`3`, `true`, `{}`, `""`, `"   "`, `"` + strings.Repeat("x", 501) + `"`} {
		if _, err := parseHumanCheck(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`null`, `" Touch ID "`, `"` + strings.Repeat("é", 500) + `"`} {
		if _, err := parseHumanCheck(json.RawMessage(raw)); err != nil {
			t.Fatalf("refused %s: %v", raw, err)
		}
	}
	check := "Touch ID on the paired Mac"
	agent := tenant.Principal{Kind: tenant.Agent, ID: "agent"}
	if _, err := humanCheckFields(agent, json.RawMessage(`{}`), nil, &check, nil, true); err == nil {
		t.Fatal("agent completed a human check")
	}
	person := tenant.Principal{Kind: tenant.Person, ID: "person"}
	raw, err := humanCheckFields(person, json.RawMessage(`{"notes":"keep"}`), nil, &check, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	completed := result["human_check_completed"].(map[string]any)
	if completed["text"] != check || completed["by"] != person.ID || result["notes"] != "keep" {
		t.Fatalf("completion: %s", raw)
	}
	if _, err := time.Parse(time.RFC3339Nano, completed["at"].(string)); err != nil {
		t.Fatal(err)
	}
	preserved, err := humanCheckFields(agent, json.RawMessage(`{}`), raw, nil, nil, false)
	if err != nil || !strings.Contains(string(preserved), check) {
		t.Fatal("field replacement lost completion")
	}
	for _, next := range []string{check, "Another human check"} {
		if _, err := humanCheckFields(agent, json.RawMessage(`{}`), raw, nil, &next, true); err == nil {
			t.Fatal("agent erased a person's completion")
		}
	}
	if _, err := humanCheckFields(agent, json.RawMessage(`{}`), nil, nil, &check, true); err != nil {
		t.Fatalf("agent cannot add an initial pending check: %v", err)
	}
	if _, err := humanCheckFields(agent, json.RawMessage(`{}`), json.RawMessage(`{"human_check_completed":null}`), nil, &check, true); err != nil {
		t.Fatalf("null is not completion provenance: %v", err)
	}
	reworded := "Check Touch ID and pairing"
	if _, err := humanCheckFields(agent, json.RawMessage(`{}`), nil, &check, &reworded, true); err != nil {
		t.Fatalf("agent cannot edit a pending check: %v", err)
	}
	reopened, err := humanCheckFields(person, raw, raw, nil, &check, true)
	if err != nil || strings.Contains(string(reopened), "human_check_completed") {
		t.Fatal("new pending check retained completion")
	}
	if _, err := humanCheckFields(agent, json.RawMessage(`{"human_check_completed":{"text":"fake","by":"person","at":"now"}}`), nil, nil, nil, false); err == nil {
		t.Fatal("forged completion accepted")
	}
}

func TestHumanCheckCRUDFilterAndRevision(t *testing.T) {
	p := newPrincipal(t, "human-check")
	ticket := kindBySlug(t, p, "ticket")
	n := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"Touch ID","human_check":" Touch ID on the paired Mac ","fields":{"notes":"keep"}}`)
	if n.HumanCheck == nil || *n.HumanCheck != "Touch ID on the paired Mac" {
		t.Fatalf("check not stored: %+v", n)
	}
	mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"No check"}`)
	agent := estimateAgent(t, p)
	status, body := call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"human_check":null}`)
	if status != http.StatusForbidden {
		t.Fatalf("agent completed the check: %d %s", status, body)
	}
	status, body = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"human_check_completed":{"text":"fake","by":"person","at":"now"}}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("agent forged completion: %d %s", status, body)
	}
	for _, query := range []string{"pending", "!none"} {
		status, body := call(t, &p, "GET", "/api/nodes?kind=ticket&human_check="+query+"&facets=human_check", "")
		page := decode[nodePage](t, status, body, 200)
		if len(page.Items) != 1 || page.Items[0].ID != n.ID || page.Facets["human_check"]["pending"] != 1 {
			t.Fatalf("pending filter: %s", body)
		}
	}
	for _, query := range []string{"none", "!pending"} {
		status, body := call(t, &p, "GET", "/api/nodes?kind=ticket&human_check="+query, "")
		page := decode[nodePage](t, status, body, 200)
		if len(page.Items) != 1 || page.Items[0].ID == n.ID {
			t.Fatalf("empty filter: %s", body)
		}
	}
	status, _ = call(t, &p, "GET", "/api/nodes?human_check=garbage", "")
	if status != 400 {
		t.Fatalf("invalid filter %d", status)
	}
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	patch := func(since string) (int, []byte) {
		r := httptest.NewRequest("PATCH", "/api/nodes/"+n.ID, strings.NewReader(`{"human_check":null}`))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		r.Header.Set("If-Unmodified-Since", since)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	var mark int64
	if err := adminPool.QueryRow(t.Context(), `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, p.TenantID).Scan(&mark); err != nil {
		t.Fatal(err)
	}
	status, body = patch(n.UpdatedAt.Format(time.RFC3339Nano))
	checked := decode[nodeJSON](t, status, body, 200)
	if checked.HumanCheck != nil || !strings.Contains(string(checked.Fields), "human_check_completed") || !strings.Contains(string(checked.Fields), "keep") {
		t.Fatalf("checked: %s", body)
	}
	status, body = callAs(t, events.New(appPool), &p, "GET", "/api/events?after="+strconv.FormatInt(mark, 10), "")
	audit := decode[struct {
		Items []events.Event `json:"items"`
	}](t, status, body, 200)
	if len(audit.Items) != 1 || len(audit.Items[0].NodeChanges) != 1 || audit.Items[0].ActorPrincipalID != p.ID ||
		!slices.Contains(audit.Items[0].NodeChanges[0].Fields, "human_check") || !slices.Contains(audit.Items[0].NodeChanges[0].Fields, "fields.human_check_completed") {
		t.Fatalf("human-check audit/live update: %s", body)
	}
	status, body = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"notes":"replacement"}}`)
	preserved := decode[nodeJSON](t, status, body, 200)
	if !strings.Contains(string(preserved.Fields), "human_check_completed") || !strings.Contains(string(preserved.Fields), p.ID) {
		t.Fatalf("field replacement erased attribution: %s", body)
	}
	for _, text := range []string{"Touch ID on the paired Mac", "A different human check"} {
		status, body = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"title":"Must not change","human_check":"`+text+`","fields":{"notes":"must not change","human_check_completed":null}}`)
		if status != http.StatusForbidden {
			t.Fatalf("agent erased completion: %d %s", status, body)
		}
		status, body = call(t, &p, "GET", "/api/nodes/"+n.ID, "")
		unchanged := decode[nodeJSON](t, status, body, 200)
		if unchanged.HumanCheck != nil || unchanged.Title != preserved.Title || !unchanged.UpdatedAt.Equal(preserved.UpdatedAt) || !reflectJSONEqual(json.RawMessage(unchanged.Fields), json.RawMessage(preserved.Fields)) {
			t.Fatalf("refused undo changed the node or provenance: %s", body)
		}
	}
	status, _ = patch(n.UpdatedAt.Format(time.RFC3339Nano))
	if status != 412 {
		t.Fatalf("stale check accepted: %d", status)
	}
	status, body = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"human_check":"Touch ID on the paired Mac"}`)
	reopened := decode[nodeJSON](t, status, body, 200)
	if reopened.HumanCheck == nil || strings.Contains(string(reopened.Fields), "human_check_completed") {
		t.Fatalf("undo check: %s", body)
	}
	status, body = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"human_check":"Check Touch ID and pairing"}`)
	reworded := decode[nodeJSON](t, status, body, 200)
	if reworded.HumanCheck == nil || *reworded.HumanCheck != "Check Touch ID and pairing" {
		t.Fatalf("agent cannot edit a pending check: %s", body)
	}
	other := addPrincipal(t, "human-other")
	status, _ = call(t, &other, "PATCH", "/api/nodes/"+n.ID, `{"human_check":null}`)
	if status != 404 {
		t.Fatalf("foreign check modified: %d", status)
	}
}
