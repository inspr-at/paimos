// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"encoding/json"
	"errors"
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

func TestProductNotesProjectionAndImmutability(t *testing.T) {
	s := noteFixture()
	s.Frozen = true
	s.Tickets[0].Key = "AEON-7"
	s.Tickets[0].Group = GroupFixes
	s.Tickets[0].Fields = json.RawMessage(`{"pill_en":"Captured release notes","pill_de":"","benefit_en":"Preserve the release text.","benefit_de":"","private_comment":"NEVER EMBED INTERNAL COMMENTS"}`)
	hidden := s.Tickets[0]
	hidden.ID = "55555555-5555-4555-8555-555555555555"
	hidden.Key = "AEON-8"
	hidden.Fields = json.RawMessage(`{"hide_from_release_notes":true,"pill_en":"HIDDEN NOTE TEXT"}`)
	s.Tickets = append(s.Tickets, hidden)
	raw, _ := json.Marshal(s)
	notes, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes.Items) != 1 || notes.Items[0].Group != GroupFixes || notes.Items[0].PillDE != "" {
		t.Fatalf("notes %+v", notes)
	}
	bundle := EmptyProductNotes()
	if err := bundle.Add(notesVersion, notes); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Add(notesVersion, notes); err != nil {
		t.Fatal("identical export", err)
	}
	encoded, _ := json.Marshal(bundle)
	for _, private := range []string{s.TenantID, s.ProjectID, s.ReleaseID, s.Tickets[0].ID, "AEON-8", "HIDDEN", "INTERNAL", "private_comment", "fields", "hide_from_release_notes"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private projection: %s", private)
		}
	}
	if _, err := ReadProductNotes(encoded); err != nil {
		t.Fatal(err)
	}
	changed := notes
	changed.Revision++
	if err := bundle.Add(notesVersion, changed); err == nil {
		t.Fatal("overwrote immutable version")
	}
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.ProjectID, s.TenantID, false); err == nil {
		t.Fatal("accepted wrong ownership")
	}
	s.Frozen = false
	raw, _ = json.Marshal(s)
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, false); err == nil {
		t.Fatal("backfilled from live preview")
	}
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, true); err != nil {
		t.Fatal("explicit reservation", err)
	}
	s.Tickets[0].Fields = json.RawMessage(`{"hide_from_release_notes":"true","pill_en":"Must not become public"}`)
	raw, _ = json.Marshal(s)
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, true); err == nil {
		t.Fatal("invalid hide flag became public")
	}
	for _, bad := range []string{strings.Replace(string(encoded), `"items":`, `"internal_comment":"private","items":`, 1), strings.Replace(string(encoded), "AEON-7", "CLIENT-7", 1), string(encoded) + "{}"} {
		if _, err := ReadProductNotes([]byte(bad)); err == nil {
			t.Fatal("accepted invalid public bundle")
		}
	}
}

