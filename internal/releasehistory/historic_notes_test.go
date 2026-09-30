// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

const historicTenant = "11111111-1111-4111-8111-111111111111"
const historicProject = "22222222-2222-4222-8222-222222222222"

func TestHistoricProductBundleIsShipped(t *testing.T) {
	raw, err := data.ReadFile("data/product-notes.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := ReadProductNotes(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The reported historic release must contain notes even without any PPM DB.
	if len(bundle.Releases["260929193046.0.0"].Items) == 0 {
		t.Fatal("stable107 historic notes are missing")
	}
	groups := map[string]bool{}
	for _, note := range bundle.Releases["260929113854.0.0"].Items {
		groups[note.Group] = true
	}
	if !groups[GroupFeatures] || !groups[GroupFixes] {
		t.Fatal("stable105 backfilled features or fixes are missing")
	}
	for _, tc := range []struct {
		version string
		present []string
		absent  []string
	}{
		{version: "260929203122.0.0", present: []string{"AEON-367"}},
		{version: "260929215406.0.0", absent: []string{"AEON-367"}},
		{version: "260929232203.0.0", present: []string{"AEON-357", "AEON-358", "AEON-365", "AEON-371"}, absent: []string{"AEON-351", "AEON-373"}},
	} {
		notes, ok := bundle.Releases[tc.version]
		if !ok {
			t.Fatalf("historic notes missing for %s", tc.version)
		}
		keys := map[string]bool{}
		for _, item := range notes.Items {
			keys[item.Key] = true
		}
		for _, key := range tc.present {
			if !keys[key] {
				t.Errorf("%s missing from %s", key, tc.version)
			}
		}
		for _, key := range tc.absent {
			if keys[key] {
				t.Errorf("%s incorrectly included in %s", key, tc.version)
			}
		}
	}
}

func historicFixture(t *testing.T) (History, []byte) {
	t.Helper()
	h := History{Schema: Schema, Product: "PAIMOS AEON", Repository: "inspr-at/paimos", Releases: []Release{
		{Version: notesVersion, State: StatePublished, Tickets: []string{"AEON-1", "AEON-1"}, Notes: MissingNotes(), Changes: []Change{
			{Commit: "aaa", Subject: "AEON-2: repair", Tickets: []string{"AEON-2", "AEON-1"}},
			{Commit: "bbb", Subject: "AEON-3: internal work", Tickets: []string{"AEON-3"}},
			{Commit: "ccc", Subject: "AEON-4: maintenance", Tickets: []string{"AEON-4"}},
			{Commit: "ddd", Subject: "AEON-5: bug without notes", Tickets: []string{"AEON-5"}},
		}},
		{Version: "260923134631.0.0", State: StateReserved, Tickets: []string{"AEON-99"}, Notes: MissingNotes()},
	}}
	export := HistoricTicketExport{Schema: HistoricNotesSchema, TenantID: historicTenant, ProjectID: historicProject, CapturedAt: time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC), Tickets: []HistoricTicket{
		{Key: "AEON-1", Kind: "ticket", Fields: json.RawMessage(`{"pill_en":"Historic feature","pill_de":"Historisches Feature","benefit_en":"Existing benefit.","benefit_de":"Bestehender Nutzen.","private":"DO NOT EMBED"}`)},
		{Key: "AEON-2", Kind: "ticket", Fields: json.RawMessage(`{"tags":[{"name":"bug"}],"pill_en":"Historic fix","benefit_en":"Existing fix benefit."}`)},
		{Key: "AEON-3", Kind: "bug", Fields: json.RawMessage(`{"hide_from_release_notes":true,"pill_en":"HIDDEN TEXT","benefit_en":"HIDDEN BENEFIT"}`)},
		{Key: "AEON-4", Kind: "ticket", Fields: json.RawMessage(`{}`)},
		{Key: "AEON-5", Kind: "bug", Fields: json.RawMessage(`{}`)},
	}}
	raw, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	return h, raw
}

func TestHistoricNotesProjection(t *testing.T) {
	h, raw := historicFixture(t)
	bundle := EmptyProductNotes()
	report, err := bundle.AddHistoric(h, raw, historicTenant, historicProject)
	if err != nil {
		t.Fatal(err)
	}
	if report != (HistoricReport{Releases: 1, Classified: 2, Other: 2, Hidden: 1}) {
		t.Fatalf("report %+v", report)
	}
	if got := strings.Join(HistoricTicketKeys(h), ","); got != "AEON-1,AEON-2,AEON-3,AEON-4,AEON-5" {
		t.Fatal(got)
	}
	notes := bundle.Releases[notesVersion]
	if !notes.WrittenAfterRelease || len(notes.Items) != 2 || notes.Items[0].Group != GroupFeatures || notes.Items[1].Group != GroupFixes {
		t.Fatalf("notes %+v", notes)
	}
	if notes.Items[1].PillDE != "" || notes.Items[1].BenefitDE != "" {
		t.Fatal("fabricated translation")
	}
	encoded, _ := json.Marshal(bundle)
	for _, forbidden := range []string{"HIDDEN", "DO NOT EMBED", historicTenant, historicProject, "AEON-3", "AEON-4", "AEON-5", "private", "fields"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public bundle contains %s", forbidden)
		}
	}
	if _, err := ReadProductNotes(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.AddHistoric(h, raw, historicTenant, historicProject); err != nil {
		t.Fatal("identical rerun", err)
	}
	changed := strings.Replace(string(raw), "Existing benefit.", "Changed benefit.", 1)
	if _, err := bundle.AddHistoric(h, []byte(changed), historicTenant, historicProject); err == nil {
		t.Fatal("overwrote an existing capture")
	}
	if bundle.Releases[notesVersion].Items[0].BenefitEN != "Existing benefit." {
		t.Fatal("failed import mutated bundle")
	}
}

