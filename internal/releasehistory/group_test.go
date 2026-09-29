// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestParseTicketMeta(t *testing.T) {
	benefit := func(hidden bool) string {
		raw, _ := json.Marshal(map[string]any{
			"pill_en": "Quotes open reliably", "pill_de": "Angebote öffnen zuverlässig",
			"benefit_en": "Quotes open normally.", "benefit_de": "Angebote öffnen sich.",
			"hide_from_release_notes": hidden, "tags": []string{"bug"},
		})
		return string(raw)
	}
	if got := ParseTicketMeta("ticket", json.RawMessage(benefit(false))); !got.Bug || got.PublicBenefit {
		t.Fatalf("visible bug %+v", got)
	}
	if got := ParseTicketMeta("ticket", json.RawMessage(benefit(true))); !got.Bug || got.PublicBenefit {
		t.Fatalf("hidden bug %+v", got)
	}
	classic, _ := json.Marshal(map[string]any{"tags": []map[string]any{{"name": "BUG", "color": "red"}}})
	if got := ParseTicketMeta("ticket", classic); !got.Bug || got.PublicBenefit {
		t.Fatalf("classic tag %+v", got)
	}
	if got := ParseTicketMeta("bug", json.RawMessage(`{"type":"bug"}`)); !got.Bug {
		t.Fatalf("kind and type %+v", got)
	}
	hiddenNote, _ := json.Marshal(map[string]any{"pill_en": "Keep private", "benefit_en": "Stay out of the notes.", "hide_from_release_notes": true})
	if got := ParseTicketMeta("ticket", hiddenNote); got.Bug || got.PublicBenefit {
		t.Fatalf("hidden note %+v", got)
	}
	blank, _ := json.Marshal(map[string]any{"pill_en": "   ", "benefit_en": ""})
	if got := ParseTicketMeta("ticket", blank); got.PublicBenefit || got.Note != nil {
		t.Fatalf("blank note %+v", got)
	}
	feature, _ := json.Marshal(map[string]any{"pill_en": " Quotes open ", "pill_de": "", "benefit_en": "Quotes open normally.", "benefit_de": "Angebote öffnen sich."})
	if got := ParseTicketMeta("ticket", feature); got.Bug || !got.PublicBenefit || got.Note == nil || got.Note.PillEN != "Quotes open" || got.Note.PillDE != "" || got.Note.BenefitDE != "Angebote öffnen sich." {
		t.Fatalf("feature note %+v", got)
	}
	if got := ParseTicketMeta("ticket", json.RawMessage(benefit(false))); got.Note == nil || got.Note.PillEN != "Quotes open reliably" || got.PublicBenefit {
		t.Fatalf("visible bug keeps its note %+v", got)
	}
	if got := ParseTicketMeta("ticket", hiddenNote); got.Note != nil {
		t.Fatalf("hidden note leaked %+v", got.Note)
	}
	if got := ParseTicketMeta("ticket", json.RawMessage(`{"tags":"bug","type":{"name":"bug"}}`)); got.Bug {
		t.Fatalf("wrong shapes %+v", got)
	}
}