func TestProductNotesNeverReadLiveTickets(t *testing.T) {
	h := History{Product: "PAIMOS AEON", Repository: "inspr-at/aeon", Releases: []Release{{Version: notesVersion, Notes: &Notes{Source: ProductNotesSource, Items: []NoteItem{{Key: "AEON-7", Group: GroupFixes, PillEN: "Frozen fix", BenefitEN: "Captured benefit."}}}, Changes: []Change{{Commit: "a", Subject: "AEON-7: repair the release", Type: "other", Tickets: []string{"AEON-7"}}}}, {Version: "260927120000.0.0", Notes: MissingNotes(), Changes: []Change{{Commit: "b", Subject: "AEON-7: an older change", Type: "other", Tickets: []string{"AEON-7"}}}}}}
	mod := NewWith(h, notesVersion)
	mod.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		t.Fatal("read live ticket fields")
		return nil, nil
	})
	got := mod.annotated(t.Context(), h)
	if got.Releases[0].Changes[0].Group != GroupFixes || got.Releases[0].Changes[0].Linked[0].PillEN != "Frozen fix" {
		t.Fatal(got)
	}
	if got.Releases[1].Changes[0].Group != GroupOther || len(got.Releases[1].Changes[0].Linked) != 0 {
		t.Fatal("invented historical notes")
	}
	if h.Releases[0].Changes[0].Group != "" {
		t.Fatal("mutated shared history")
	}
	// Empty and hidden-only captures must never be filled with bundle members.
	for _, notes := range []*Notes{{Source: "database-snapshot", Items: []NoteItem{}}, {Source: "database-snapshot", Hidden: 1, Items: []NoteItem{}}} {
		h.Releases[0].Notes = notes
		bundle := EmptyProductNotes()
		bundle.Releases[notesVersion] = PublicNotes{Items: []TicketNote{{Key: "AEON-7", PillEN: "Portable"}}}
		if got := withProductNotes(h, bundle); got.Releases[0].Notes != notes {
			t.Fatal("empty capture replaced")
		}
	}
	h.Releases[0].Notes = MissingNotes()
	bundle := EmptyProductNotes()
	bundle.Releases[notesVersion] = PublicNotes{Items: []TicketNote{{Key: "AEON-7", PillEN: "Portable"}}}
	h.Product = "Other product"
	if got := withProductNotes(h, bundle); got.Releases[0].Notes.Source != "unavailable" {
		t.Fatal("notes crossed product boundary")
	}
}

func TestHistoryExportIgnoresLiveAnnotations(t *testing.T) {
	s := noteFixture()
	s.Tickets[0].Key = "AEON-7"
	raw, _ := json.Marshal(s)
	notes, err := NotesFromSnapshot(raw, notesVersion, "database-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	h := History{Schema: Schema, Product: "PAIMOS AEON", Repository: "inspr-at/aeon", Releases: []Release{
		{Version: notesVersion, Notes: notes, Changes: []Change{{Linked: []TicketNote{{Key: "AEON-7", PillEN: "LIVE EDIT NEVER EMBED"}}}}},
		{Version: "260927120000.0.0", Notes: MissingNotes(), Changes: []Change{{Linked: []TicketNote{{Key: "AEON-9", PillEN: "UNFROZEN NEVER EMBED"}}}}},
	}}
	bundle := EmptyProductNotes()
	if err := bundle.AddHistory(h); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(bundle)
	if len(bundle.Releases) != 1 || strings.Contains(string(encoded), "NEVER EMBED") || strings.Contains(string(encoded), s.Tickets[0].ID) {
		t.Fatal("live data or tenant ID entered public bundle")
	}
	h.Repository = "inspr-at/paimos"
	if err := bundle.AddHistory(h); err != nil {
		t.Fatal("paimos alias", err)
	}
	h.Repository = "another/product"
	if err := bundle.AddHistory(h); err == nil {
		t.Fatal("accepted another product")
	}
}

func TestPackNotesCommandReservesPublicProjection(t *testing.T) {
	dir := repo(t)
	s := noteFixture()
	s.Version = "260923143005.0.0" // fixture version.json
	s.Tickets[0].Key = "AEON-7"
	s.Tickets[0].Group = GroupFeatures
	raw, _ := json.Marshal(s)
	input := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./packnotes", "-repo", dir, "-snapshot", input, "-reserve", s.Version, "-tenant", s.TenantID, "-project", s.ProjectID)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("packnotes: %v %s", err, out)
	}
	built, err := Build(t.Context(), Options{Repo: dir, Repository: "inspr-at/aeon"})
	if err != nil {
		t.Fatal(err)
	}
	if got := built.Releases[0].Notes; got.Source != ProductNotesSource || len(got.Items) != 0 || len(got.PublicItems) != 1 || got.PublicItems[0].Group != GroupFeatures {
		t.Fatal(got)
	}
}

