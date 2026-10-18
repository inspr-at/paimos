// SPDX-License-Identifier: AGPL-3.0-only
package outcomes

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestOutcomeRangeValidation(t *testing.T) {
	for _, pair := range [][2]string{
		{"", "2026-10-01T08:00:00Z"}, {"bad", "bad"},
		{"2026-10-01T08:00:00Z", "2026-10-01T08:00:00Z"},
		{"2026-10-01T08:00:00Z", "2026-09-30T08:00:00Z"},
		{"2024-10-01T08:00:00Z", "2026-10-01T08:00:00Z"},
	} {
		if _, _, err := outcomeRange(pair[0], pair[1], true); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
	if from, to, err := outcomeRange("", "", false); err != nil || from != nil || to != nil {
		t.Fatal("unfiltered compatibility", err)
	}
	if _, _, err := outcomeRange("2026-09-30T10:00:00+02:00", "2026-10-01T08:00:00Z", true); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", "bad", strings.Repeat("x", 257), base64.RawURLEncoding.EncodeToString([]byte("bad|11111111-1111-4111-8111-111111111111"))} {
		if _, _, err := outcomeCursor(raw, true); err == nil {
			t.Fatalf("accepted cursor %q", raw)
		}
	}
}

func TestOutcomeBriefingRangePagination(t *testing.T) {
	d := dbtest.Open(t)
	person := newPerson(t, d, "briefing-outcomes")
	project := insertNode(t, d, person, "project", "BRF-1", "Briefing", nil)
	ticket := insertNode(t, d, person, "ticket", "BRF-2", "A result", &project)
	_, token := agentKey(t, d, person.TenantID, "briefing-reader", []string{"nodes.read", "outcome.read"})
	api := outcomesAPI{t: t, mod: New(d.App)}
	// Equal timestamps require the ID tie-breaker: no duplicates or skipped rows.
	for i, at := range []string{"2026-09-30T08:00:00Z", "2026-10-01T07:00:00Z", "2026-10-01T07:00:00Z", "2026-10-01T08:00:00Z"} {
		_, err := d.Admin.Exec(t.Context(), `INSERT INTO outcome_events(tenant_id,kind,project_id,ticket_node_id,idempotency_key,actor_principal_id,source,payload,request_digest,recorded_at)
   VALUES($1,'review_verdict',$2,$3,$4,$5,'recorded','{"verdict":"changes","summary":"Fix the guard"}',decode(md5($4),'hex'),$6::timestamptz)`, person.TenantID, project, ticket, "briefing-row-"+itoa(i), person.ID, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Test through the module with its authenticated context, as other outcome list tests do.
	authMod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	api.auth = authMod
	path := "/api/outcomes?from=2026-09-30T08:00:00Z&to=2026-10-01T08:00:00Z&limit=1"
	ids := map[string]bool{}
	cursor := ""
	for i := 0; i < 3; i++ {
		current := path
		if cursor != "" {
			current += "&cursor=" + url.QueryEscape(cursor)
		}
		got := api.call(token, http.MethodGet, current, "", "")
		if got.Code != 200 {
			t.Fatalf("range %d: %d %s", i, got.Code, got.Body.String())
		}
		var page struct {
			Outcomes []outcome `json:"outcomes"`
			Next     *string   `json:"next_cursor"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Outcomes) != 1 || ids[page.Outcomes[0].ID] {
			t.Fatalf("duplicate/missing page %+v", page)
		}
		item := page.Outcomes[0]
		ids[item.ID] = true
		if !item.RecordedAt.Before(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)) {
			t.Fatal("included exclusive upper bound")
		}
		if i < 2 && page.Next == nil {
			t.Fatal("lost continuation")
		}
		if i == 2 && page.Next != nil {
			t.Fatal("spurious continuation")
		}
		if page.Next != nil {
			cursor = *page.Next
		}
	}
	for id := range ids {
		got := api.call(token, http.MethodGet, "/api/outcomes?ticket_node_id=BRF-2&outcome_id="+id, "", "")
		page := decodePage(t, got.Body.Bytes())
		if got.Code != 200 || len(page) != 1 || page[0].ID != id {
			t.Fatalf("exact source %d %+v", got.Code, page)
		}
	}
	for _, query := range []string{"from=bad&to=bad", "from=2026-10-01T08:00:00Z", "cursor=bad&ticket_node_id=BRF-2", "outcome_id=bad&ticket_node_id=BRF-2"} {
		if got := api.call(token, http.MethodGet, "/api/outcomes?"+query, "", ""); got.Code != 400 {
			t.Fatalf("invalid query %s: %d", query, got.Code)
		}
	}
	other := newPerson(t, d, "briefing-foreign")
	_, foreignToken := agentKey(t, d, other.TenantID, "briefing-foreign-reader", []string{"nodes.read", "outcome.read"})
	got := api.call(foreignToken, http.MethodGet, path, "", "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"outcomes":[]`) {
		t.Fatalf("foreign range leaked %d %s", got.Code, got.Body.String())
	}
}