func TestGroupChange(t *testing.T) {
	bug := TicketMeta{Bug: true, PublicBenefit: true}
	feature := TicketMeta{PublicBenefit: true}
	hidden := TicketMeta{}
	meta := map[string]TicketMeta{"AEON-274": bug, "AEON-273": feature, "AEON-1": hidden, "AEON-2": feature}
	cases := []struct {
		subject string
		tickets []string
		want    string
	}{
		{"feat(AEON-274): a bug written as a feature", []string{"AEON-274"}, GroupFeatures},
		{"fix(AEON-273): a feature written as a fix", []string{"AEON-273"}, GroupFixes},
		{"test(AEON-273): cover the chat", []string{"AEON-273"}, GroupOther},
		{"docs: explain the column picker", nil, GroupOther},
		{"chore: tidy the manifest", []string{"AEON-273"}, GroupOther},
		{"release: v260929062507.0.0", []string{"AEON-273"}, ""},
		{"P0.x: showcase quotes load without crashing (AEON-274)", []string{"AEON-274"}, GroupFixes},
		{"P0.x: session chat tabs, unread and scroll behaviour (AEON-273)", []string{"AEON-273"}, GroupFeatures},
		{"P0.x: one commit, a bug and a feature (AEON-274, AEON-273)", []string{"AEON-274", "AEON-273"}, GroupFixes},
		{"P0.x: hidden ticket and a feature (AEON-1, AEON-2)", []string{"AEON-1", "AEON-2"}, GroupFeatures},
		{"P0.x: a hidden ticket only (AEON-1)", []string{"AEON-1"}, GroupOther},
		{"P0.x: refresh the release manifest", nil, GroupOther},
		{"P0.x: names a ticket this workspace does not have (AEON-999)", []string{"AEON-999"}, GroupOther},
		{"hotfix(AEON-273): busy begin", []string{"AEON-273"}, GroupFixes},
		{"feature(AEON-274): old spelling", []string{"AEON-274"}, GroupFeatures},
	}
	for _, tc := range cases {
		if got := GroupChange(tc.subject, tc.tickets, meta); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.subject, got, tc.want)
		}
	}
	// One commit is one group, even when it names several tickets.
	if got := GroupChange(cases[8].subject, cases[8].tickets, meta); got != GroupFixes {
		t.Fatalf("multi-ticket commit %q", got)
	}
}

