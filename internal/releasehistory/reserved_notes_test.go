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

func reservedFixture(t *testing.T) (NoteReservation, []byte) {
	t.Helper()
	r := NoteReservation{Version: "260930160000.0.0", Channel: "stable", Sequence: 114, Tickets: []string{"AEON-1", "AEON-2", "AEON-3"}}
	e := HistoricTicketExport{Schema: HistoricNotesSchema, TenantID: historicTenant, ProjectID: historicProject, CapturedAt: time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC), Reservation: &r, Tickets: []HistoricTicket{
		{Key: "AEON-1", Kind: "ticket", Fields: json.RawMessage(`{"pill_en":"Own release feature","pill_de":"Eigene neue Funktion","benefit_en":"This release carries its features.","benefit_de":"Dieses Release enthält seine Funktionen."}`)},
		{Key: "AEON-2", Kind: "ticket", Fields: json.RawMessage(`{"tags":["bug"],"pill_en":"Own release fix","pill_de":"Eigene neue Fehlerbehebung","benefit_en":"This release carries its fixes.","benefit_de":"Dieses Release enthält seine Fehlerbehebungen."}`)},
		{Key: "AEON-3", Kind: "ticket", Fields: json.RawMessage(`{"hide_from_release_notes":true,"pill_en":"HIDDEN NOTE","benefit_en":"HIDDEN BENEFIT"}`)},
	}}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return r, raw
}

func TestReservedNotesFreezeOwnRelease(t *testing.T) {
	r, raw := reservedFixture(t)
	b := EmptyProductNotes()
	if err := b.AddReserved(raw, r, historicTenant, historicProject); err != nil {
		t.Fatal(err)
	}
	n := b.Releases[r.Version]
	if n.WrittenAfterRelease || n.ReleaseSequence != 114 || len(n.Items) != 2 || n.Items[0].Group != GroupFeatures || n.Items[1].Group != GroupFixes {
		t.Fatalf("own capture: %+v", n)
	}
	if err := b.AddReserved(raw, r, historicTenant, historicProject); err != nil {
		t.Fatal("identical rerun", err)
	}
	if err := b.AddReserved([]byte(strings.Replace(string(raw), "carries its features", "changes its features", 1)), r, historicTenant, historicProject); err == nil {
		t.Fatal("changed frozen notes")
	}
	encoded, _ := json.Marshal(b)
	for _, forbidden := range []string{"HIDDEN", "AEON-3", historicTenant, historicProject} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("private data in capture:", forbidden)
		}
	}
	if _, err := b.AddHistoric(History{Schema: Schema, Product: b.Product, Repository: b.Repository}, raw, historicTenant, historicProject); err == nil {
		t.Fatal("reservation accepted as historical backfill")
	}
}

func TestReservedNotesFailClosed(t *testing.T) {
	r, raw := reservedFixture(t)
	for name, mutate := range map[string]func(*HistoricTicketExport){
		"version":  func(e *HistoricTicketExport) { e.Reservation.Version = notesVersion },
		"sequence": func(e *HistoricTicketExport) { e.Reservation.Sequence-- },
		"scope":    func(e *HistoricTicketExport) { e.Reservation.Tickets = e.Reservation.Tickets[:1] },
		"missing":  func(e *HistoricTicketExport) { e.Tickets = e.Tickets[:1] },
		"extra": func(e *HistoricTicketExport) {
			e.Tickets = append(e.Tickets, HistoricTicket{Key: "AEON-4", Kind: "ticket", Fields: json.RawMessage(`{}`)})
		},
		"translation": func(e *HistoricTicketExport) {
			e.Tickets[0].Fields = json.RawMessage(`{"pill_en":"An English pill","benefit_en":"English only."}`)
		},
		"hide": func(e *HistoricTicketExport) {
			e.Tickets[0].Fields = json.RawMessage(`{"hide_from_release_notes":"true"}`)
		},
		"tenant": func(e *HistoricTicketExport) { e.TenantID = historicProject },
	} {
		t.Run(name, func(t *testing.T) {
			var e HistoricTicketExport
			_ = json.Unmarshal(raw, &e)
			mutate(&e)
			bad, _ := json.Marshal(e)
			b := EmptyProductNotes()
			if b.AddReserved(bad, r, historicTenant, historicProject) == nil {
				t.Fatal("accepted bad reservation")
			}
			if len(b.Releases) != 0 {
				t.Fatal("partly applied reservation")
			}
		})
	}
}

// Freeze before tagging, then build and serve the same release without any PPM
// project or ticket data. Publication never needs to fetch new note text.
func TestReservedNotesNonPPMTenant(t *testing.T) {
	dir := repo(t)
	r, raw := reservedFixture(t)
	version, _ := json.Marshal(map[string]any{"product": "PAIMOS AEON", "version_scheme": SchemeCalVer3, "version": r.Version, "release_channel": r.Channel, "release_sequence": r.Sequence})
	if err := os.WriteFile(filepath.Join(dir, "version.json"), version, 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := ReadNoteReservation(dir, r.Version, []string{"AEON-3", "AEON-2", "AEON-1"})
	if err != nil {
		t.Fatal(err)
	}
	b := EmptyProductNotes()
	if err := b.AddReserved(raw, selected, historicTenant, historicProject); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ProductNotesPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	packed, _ := json.Marshal(b)
	if err := os.WriteFile(path, packed, 0600); err != nil {
		t.Fatal(err)
	}
	// A separate review record travels with the original capture; the raw
	// frozen bundle and its digest are not rewritten by a wording correction.
	reviewed := "This release carries its fixes."
	layer, _ := json.Marshal(map[string]any{"schema": "aeon.product-note-corrections.v1", "corrections": []NoteCorrection{{Version: r.Version, Key: "AEON-2", SHA256: b.Releases[r.Version].SHA256, Reason: "Reviewed repair wording.", BenefitEN: &reviewed}}})
	if err := os.WriteFile(filepath.Join(dir, NoteCorrectionsPath), layer, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "version.json", ProductNotesPath, NoteCorrectionsPath}, {"commit", "-qm", "AEON-1 AEON-2 AEON-3: own release"}, {"tag", "-a", "v" + r.Version, "-m", "PAIMOS AEON v" + r.Version + " · inspr-calver-3 · stable · release_sequence 114 · Own release"}} {
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
	m := NewWith(h, r.Version)
	m.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		return map[string]TicketMeta{}, nil
	})
	mux := http.NewServeMux()
	m.Mount(mux)
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
	n := response.Releases[0].Notes
	if response.Current != r.Version || n.Source != ProductNotesSource || n.WrittenAfterRelease || len(n.PublicItems) != 2 || n.PublicItems[0].PillDE == "" || n.PublicItems[1].Group != GroupFixes {
		t.Fatalf("lost own bilingual notes: %+v", n)
	}
	if len(n.Corrections) != 1 || n.Corrections[0].Reason != "Reviewed repair wording." {
		t.Fatal("lost correction audit trail")
	}
	if out := os.Getenv("AEON_RESERVED_FIXTURE_OUT"); out != "" {
		if err := os.WriteFile(out, w.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
