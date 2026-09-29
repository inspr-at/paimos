// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"strings"
	"testing"
	"time"
)

func TestProjectPublicReleaseWhitelist(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{
		"schema":"aeon.release-note-snapshot.v1",
		"version":"260926120000.0.0",
		"frozen":true,
		"tenant_id":"SECRET-TENANT",
		"project_node_id":"SECRET-PROJECT",
		"release_node_id":"SECRET-RELEASE",
		"tickets":[
			{"id":"SECRET-ID","key":"TKT-91","unavailable":"","fields":{"pill_en":"Clear morning notes","pill_de":"Klare Morgennotizen","benefit_en":"You can see what shipped.","benefit_de":"Sichtbar, was geliefert wurde.","hide_from_release_notes":false,"internal_note":"SECRET-INTERNAL"}},
			{"id":"SECRET-HIDDEN-ID","key":"TKT-2","unavailable":"","fields":{"pill_en":"Keep this private","pill_de":"Nicht öffentlich zeigen","benefit_en":"SECRET-HIDDEN-BENEFIT","benefit_de":"SECRET-HIDDEN-BENEFIT","hide_from_release_notes":true}},
			{"id":"SECRET-GAP-ID","key":"TKT-6","unavailable":"","fields":{"pill_en":"Nope","pill_de":"Nein danke","benefit_en":"SECRET-GAP-NOTE","benefit_de":"SECRET-GAP-NOTE","hide_from_release_notes":false}},
			{"id":"SECRET-GONE","key":"TKT-7","unavailable":"Member is unavailable.","fields":{"pill_en":"Still secret","pill_de":"Noch geheim","benefit_en":"SECRET-UNAVAILABLE","benefit_de":"SECRET-UNAVAILABLE","hide_from_release_notes":false}},
			{"id":"SECRET-MARKUP","key":"TKT-8","unavailable":"","fields":{"pill_en":"Clear <markup> notes","pill_de":"Klare Notizen","benefit_en":"You can see what shipped.","benefit_de":"Sichtbar, was geliefert wurde.","hide_from_release_notes":false}}
		]
	}`)
	version := "260926120000.0.0"
	rel, ok := projectPublicRelease(&version, at, raw)
	if !ok || rel.Version != version || rel.ReleasedAt != "2026-09-26T12:00:00Z" || len(rel.Notes) != 2 {
		t.Fatalf("projection: ok=%v %+v", ok, rel)
	}
	if rel.Notes[0].PillEN != "Clear morning notes" || rel.Notes[0].BenefitDE != "Sichtbar, was geliefert wurde." {
		t.Fatalf("visible note: %+v", rel.Notes[0])
	}
	if rel.Notes[1].PillEN != "" || rel.Notes[1].BenefitEN != "You can see what shipped." {
		t.Fatalf("markup note: %+v", rel.Notes[1])
	}
	encoded := rel.ReleasedAt + rel.Version + rel.Notes[0].PillEN + rel.Notes[0].PillDE + rel.Notes[0].BenefitEN + rel.Notes[0].BenefitDE + rel.Notes[1].PillDE
	for _, secret := range []string{"TKT-91", "TKT-2", "TKT-6", "SECRET-INTERNAL", "SECRET-HIDDEN-BENEFIT", "SECRET-GAP-NOTE", "SECRET-UNAVAILABLE", "SECRET-TENANT", "hide_from_release_notes", "unavailable"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("leaked %s in %+v", secret, rel)
		}
	}

	mismatch := "260916120000.0.0"
	if _, ok := projectPublicRelease(&mismatch, at, raw); ok {
		t.Fatal("version mismatch was published")
	}
	if rel, ok := projectPublicRelease(nil, at, raw); !ok || rel.Version != version {
		t.Fatalf("snapshot version: ok=%v %+v", ok, rel)
	}
	unversioned := []byte(`{"schema":"aeon.release-note-snapshot.v1","version":"not-a-version","frozen":true,"tickets":[{"unavailable":"","fields":{"pill_en":"Quiet dated notes","pill_de":"Leise datierte Notizen","benefit_en":"A date is enough.","benefit_de":"Ein Datum genügt.","hide_from_release_notes":false}}]}`)
	blank := ""
	rel, ok = projectPublicRelease(&blank, at, unversioned)
	if !ok || rel.Version != "" || len(rel.Notes) != 1 || rel.Notes[0].PillEN != "Quiet dated notes" {
		t.Fatalf("unversioned: ok=%v %+v", ok, rel)
	}
	hiddenOnly := []byte(`{"schema":"aeon.release-note-snapshot.v1","version":"260912120000.0.0","frozen":true,"tickets":[{"unavailable":"","fields":{"pill_en":"SECRET-EMPTY-RELEASE","pill_de":"SECRET-EMPTY-RELEASE","benefit_en":"SECRET-EMPTY-RELEASE","benefit_de":"SECRET-EMPTY-RELEASE","hide_from_release_notes":true}}]}`)
	hiddenVersion := "260912120000.0.0"
	if _, ok := projectPublicRelease(&hiddenVersion, at, hiddenOnly); ok {
		t.Fatal("a release with no public note was published")
	}
}

func TestLlmsTextIsPublicAndBounded(t *testing.T) {
	doc := portalDocument{
		Product: &portalProduct{Key: "PPR-1", Title: "Harbour catalog", Summary: "Work that is ready in the morning."},
		Catalog: []portalFeature{{
			Key: "PCF-1", Title: "Deadline [radar]", Summary: "Public summary.", Status: "live", LiveSince: "260926120000.0.0",
		}},
		Wishes:         []portalWish{{Key: "PWS-1", Title: "A public wish", Summary: "Join without an account.", Votes: 1}},
		ReleaseHistory: true,
	}
	releases := []publicRelease{{
		ReleasedAt: "2026-09-26T12:00:00Z",
		Version:    "260926120000.0.0",
		Notes: []publicNote{{
			PillEN: "Clear morning notes", PillDE: "Klare Morgennotizen",
			BenefitEN: "You can see what shipped.", BenefitDE: "Sichtbar, was geliefert wurde.",
		}},
	}}
	text := renderLlms("harbour", doc, releases)
	for _, want := range []string{
		"# Harbour catalog\n\n> Work that is ready in the morning.\n",
		"- [Deadline radar](/portal/harbour): Public summary. Live since 260926120000.0.0.\n",
		"- [A public wish](/portal/harbour): Join without an account. 1 vote.\n",
		"- [260926120000.0.0](/portal/harbour/releases): Clear morning notes. You can see what shipped.\n",
		"- [Catalog JSON](/portal/harbour/catalog.json)\n",
		"- [Release history](/portal/harbour/releases)\n",
		"- [Release history JSON](/api/public/portal/harbour/releases)\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "[radar]") || strings.Contains(text, "http://") || strings.Contains(text, "PPR-1") || strings.Contains(text, "Klare Morgennotizen") {
		t.Fatalf("llms leaked a private or bracketed field\n%s", text)
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatal("llms.txt must end with a newline")
	}
	doc.ReleaseHistory = false
	silent := renderLlms("harbour", doc, releases)
	if strings.Contains(silent, "Clear morning notes") || strings.Contains(silent, "Release history") || strings.Contains(silent, "You can see what shipped") {
		t.Fatalf("llms published notes without the opt-in\n%s", silent)
	}
	if !strings.Contains(silent, "- [Catalog JSON](/portal/harbour/catalog.json)\n") {
		t.Fatalf("llms dropped the catalog link\n%s", silent)
	}

	unsafe := portalDocument{Product: &portalProduct{Title: "Bad <script>", Summary: "still here"}, Catalog: []portalFeature{}, Wishes: []portalWish{}}
	bare := renderLlms("harbour", unsafe, nil)
	if strings.Contains(bare, "<") || strings.Contains(bare, "script") || !strings.Contains(bare, "# Product portal\n") || !strings.Contains(bare, "> still here\n") {
		t.Fatalf("unsafe title: %s", bare)
	}

	long := portalDocument{Product: &portalProduct{Title: "Harbour catalog", Summary: "Short."}, Catalog: []portalFeature{}, Wishes: []portalWish{}}
	item := portalFeature{Title: "Feature", Summary: strings.Repeat("word ", 800), Status: "planned"}
	for range 80 {
		long.Catalog = append(long.Catalog, item)
	}
	capped := renderLlms("harbour", long, nil)
	if len(capped) > llmsTextLimit || !strings.Contains(capped, "## Machine-readable\n") || !strings.HasSuffix(capped, "\n") {
		t.Fatalf("cap len=%d", len(capped))
	}
}