func TestBuildServesProductNotesForWorkflowRepository(t *testing.T) {
	dir := repo(t)
	const bundle = `{"schema":"aeon.product-release-notes.v1","product":"PAIMOS AEON","repository":"inspr-at/aeon","releases":{"260923143005.0.0":{"snapshot_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","captured_at":"2026-09-23T14:30:05Z","release_revision":1,"items":[{"key":"AEON-15","group":"features","pill_en":"Clear release notes","pill_de":"","benefit_en":"Read what changed.","benefit_de":""}]}}}`
	if _, err := ReadProductNotes([]byte(bundle)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProductNotes([]byte(strings.Replace(bundle, `"repository":"inspr-at/aeon"`, `"repository":"inspr-at/paimos"`, 1))); err != nil {
		t.Fatal("paimos bundle", err)
	}
	if _, err := ReadProductNotes([]byte(strings.Replace(bundle, "inspr-at/aeon", "other/repo", 1))); err == nil {
		t.Fatal("accepted a third repository")
	}
	path := filepath.Join(dir, ProductNotesPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Build(t.Context(), Options{Repo: dir, Repository: "inspr-at/paimos"})
	if err != nil {
		t.Fatal(err)
	}
	mod := NewWith(h, "260923143005.0.0")
	mux := http.NewServeMux()
	mod.Mount(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/releases/260923143005.0.0", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: "22222222-2222-4222-8222-222222222222", TenantID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var rel Release
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rel) != nil {
		t.Fatalf("serve %d %s", w.Code, w.Body)
	}
	if h.Repository != "inspr-at/paimos" || rel.Notes == nil || rel.Notes.Source != ProductNotesSource || len(rel.Notes.PublicItems) != 1 || rel.Notes.PublicItems[0].Key != "AEON-15" {
		t.Fatalf("dropped notes %+v", rel.Notes)
	}
	bare, err := Build(t.Context(), Options{Repo: dir, Repository: "other/repo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range bare.Releases {
		if release.Version == "260923143005.0.0" && HasSnapshot(release) {
			t.Fatal("foreign repository received product notes")
		}
	}
}

func TestGrouplessCaptureUsesLiveClassification(t *testing.T) {
	s := noteFixture()
	s.Tickets[0].Key = "AEON-7"
	s.Tickets[0].Group = ""
	feature := s.Tickets[0]
	feature.ID = "55555555-5555-4555-8555-555555555555"
	feature.Key = "AEON-9"
	feature.Position = 3
	feature.Fields = json.RawMessage(`{"pill_en":"Frozen feature","pill_de":"","benefit_en":"Captured feature.","benefit_de":""}`)
	kept := s.Tickets[0]
	kept.ID = "66666666-6666-4666-8666-666666666666"
	kept.Key = "AEON-8"
	kept.Group = GroupFeatures
	kept.Position = 1
	kept.Fields = json.RawMessage(`{"pill_en":"Frozen kept","pill_de":"","benefit_en":"Stays a feature.","benefit_de":""}`)
	s.Tickets = []NoteTicket{kept, s.Tickets[0], feature}
	raw, _ := json.Marshal(s)
	notes, err := NotesFromSnapshot(raw, notesVersion, "database-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	h := History{Schema: Schema, Product: "PAIMOS AEON", Repository: "inspr-at/paimos", Releases: []Release{{
		Version: notesVersion, Notes: notes,
		Changes: []Change{
			{Commit: "a", Subject: "AEON-7: repair the release", Type: "other", Tickets: []string{"AEON-7"}},
			{Commit: "b", Subject: "AEON-8: keep the frozen group", Type: "other", Tickets: []string{"AEON-8"}},
			{Commit: "c", Subject: "AEON-9: ship the notes", Type: "other", Tickets: []string{"AEON-9"}},
		},
	}}}
	mod := NewWith(h, notesVersion)
	var keys []string
	mod.UseTickets(func(_ context.Context, tenantID string, got []string) (map[string]TicketMeta, error) {
		if tenantID != "11111111-1111-4111-8111-111111111111" {
			t.Errorf("tenant %s", tenantID)
		}
		keys = append([]string{}, got...)
		return map[string]TicketMeta{
			"AEON-7": {Bug: true, Note: &TicketNote{PillEN: "LIVE TEXT", BenefitEN: "LIVE BENEFIT"}},
			"AEON-8": {Bug: true, Note: &TicketNote{PillEN: "LIVE KEPT", BenefitEN: "LIVE KEPT BENEFIT"}},
			"AEON-9": {PublicBenefit: true, Note: &TicketNote{PillEN: "LIVE FEATURE", BenefitEN: "LIVE FEATURE BENEFIT"}},
		}, nil
	})
	ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: "22222222-2222-4222-8222-222222222222", TenantID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person})
	got := mod.annotated(ctx, h)
	if strings.Join(keys, ",") != "AEON-7,AEON-9" {
		t.Fatalf("keys %v", keys)
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "LIVE") {
		t.Fatal("live text entered the response")
	}
	byKey := map[string]NoteItem{}
	for _, item := range got.Releases[0].Notes.Items {
		byKey[item.Key] = item
	}
	if byKey["AEON-7"].Group != GroupFixes || byKey["AEON-7"].PillEN != "Clear release notes" || byKey["AEON-9"].Group != GroupFeatures || byKey["AEON-9"].PillEN != "Frozen feature" || byKey["AEON-8"].Group != GroupFeatures || byKey["AEON-8"].PillEN != "Frozen kept" {
		t.Fatalf("items %+v", byKey)
	}
	for _, change := range got.Releases[0].Changes {
		if len(change.Tickets) != 1 {
			continue
		}
		want := GroupFeatures
		pill := "Frozen kept"
		if change.Tickets[0] == "AEON-7" {
			want, pill = GroupFixes, "Clear release notes"
		}
		if change.Tickets[0] == "AEON-9" {
			want, pill = GroupFeatures, "Frozen feature"
		}
		if change.Group != want || len(change.Linked) != 1 || change.Linked[0].PillEN != pill || change.Linked[0].Group != want {
			t.Fatalf("change %s %+v", change.Tickets[0], change)
		}
	}
	stored := map[string]string{}
	for _, item := range h.Releases[0].Notes.Items {
		stored[item.Key] = item.Group
	}
	if stored["AEON-7"] != "" || stored["AEON-9"] != "" || stored["AEON-8"] != GroupFeatures {
		t.Fatal("mutated the stored capture", stored)
	}
	bundle := EmptyProductNotes()
	if err := bundle.AddHistory(got); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(bundle)
	if strings.Contains(string(encoded), "LIVE") || strings.Contains(string(encoded), s.TenantID) {
		t.Fatal("export leaked live text or a tenant id")
	}
	saved := map[string]string{}
	for _, item := range bundle.Releases[notesVersion].Items {
		saved[item.Key] = item.Group + ":" + item.PillEN
	}
	if saved["AEON-7"] != GroupFixes+":Clear release notes" || saved["AEON-9"] != GroupFeatures+":Frozen feature" || saved["AEON-8"] != GroupFeatures+":Frozen kept" {
		t.Fatalf("bundle %v", saved)
	}
	failed := NewWith(h, notesVersion)
	failed.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		return nil, errors.New("lookup failed")
	})
	down := failed.annotated(ctx, h)
	for _, item := range down.Releases[0].Notes.Items {
		if item.Key == "AEON-7" && item.Group != "" {
			t.Fatal("lookup failure invented a group")
		}
	}
}