func TestHistoricNotesFailClosed(t *testing.T) {
	h, raw := historicFixture(t)
	for name, mutate := range map[string]func(*HistoricTicketExport){
		"tenant":    func(e *HistoricTicketExport) { e.TenantID = historicProject },
		"project":   func(e *HistoricTicketExport) { e.ProjectID = historicTenant },
		"missing":   func(e *HistoricTicketExport) { e.Tickets = e.Tickets[:1] },
		"duplicate": func(e *HistoricTicketExport) { e.Tickets = append(e.Tickets, e.Tickets[0]) },
		"hide-string": func(e *HistoricTicketExport) {
			e.Tickets[0].Fields = json.RawMessage(`{"hide_from_release_notes":"true"}`)
		},
		"hide-null": func(e *HistoricTicketExport) {
			e.Tickets[0].Fields = json.RawMessage(`{"hide_from_release_notes":null}`)
		},
		"fields":      func(e *HistoricTicketExport) { e.Tickets[0].Fields = json.RawMessage(`null`) },
		"timestamp":   func(e *HistoricTicketExport) { e.CapturedAt = time.Time{} },
		"foreign-key": func(e *HistoricTicketExport) { e.Tickets[0].Key = "CLIENT-1" },
	} {
		t.Run(name, func(t *testing.T) {
			var e HistoricTicketExport
			_ = json.Unmarshal(raw, &e)
			mutate(&e)
			bad, _ := json.Marshal(e)
			bundle := EmptyProductNotes()
			if _, err := bundle.AddHistoric(h, bad, historicTenant, historicProject); err == nil {
				t.Fatal("accepted invalid export")
			}
			if len(bundle.Releases) != 0 {
				t.Fatal("partly applied invalid export")
			}
		})
	}
	// Existing authoritative snapshots, even empty ones, retain precedence.
	h.Releases[0].Notes = &Notes{Source: "database-snapshot", Items: []NoteItem{}}
	bundle := EmptyProductNotes()
	if report, err := bundle.AddHistoric(h, raw, historicTenant, historicProject); err != nil || report.Releases != 0 {
		t.Fatalf("snapshot overwritten: %+v %v", report, err)
	}
	h.Repository = "another/product"
	if _, err := bundle.AddHistoric(h, raw, historicTenant, historicProject); err == nil {
		t.Fatal("accepted another product")
	}
}

// Browser fixture uses the real historic importer, Git history builder and HTTP
// handler. This tenant has no AEON project, snapshots or live ticket metadata.
func TestHistoricNotesNonPPMTenant(t *testing.T) {
	dir := repo(t)
	for _, args := range [][]string{
		{"commit", "--allow-empty", "-qm", "AEON-13: historic feature implementation"},
		{"commit", "--allow-empty", "-qm", "AEON-15: historic fix implementation"},
		{"commit", "--allow-empty", "-qm", "AEON-4: maintenance without benefit text"},
		{"tag", "-a", "v260923160000.0.0", "-m", "PAIMOS AEON v260923160000.0.0 · inspr-calendar-v2 · stable · release_sequence 3 · Historic release"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Build(t.Context(), Options{Repo: dir, Repository: "inspr-at/aeon"})
	if err != nil {
		t.Fatal(err)
	}
	_, raw := historicFixture(t)
	var export HistoricTicketExport
	_ = json.Unmarshal(raw, &export)
	export.Tickets = append(export.Tickets[:2], export.Tickets[3])
	export.Tickets[0].Key = "AEON-13"
	export.Tickets[1].Key = "AEON-15"
	raw, _ = json.Marshal(export)
	bundle := EmptyProductNotes()
	if _, err := bundle.AddHistoric(h, raw, historicTenant, historicProject); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ProductNotesPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(bundle)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	h, err = Build(t.Context(), Options{Repo: dir, Repository: "inspr-at/aeon"})
	if err != nil {
		t.Fatal(err)
	}
	mod := NewWith(h, "260923160000.0.0")
	mod.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		return map[string]TicketMeta{}, nil
	})
	mux := http.NewServeMux()
	mod.Mount(mux)
	req := httptest.NewRequest("GET", "/api/releases", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: historicTenant, TenantID: "33333333-3333-4333-8333-333333333333", Kind: tenant.Person}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("HTTP %d", w.Code)
	}
	var response Response
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	groups := map[string]bool{}
	for _, rel := range response.Releases {
		if rel.State == StateReserved {
			continue
		}
		if rel.Notes.Source != ProductNotesSource || !rel.Notes.WrittenAfterRelease {
			t.Fatal("lost historic provenance")
		}
		for _, note := range rel.Notes.PublicItems {
			groups[note.Group] = true
		}
	}
	if !groups[GroupFeatures] || !groups[GroupFixes] {
		t.Fatalf("historic groups missing: %v", groups)
	}
	latest := response.Releases[0]
	if len(latest.Notes.PublicItems) != 2 || latest.Changes[0].Group != GroupOther {
		t.Fatalf("historic release lost its two notes or Other change: %+v", latest)
	}
	if out := os.Getenv("AEON_HISTORIC_FIXTURE_OUT"); out != "" {
		if err := os.WriteFile(out, w.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