func TestServeGroups(t *testing.T) {
	h := History{Schema: Schema, Product: "PAIMOS AEON", Releases: []Release{{
		Version: "260929062507.0.0", Tag: "v260929062507.0.0", State: StatePublished, Headline: "stable100",
		Tickets: []string{"AEON-273", "AEON-274"},
		Changes: []Change{
			{Commit: "a", Subject: "release: v260929062507.0.0", Type: "release", Tickets: []string{"AEON-273"}, At: "2026-09-29T06:25:07Z"},
			{Commit: "b", Subject: "P0.x: showcase quotes load without crashing (AEON-274)", Type: "other", Tickets: []string{"AEON-274"}, At: "2026-09-29T06:20:00Z"},
			{Commit: "c", Subject: "P0.x: session chat tabs (AEON-273)", Type: "other", Tickets: []string{"AEON-273"}, At: "2026-09-29T06:10:00Z"},
			{Commit: "d", Subject: "P0.x: one commit, a bug and a feature (AEON-274, AEON-273)", Type: "other", Tickets: []string{"AEON-274", "AEON-273"}, At: "2026-09-29T06:00:00Z"},
			{Commit: "e", Subject: "P0.x: refresh the release manifest", Type: "other", Tickets: nil, At: "2026-09-29T05:50:00Z"},
			{Commit: "f", Subject: "feat: keep a conventional feature", Type: "feat", Tickets: []string{"AEON-274"}, At: "2026-09-29T05:40:00Z"},
			{Commit: "g", Subject: "P0.x: a hidden ticket only (AEON-1)", Type: "other", Tickets: []string{"AEON-1"}, At: "2026-09-29T05:30:00Z"},
		},
	}}}
	mod := NewWith(h, "260929062507.0.0")
	mod.UseTickets(func(_ context.Context, tenantID string, keys []string) (map[string]TicketMeta, error) {
		if tenantID != "11111111-1111-4111-8111-111111111111" {
			t.Errorf("tenant %s", tenantID)
		}
		if strings.Join(keys, ",") != "AEON-273,AEON-274,AEON-1" {
			t.Errorf("keys %v", keys)
		}
		hidden, _ := json.Marshal(map[string]any{"pill_en": "Keep private", "benefit_en": "Stay out of the notes.", "hide_from_release_notes": true})
		return map[string]TicketMeta{
			"AEON-274": {Bug: true, PublicBenefit: true, Note: &TicketNote{PillEN: "Quotes open reliably", PillDE: "Angebote öffnen zuverlässig", BenefitEN: "Quotes open.", BenefitDE: "Angebote öffnen sich."}},
			"AEON-273": {PublicBenefit: true, Note: &TicketNote{PillEN: "Session chat", PillDE: "Sitzungschat", BenefitEN: "The chat stays put.", BenefitDE: "Der Chat bleibt."}},
			"AEON-1":   ParseTicketMeta("ticket", hidden),
		}, nil
	})
	mux := http.NewServeMux()
	mod.Mount(mux)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: "22222222-2222-4222-8222-222222222222", TenantID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	w := get("/api/releases")
	if w.Code != 200 {
		t.Fatalf("list %d %s", w.Code, w.Body)
	}
	var body Response
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []string{"", GroupFixes, GroupFeatures, GroupFixes, GroupOther, GroupFeatures, GroupOther}
	if len(body.Releases[0].Changes) != len(want) {
		t.Fatalf("changes %d", len(body.Releases[0].Changes))
	}
	for i, change := range body.Releases[0].Changes {
		if change.Group != want[i] {
			t.Errorf("change %d %q group %q want %q", i, change.Subject, change.Group, want[i])
		}
	}
	rawList := w.Body.String()
	if strings.Contains(string(changeJSON(t, w.Body.Bytes(), 0)), `"group"`) || strings.Contains(string(changeJSON(t, w.Body.Bytes(), 0)), `linked_tickets`) {
		t.Fatal("version bump carries a group or linked tickets")
	}
	if strings.Contains(rawList, "Keep private") || strings.Contains(rawList, "Stay out of the notes.") {
		t.Fatal("hidden ticket text was served")
	}
	one := get("/api/releases/260929062507.0.0")
	var rel Release
	if one.Code != 200 || json.Unmarshal(one.Body.Bytes(), &rel) != nil || rel.Changes[1].Group != GroupFixes {
		t.Fatalf("one %d %s", one.Code, one.Body)
	}
	if len(rel.Changes[1].Linked) != 1 || rel.Changes[1].Linked[0].Key != "AEON-274" || rel.Changes[1].Linked[0].PillEN != "Quotes open reliably" || rel.Changes[1].Linked[0].BenefitDE != "Angebote öffnen sich." {
		t.Fatalf("bug note %+v", rel.Changes[1].Linked)
	}
	if len(rel.Changes[3].Linked) != 2 || rel.Changes[3].Linked[0].Key != "AEON-274" || rel.Changes[3].Linked[1].Key != "AEON-273" || rel.Changes[3].Linked[0].Group != GroupFixes || rel.Changes[3].Linked[1].Group != GroupFeatures {
		t.Fatalf("two notes %+v", rel.Changes[3].Linked)
	}
	if rel.Changes[4].Linked != nil || rel.Changes[6].Linked != nil || rel.Changes[6].Group != GroupOther {
		t.Fatalf("other changes must not carry notes: %+v %+v", rel.Changes[4], rel.Changes[6])
	}
	for _, change := range h.Releases[0].Changes {
		if change.Group != "" || change.Linked != nil {
			t.Fatal("annotation mutated the stored history")
		}
	}

	mod.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		return nil, errors.New("lookup failed")
	})
	failed := get("/api/releases/260929062507.0.0")
	var plain Release
	if failed.Code != 200 || json.Unmarshal(failed.Body.Bytes(), &plain) != nil || plain.Changes[1].Group != "" || plain.Changes[1].Type != "other" {
		t.Fatalf("lookup failure %d %s", failed.Code, failed.Body)
	}
}

func changeJSON(t *testing.T, body []byte, index int) json.RawMessage {
	t.Helper()
	var raw struct {
		Releases []struct {
			Changes []json.RawMessage `json:"changes"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	return raw.Releases[0].Changes[index]
}