func TestGrouplessAndUncapturedCommitsMatchBaseGroups(t *testing.T) {
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	s := noteFixture()
	s.Tickets = []NoteTicket{
		{ID: "44444444-4444-4444-8444-444444444411", Key: "AEON-11", Position: 1, UpdatedAt: &at, Fields: json.RawMessage(`{"tags":["bug"],"pill_en":"Frozen bug","benefit_en":"Captured bug."}`)},
		{ID: "55555555-5555-4555-8555-555555555512", Key: "AEON-12", Position: 2, UpdatedAt: &at, Fields: json.RawMessage(`{"hide_from_release_notes":true,"tags":["bug"],"pill_en":"HIDDEN BUG TEXT","benefit_en":"HIDDEN BENEFIT"}`)},
		{ID: "66666666-6666-4666-8666-666666666613", Key: "AEON-13", Position: 3, UpdatedAt: &at, Fields: json.RawMessage(`{"tags":["bug"]}`)},
	}
	raw, _ := json.Marshal(s)
	notes, err := NotesFromSnapshot(raw, notesVersion, "database-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if len(notes.Items) != 1 || notes.Items[0].Key != "AEON-11" || notes.Items[0].Group != "" || notes.Hidden != 1 {
		t.Fatalf("capture %+v", notes)
	}
	frozen := noteFixture()
	frozen.Version = "260926120000.0.0"
	frozen.Tickets = []NoteTicket{
		{ID: "77777777-7777-4777-8777-777777777720", Key: "AEON-20", Position: 1, UpdatedAt: &at, Group: GroupFixes, Fields: json.RawMessage(`{"pill_en":"Frozen kept","benefit_en":"Stays a fix."}`)},
		{ID: "77777777-7777-4777-8777-777777777721", Key: "AEON-21", Position: 2, UpdatedAt: &at, Group: GroupFeatures, Fields: json.RawMessage(`{"pill_en":"Frozen feature","benefit_en":"Stays a feature."}`)},
		{ID: "77777777-7777-4777-8777-777777777798", Key: "AEON-98", Position: 3, UpdatedAt: &at, Group: GroupFixes, Fields: json.RawMessage(`{"hide_from_release_notes":true,"tags":["bug"],"pill_en":"HIDDEN GROUPED BUG","benefit_en":"HIDDEN GROUPED BENEFIT"}`)},
	}
	raw, _ = json.Marshal(frozen)
	kept, err := NotesFromSnapshot(raw, frozen.Version, "database-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Hidden != 1 || len(kept.Items) != 2 || kept.Items[0].Key != "AEON-20" || kept.Items[1].Key != "AEON-21" {
		t.Fatalf("grouped capture %+v", kept)
	}
	h := History{Schema: Schema, Product: "PAIMOS AEON", Repository: "inspr-at/paimos", Releases: []Release{
		{Version: notesVersion, Notes: notes, Changes: []Change{
			{Commit: "a", Subject: "AEON-11: told bug", Type: "other", Tickets: []string{"AEON-11"}},
			{Commit: "b", Subject: "AEON-12: hidden bug", Type: "other", Tickets: []string{"AEON-12"}},
			{Commit: "c", Subject: "AEON-13: textless bug", Type: "other", Tickets: []string{"AEON-13"}},
			{Commit: "d", Subject: "AEON-14: not a member", Type: "other", Tickets: []string{"AEON-14"}},
		}},
		{Version: "260927120000.0.0", Notes: MissingNotes(), Changes: []Change{
			{Commit: "e", Subject: "AEON-15: bug without a capture", Type: "other", Tickets: []string{"AEON-15"}},
			{Commit: "f", Subject: "AEON-16: benefit without a capture", Type: "other", Tickets: []string{"AEON-16"}},
		}},
		{Version: frozen.Version, Notes: kept, Changes: []Change{
			{Commit: "g", Subject: "AEON-20: frozen group", Type: "other", Tickets: []string{"AEON-20"}},
			{Commit: "h", Subject: "AEON-99: outside a grouped capture", Type: "other", Tickets: []string{"AEON-99"}},
			{Commit: "i", Subject: "AEON-97: benefit outside a grouped capture", Type: "other", Tickets: []string{"AEON-97"}},
			{Commit: "j", Subject: "AEON-98: hidden member of a grouped capture", Type: "other", Tickets: []string{"AEON-98"}},
			{Commit: "k", Subject: "AEON-21: shared with a hidden bug", Type: "other", Tickets: []string{"AEON-21", "AEON-98"}},
		}},
	}}
	liveNote := func(pill string) *TicketNote {
		return &TicketNote{PillEN: pill, BenefitEN: "LIVE BENEFIT"}
	}
	live := map[string]TicketMeta{
		"AEON-11": {Bug: true, Note: liveNote("LIVE TOLD")},
		"AEON-12": {Bug: true, Note: liveNote("LIVE HIDDEN")},
		"AEON-13": {Bug: true, Note: liveNote("LIVE TEXTLESS")},
		"AEON-14": {PublicBenefit: true, Note: liveNote("LIVE NONMEMBER")},
		"AEON-15": {Bug: true, Note: liveNote("LIVE NOCAPTURE")},
		"AEON-16": {PublicBenefit: true, Note: liveNote("LIVE BENEFIT ONLY")},
		"AEON-20": {PublicBenefit: true, Note: liveNote("LIVE KEPT")},
		"AEON-21": {PublicBenefit: true, Note: liveNote("LIVE TOLD FEATURE")},
		"AEON-97": {PublicBenefit: true, Note: liveNote("LIVE OUTSIDE BENEFIT")},
		"AEON-98": {Bug: true, Note: liveNote("LIVE HIDDEN GROUPED")},
		"AEON-99": {Bug: true, Note: liveNote("LIVE OUTSIDE")},
	}
	// The base classified the same live facts with GroupChange, including note text.
	base := withGroups(h, live)
	mod := NewWith(h, notesVersion)
	var keys []string
	mod.UseTickets(func(_ context.Context, tenantID string, got []string) (map[string]TicketMeta, error) {
		if tenantID != "11111111-1111-4111-8111-111111111111" {
			t.Errorf("tenant %s", tenantID)
		}
		keys = append([]string{}, got...)
		return live, nil
	})
	ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: "22222222-2222-4222-8222-222222222222", TenantID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person})
	got := mod.annotated(ctx, h)
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
	}
	for _, key := range []string{"AEON-11", "AEON-12", "AEON-13", "AEON-14", "AEON-15", "AEON-16"} {
		if !seen[key] {
			t.Fatalf("lookup missed %s in %v", key, keys)
		}
	}
	for _, key := range []string{"AEON-97", "AEON-98", "AEON-99"} {
		if !seen[key] {
			t.Fatalf("lookup missed %s, a ticket the grouped capture does not name: %v", key, keys)
		}
	}
	if seen["AEON-20"] || seen["AEON-21"] {
		t.Fatalf("lookup re-read a ticket the capture already grouped: %v", keys)
	}
	body, _ := json.Marshal(got)
	for _, leaked := range []string{"LIVE", "HIDDEN"} {
		if strings.Contains(string(body), leaked) {
			t.Fatalf("live text %q entered the response", leaked)
		}
	}
	byCommit := func(rel Release) map[string]Change {
		out := map[string]Change{}
		for _, change := range rel.Changes {
			out[change.Commit] = change
		}
		return out
	}
	// The base is live classification of every commit ticket, including note
	// text. A grouped capture keeps a group it already stored. Tickets it does
	// not name take the base group and drop the note.
	for i, rel := range got.Releases[:2] {
		want := byCommit(base.Releases[i])
		for _, change := range rel.Changes {
			if change.Group != want[change.Commit].Group {
				t.Fatalf("commit %s group %q, base %q", change.Commit, change.Group, want[change.Commit].Group)
			}
		}
	}
	groupless := byCommit(got.Releases[0])
	if groupless["a"].Group != GroupFixes || len(groupless["a"].Linked) != 1 || groupless["a"].Linked[0].PillEN != "Frozen bug" {
		t.Fatalf("told bug %+v", groupless["a"])
	}
	if groupless["b"].Group != GroupFixes || groupless["b"].Linked != nil || groupless["c"].Group != GroupFixes || groupless["c"].Linked != nil {
		t.Fatalf("hidden/textless %+v %+v", groupless["b"], groupless["c"])
	}
	if groupless["d"].Group != GroupFeatures || groupless["d"].Linked != nil {
		t.Fatalf("non-member %+v", groupless["d"])
	}
	plain := byCommit(got.Releases[1])
	if plain["e"].Group != GroupFixes || plain["e"].Linked != nil || plain["f"].Group != GroupFeatures || plain["f"].Linked != nil {
		t.Fatalf("no capture %+v %+v", plain["e"], plain["f"])
	}
	grouped := byCommit(got.Releases[2])
	baseGrouped := byCommit(base.Releases[2])
	if grouped["g"].Group != GroupFixes || len(grouped["g"].Linked) != 1 || grouped["g"].Linked[0].PillEN != "Frozen kept" || grouped["g"].Linked[0].Group != GroupFixes {
		t.Fatalf("frozen group %+v", grouped["g"])
	}
	if grouped["g"].Group == baseGrouped["g"].Group {
		t.Fatal("frozen group followed the live classification")
	}
	for _, commit := range []string{"h", "i", "j", "k"} {
		if grouped[commit].Group != baseGrouped[commit].Group || len(baseGrouped[commit].Linked) == 0 {
			t.Fatalf("commit %s group %q, base %q, linked %d", commit, grouped[commit].Group, baseGrouped[commit].Group, len(baseGrouped[commit].Linked))
		}
	}
	if grouped["h"].Group != GroupFixes || grouped["h"].Linked != nil || grouped["j"].Group != GroupFixes || grouped["j"].Linked != nil {
		t.Fatalf("hidden and non-member bugs %+v %+v", grouped["h"], grouped["j"])
	}
	if grouped["i"].Group != GroupFeatures || grouped["i"].Linked != nil {
		t.Fatalf("non-member benefit %+v", grouped["i"])
	}
	if grouped["k"].Group != GroupFixes || len(grouped["k"].Linked) != 1 || grouped["k"].Linked[0].Key != "AEON-21" || grouped["k"].Linked[0].PillEN != "Frozen feature" || grouped["k"].Linked[0].Group != GroupFeatures {
		t.Fatalf("shared hidden bug %+v", grouped["k"])
	}
	if h.Releases[0].Notes.Items[0].Group != "" || h.Releases[2].Notes.Hidden != 1 || len(h.Releases[2].Notes.Items) != 2 || h.Releases[2].Notes.Items[0].Group != GroupFixes || h.Releases[2].Notes.Items[1].Group != GroupFeatures {
		t.Fatal("mutated the stored capture")
	}
	failed := NewWith(h, notesVersion)
	failed.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		return nil, errors.New("lookup failed")
	})
	down := failed.annotated(ctx, h)
	downGrouped := byCommit(down.Releases[2])
	if byCommit(down.Releases[0])["b"].Group == GroupFixes || down.Releases[0].Notes.Items[0].Group != "" || downGrouped["h"].Group == GroupFixes || downGrouped["i"].Group == GroupFeatures || downGrouped["j"].Group == GroupFixes || downGrouped["k"].Group == GroupFixes {
		t.Fatal("lookup failure invented a group")
	}
}

func TestPackNotesSnapshotsAndHistory(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "packnotes")
	if out, err := exec.Command("go", "build", "-o", bin, "./packnotes").CombinedOutput(); err != nil {
		t.Fatalf("build packnotes: %v %s", err, out)
	}
	run := func(dir string, args ...string) ([]byte, error) {
		return exec.Command(bin, append([]string{"-repo", dir}, args...)...).CombinedOutput()
	}
	read := func(dir string) ProductNotes {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, ProductNotesPath))
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := ReadProductNotes(raw)
		if err != nil {
			t.Fatalf("bundle: %v %s", err, raw)
		}
		return bundle
	}
	dir := repo(t)
	s := noteFixture()
	s.Frozen = true
	s.Version = notesVersion
	s.Tickets[0].Key = "AEON-7"
	s.Tickets[0].Group = GroupFixes
	raw, _ := json.Marshal(s)
	snapDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(snapDir, notesVersion+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	plain := s
	plain.Tickets = append([]NoteTicket(nil), s.Tickets...)
	plain.Version = "260928130000.0.0"
	plain.Tickets[0].Group = ""
	plain.Tickets[0].Key = "AEON-9"
	raw, _ = json.Marshal(plain)
	if err := os.WriteFile(filepath.Join(snapDir, plain.Version+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(dir, "-snapshots", snapDir); err == nil {
		t.Fatalf("snapshots without binding: %s", out)
	}
	if out, err := run(dir, "-snapshots", snapDir, "-tenant", "99999999-9999-4999-8999-999999999999", "-project", s.ProjectID); err == nil {
		t.Fatalf("foreign snapshot accepted: %s", out)
	}
	if out, err := run(dir, "-snapshots", snapDir, "-tenant", s.TenantID, "-project", s.ProjectID); err != nil {
		t.Fatalf("snapshots: %v %s", err, out)
	}
	snapped := read(dir)
	if snapped.Releases[notesVersion].Items[0].Group != GroupFixes || snapped.Releases[plain.Version].Items[0].Key != "AEON-9" || snapped.Releases[plain.Version].Items[0].Group != "" {
		t.Fatalf("snapshots %+v", snapped.Releases)
	}

	dir = repo(t)
	s = noteFixture()
	s.Frozen = true
	s.Tickets[0].Key = "AEON-7"
	s.Tickets[0].Group = GroupFixes
	raw, _ = json.Marshal(s)
	notes, err := NotesFromSnapshot(raw, notesVersion, "database-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	h := History{Schema: Schema, Product: "PAIMOS AEON", Repository: "inspr-at/paimos", Releases: []Release{{
		Version: notesVersion, Notes: notes,
		Changes: []Change{{Commit: "a", Subject: "AEON-7: repair the release", Type: "other", Tickets: []string{"AEON-7"}, Linked: []TicketNote{{Key: "AEON-7", PillEN: "LIVE TEXT NEVER EMBED", BenefitEN: "LIVE BENEFIT"}}}},
	}}}
	writeHistory := func(tenantID string) string {
		t.Helper()
		payload := struct {
			History
			TenantID  string `json:"tenant_id"`
			ProjectID string `json:"project_node_id"`
			Current   string `json:"current"`
			LiveSince string `json:"live_since"`
		}{History: h, TenantID: tenantID, ProjectID: s.ProjectID, Current: notesVersion, LiveSince: "2026-09-28T12:00:00Z"}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "history.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	historyPath := writeHistory(s.TenantID)
	if out, err := run(dir, "-history", historyPath); err == nil {
		t.Fatalf("history without binding: %s", out)
	}
	if out, err := run(dir, "-history", writeHistory(""), "-tenant", s.TenantID, "-project", s.ProjectID); err == nil || !strings.Contains(string(out), "does not match the explicitly selected tenant and project") {
		t.Fatalf("unbound history: %v %s", err, out)
	}
	if out, err := run(dir, "-history", writeHistory("99999999-9999-4999-8999-999999999999"), "-tenant", s.TenantID, "-project", s.ProjectID); err == nil {
		t.Fatalf("foreign history accepted: %s", out)
	}
	if out, err := run(dir, "-history", historyPath, "-tenant", s.TenantID, "-project", s.ProjectID); err != nil {
		t.Fatalf("history: %v %s", err, out)
	}
	exported := read(dir)
	raw, err = os.ReadFile(filepath.Join(dir, ProductNotesPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "LIVE") || strings.Contains(string(raw), s.TenantID) {
		t.Fatal("history export kept live text or a tenant id")
	}
	if len(exported.Releases[notesVersion].Items) != 1 || exported.Releases[notesVersion].Items[0].Group != GroupFixes || exported.Releases[notesVersion].Items[0].PillEN != "Clear release notes" {
		t.Fatalf("history bundle %+v", exported.Releases[notesVersion])
	}
}
