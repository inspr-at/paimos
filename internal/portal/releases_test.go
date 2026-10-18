// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
)

func TestPublicReleaseHistoryWhitelist(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{21}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	harbour := makeTenant(t, d, "harbour", "Harbour")
	other := makeTenant(t, d, "other-yard", "Other")
	closed := makeTenant(t, d, "closed-yard", "Closed")
	quiet := makeTenant(t, d, "quiet-yard", "Quiet")
	unlinked := makeTenant(t, d, "unlinked-yard", "Unlinked")

	product := insertNode(t, d, harbour, "PPR-1", "portal_product", "Harbour catalog", "Work that is ready in the morning.", "published", "", "{}")
	insertNode(t, d, harbour, "PCF-1", "portal_feature", "Deadline radar", "Public summary.", "live", product, `{"live_since":"260926120000.0.0","internal_note":"SECRET-FEATURE"}`)
	insertNode(t, d, harbour, "PWS-1", "portal_wish", "A public wish", "Join without an account.", "published", product, "{}")
	setPortal(t, d, harbour, true)

	project := insertNode(t, d, harbour, "PRJ-1", "project", "SECRET-PROJECT-NAME", "SECRET-PROJECT-BODY", "open", "", "{}")
	otherProject := insertNode(t, d, harbour, "PRJ-2", "project", "SECRET-OTHER-PROJECT", "SECRET-OTHER-BODY", "open", "", "{}")
	linkPace(t, d, harbour, project)

	visible := `{"pill_en":"Clear morning notes","pill_de":"Klare Morgennotizen","benefit_en":"You can see what shipped.","benefit_de":"Sichtbar, was geliefert wurde.","hide_from_release_notes":false,"internal_note":"SECRET-INTERNAL"}`
	hidden := `{"pill_en":"Keep this private","pill_de":"Nicht öffentlich zeigen","benefit_en":"SECRET-HIDDEN-BENEFIT","benefit_de":"SECRET-HIDDEN-BENEFIT","hide_from_release_notes":true}`
	gap := `{"pill_en":"Nope","pill_de":"Nein danke","benefit_en":"SECRET-GAP-NOTE","benefit_de":"SECRET-GAP-NOTE","hide_from_release_notes":false}`
	latest := `{"pill_en":"Morning remains clear","pill_de":"Der Morgen bleibt klar","benefit_en":"The latest note stays.","benefit_de":"Die jüngste Notiz bleibt.","hide_from_release_notes":false}`
	dated := `{"pill_en":"Quiet dated notes","pill_de":"Leise datierte Notizen","benefit_en":"A date is enough.","benefit_de":"Ein Datum genügt.","hide_from_release_notes":false}`
	kept := `{"pill_en":"Kept from snapshot","pill_de":"Aus dem Schnappschuss","benefit_en":"The frozen line stays.","benefit_de":"Die eingefrorene Zeile bleibt.","hide_from_release_notes":false}`

	relSup := insertNode(t, d, harbour, "REL-1", "release", "SECRET-RELEASE-TITLE", "", "open", project, "{}")
	tktVisible := insertNode(t, d, harbour, "TKT-1", "work", "SECRET-TICKET-TITLE", "", "open", project, visible)
	tktHidden := insertNode(t, d, harbour, "TKT-2", "work", "SECRET-HIDDEN-TITLE", "", "open", project, hidden)
	tktGap := insertNode(t, d, harbour, "TKT-6", "work", "SECRET-GAP-TITLE", "", "open", project, gap)
	publishRelease(t, d, harbour, project, relSup, 1, "released", "260901120000.0.0", -28*24*time.Hour, []string{tktVisible, tktHidden, tktGap})
	setNodeFields(t, d, harbour, tktVisible, strings.Replace(visible, "You can see what shipped.", "SECRET-EDITED-LATER", 1))
	setReleaseState(t, d, harbour, relSup, "superseded")

	relCurrent := insertNode(t, d, harbour, "REL-3", "release", "SECRET-CURRENT-TITLE", "", "open", project, "{}")
	tktLatest := insertNode(t, d, harbour, "TKT-91", "work", "SECRET-LATEST-TITLE", "", "open", project, latest)
	publishRelease(t, d, harbour, project, relCurrent, 3, "released", "260926120000.0.0", -3*24*time.Hour, []string{tktLatest})
	setNodeFields(t, d, harbour, tktLatest, strings.Replace(latest, "The latest note stays.", "SECRET-EDITED-LATER", 1))

	relDated := insertNode(t, d, harbour, "REL-2", "release", "SECRET-DATED-TITLE", "", "open", project, "{}")
	tktDated := insertNode(t, d, harbour, "TKT-5", "work", "SECRET-DATED-TICKET", "", "open", project, dated)
	publishRelease(t, d, harbour, project, relDated, 2, "released", "", -19*24*time.Hour, []string{tktDated})

	relKept := insertNode(t, d, harbour, "REL-7", "release", "SECRET-KEPT-TITLE", "", "open", project, "{}")
	tktKept := insertNode(t, d, harbour, "TKT-7", "work", "SECRET-KEPT-TICKET", "", "open", project, kept)
	publishRelease(t, d, harbour, project, relKept, 7, "released", "260920120000.0.0", -9*24*time.Hour, []string{tktKept})
	clearReleaseVersion(t, d, harbour, relKept)

	relMismatch := insertNode(t, d, harbour, "REL-6", "release", "SECRET-MISMATCH-TITLE", "", "open", project, "{}")
	tktMismatch := insertNode(t, d, harbour, "TKT-8", "work", "SECRET-MISMATCH-TICKET", "", "open", project, `{"pill_en":"Mismatch stays hidden","pill_de":"Abweichung bleibt verborgen","benefit_en":"SECRET-MISMATCH","benefit_de":"SECRET-MISMATCH","hide_from_release_notes":false}`)
	publishRelease(t, d, harbour, project, relMismatch, 6, "released", "260915120000.0.0", -14*24*time.Hour, []string{tktMismatch})
	setReleaseVersion(t, d, harbour, relMismatch, "260916120000.0.0")

	relFuture := insertNode(t, d, harbour, "REL-4", "release", "SECRET-FUTURE-TITLE", "", "open", project, "{}")
	tktFuture := insertNode(t, d, harbour, "TKT-4", "work", "SECRET-FUTURE-TICKET", "", "open", project, `{"pill_en":"Future stays hidden","pill_de":"Zukunft bleibt verborgen","benefit_en":"SECRET-FUTURE","benefit_de":"SECRET-FUTURE","hide_from_release_notes":false}`)
	publishRelease(t, d, harbour, project, relFuture, 4, "released", "261201120000.0.0", 2*24*time.Hour, []string{tktFuture})

	relPlan := insertNode(t, d, harbour, "REL-8", "release", "SECRET-PLANNING-TITLE", "", "open", project, "{}")
	tktPlan := insertNode(t, d, harbour, "TKT-3", "work", "SECRET-PLANNING-TICKET", "", "open", project, `{"pill_en":"Plan stays hidden","pill_de":"Plan bleibt verborgen","benefit_en":"SECRET-PLANNING-NOTE","benefit_de":"SECRET-PLANNING-NOTE","hide_from_release_notes":false}`)
	publishRelease(t, d, harbour, project, relPlan, 8, "planning", "", 0, []string{tktPlan})

	relCandidate := insertNode(t, d, harbour, "REL-5", "release", "SECRET-CANDIDATE-TITLE", "", "open", project, "{}")
	tktCandidate := insertNode(t, d, harbour, "TKT-9", "work", "SECRET-CANDIDATE-TICKET", "", "open", project, `{"pill_en":"Candidate stays hidden","pill_de":"Kandidat bleibt verborgen","benefit_en":"SECRET-CANDIDATE","benefit_de":"SECRET-CANDIDATE","hide_from_release_notes":false}`)
	publishRelease(t, d, harbour, project, relCandidate, 5, "candidate", "", 0, []string{tktCandidate})

	relOther := insertNode(t, d, harbour, "REL-9", "release", "SECRET-OTHER-RELEASE", "", "open", otherProject, "{}")
	tktOther := insertNode(t, d, harbour, "TKT-10", "work", "SECRET-OTHER-TICKET", "", "open", otherProject, `{"pill_en":"Other project hidden","pill_de":"Anderes Projekt","benefit_en":"SECRET-OTHER-RELEASE","benefit_de":"SECRET-OTHER-RELEASE","hide_from_release_notes":false}`)
	publishRelease(t, d, harbour, otherProject, relOther, 1, "released", "260910120000.0.0", -2*24*time.Hour, []string{tktOther})

	relCold := insertNode(t, d, harbour, "REL-10", "release", "SECRET-UNFROZEN-TITLE", "", "open", project, "{}")
	publishRelease(t, d, harbour, project, relCold, 10, "planning", "", 0, nil)
	insertUnfrozenSnapshot(t, d, harbour, project, relCold, "SECRET-UNFROZEN")
	publishExisting(t, d, harbour, relCold, -1*24*time.Hour)

	relEmpty := insertNode(t, d, harbour, "REL-12", "release", "SECRET-EMPTY-TITLE", "", "open", project, "{}")
	tktEmpty := insertNode(t, d, harbour, "TKT-12", "work", "SECRET-EMPTY-TICKET", "", "open", project, `{"pill_en":"SECRET-EMPTY-RELEASE","pill_de":"SECRET-EMPTY-RELEASE","benefit_en":"SECRET-EMPTY-RELEASE","benefit_de":"SECRET-EMPTY-RELEASE","hide_from_release_notes":true}`)
	publishRelease(t, d, harbour, project, relEmpty, 12, "released", "260912120000.0.0", -6*24*time.Hour, []string{tktEmpty})

	relGone := insertNode(t, d, harbour, "REL-11", "release", "SECRET-DELETED-TITLE", "", "open", project, "{}")
	tktGone := insertNode(t, d, harbour, "TKT-11", "work", "SECRET-DELETED-TICKET", "", "open", project, `{"pill_en":"Deleted stays hidden","pill_de":"Gelöscht bleibt verborgen","benefit_en":"SECRET-DELETED-RELEASE","benefit_de":"SECRET-DELETED-RELEASE","hide_from_release_notes":false}`)
	publishRelease(t, d, harbour, project, relGone, 11, "released", "260905120000.0.0", -4*24*time.Hour, []string{tktGone})
	softDeleteNode(t, d, harbour, relGone)

	otherProduct := insertNode(t, d, other, "PPR-1", "portal_product", "Other catalog", "Other summary.", "published", "", "{}")
	_ = otherProduct
	setPortal(t, d, other, true)
	otherProj := insertNode(t, d, other, "PRJ-1", "project", "SECRET-OTHER-TENANT-PROJECT", "", "open", "", "{}")
	linkPace(t, d, other, otherProj)
	otherRel := insertNode(t, d, other, "REL-1", "release", "SECRET-OTHER-TENANT-TITLE", "", "open", otherProj, "{}")
	otherTkt := insertNode(t, d, other, "TKT-1", "work", "SECRET-OTHER-TENANT-TICKET", "", "open", otherProj, `{"pill_en":"Other tenant hidden","pill_de":"Anderer Mandant","benefit_en":"SECRET-OTHER-TENANT","benefit_de":"SECRET-OTHER-TENANT","hide_from_release_notes":false}`)
	publishRelease(t, d, other, otherProj, otherRel, 1, "released", "260926120000.0.0", -3*24*time.Hour, []string{otherTkt})

	insertNode(t, d, closed, "PPR-1", "portal_product", "Closed catalog", "Closed summary.", "published", "", "{}")
	setPortal(t, d, closed, false)
	insertNode(t, d, unlinked, "PPR-1", "portal_product", "Unlinked catalog", "Unlinked summary.", "published", "", "{}")
	setPortal(t, d, unlinked, true)
	setPortal(t, d, quiet, true)

	const (
		readIP = "203.0.113.80:1800"
		missIP = "203.0.113.81:1801"
	)
	releasesURL := "/api/public/portal/harbour/releases"
	withheld := f.do(http.MethodGet, releasesURL, "", readIP, nil, nil, map[string]string{"Host": "secret.example"})
	var withheldDoc publicReleasesDocument
	if withheld.Code != http.StatusOK || json.Unmarshal(withheld.Body.Bytes(), &withheldDoc) != nil || withheldDoc.Product == nil || withheldDoc.Product.Title != "Harbour catalog" || len(withheldDoc.Releases) != 0 {
		t.Fatalf("existing link published notes: %d %s", withheld.Code, withheld.Body)
	}
	if strings.Contains(withheld.Body.String(), "You can see what shipped") || strings.Contains(withheld.Body.String(), "SECRET-") || strings.Contains(withheld.Body.String(), "260926120000.0.0") {
		t.Fatalf("existing link leaked notes: %s", withheld.Body)
	}
	withheldLlms := f.do(http.MethodGet, "/api/public/portal/harbour/llms.txt", "", readIP, nil, nil, nil)
	if withheldLlms.Code != http.StatusOK || strings.Contains(withheldLlms.Body.String(), "Morning remains clear") || strings.Contains(withheldLlms.Body.String(), "Release history") || strings.Contains(withheldLlms.Body.String(), "SECRET-") {
		t.Fatalf("existing link leaked llms: %d %s", withheldLlms.Code, withheldLlms.Body)
	}
	withheldCatalog := f.do(http.MethodGet, "/api/public/portal/harbour", "", readIP, nil, nil, nil)
	if withheldCatalog.Code != http.StatusOK || strings.Contains(withheldCatalog.Body.String(), "release_history") || strings.Contains(withheldCatalog.Body.String(), "You can see what shipped") {
		t.Fatalf("existing link advertised history: %d %s", withheldCatalog.Code, withheldCatalog.Body)
	}
	privateRec := f.do(http.MethodGet, "/api/public/portal/other-yard/releases", "", readIP, nil, nil, nil)
	var privateDoc publicReleasesDocument
	if privateRec.Code != http.StatusOK || json.Unmarshal(privateRec.Body.Bytes(), &privateDoc) != nil || privateDoc.Product == nil || len(privateDoc.Releases) != 0 || strings.Contains(privateRec.Body.String(), "SECRET-OTHER-TENANT") || strings.Contains(privateRec.Body.String(), "Other tenant hidden") {
		t.Fatalf("private project without opt-in: %d %s", privateRec.Code, privateRec.Body)
	}
	setReleaseHistory(t, d, harbour, true)

	rec := f.do(http.MethodGet, releasesURL, "", readIP, nil, nil, map[string]string{"Host": "secret.example"})
	if rec.Code != http.StatusOK {
		t.Fatalf("releases: %d %s", rec.Code, rec.Body)
	}
	var doc publicReleasesDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Product == nil || doc.Product.Title != "Harbour catalog" || doc.Product.Key != "PPR-1" {
		t.Fatalf("product: %+v", doc.Product)
	}
	if len(doc.Releases) != 4 {
		t.Fatalf("releases %d: %s", len(doc.Releases), rec.Body)
	}
	wantVersions := []string{"260926120000.0.0", "260920120000.0.0", "", "260901120000.0.0"}
	var previous time.Time
	for i, rel := range doc.Releases {
		if rel.Version != wantVersions[i] {
			t.Fatalf("version[%d]=%q body %s", i, rel.Version, rec.Body)
		}
		at, err := time.Parse(time.RFC3339, rel.ReleasedAt)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && !at.Before(previous) {
			t.Fatalf("order broke at %d: %s then %s", i, previous, at)
		}
		previous = at
		if rel.Notes == nil {
			t.Fatal("notes must be an array")
		}
	}
	if len(doc.Releases[0].Notes) != 1 || doc.Releases[0].Notes[0].PillEN != "Morning remains clear" || doc.Releases[0].Notes[0].BenefitEN != "The latest note stays." {
		t.Fatalf("current: %+v", doc.Releases[0])
	}
	if doc.Releases[1].Notes[0].BenefitEN != "The frozen line stays." || doc.Releases[2].Version != "" || doc.Releases[2].Notes[0].PillEN != "Quiet dated notes" {
		t.Fatalf("middle: %+v %+v", doc.Releases[1], doc.Releases[2])
	}
	if len(doc.Releases[3].Notes) != 1 || doc.Releases[3].Notes[0].BenefitEN != "You can see what shipped." || doc.Releases[3].Notes[0].PillDE != "Klare Morgennotizen" {
		t.Fatalf("superseded notes: %+v", doc.Releases[3].Notes)
	}
	for _, secret := range []string{
		"SECRET-PROJECT-NAME", "SECRET-PROJECT-BODY", "SECRET-OTHER-PROJECT", "SECRET-RELEASE-TITLE",
		"SECRET-TICKET-TITLE", "SECRET-INTERNAL", "SECRET-HIDDEN-BENEFIT", "SECRET-GAP-NOTE",
		"SECRET-EDITED-LATER", "SECRET-MISMATCH", "SECRET-FUTURE", "SECRET-PLANNING-NOTE",
		"SECRET-CANDIDATE", "SECRET-OTHER-RELEASE", "SECRET-UNFROZEN", "SECRET-DELETED-RELEASE",
		"SECRET-OTHER-TENANT", "SECRET-FEATURE", "SECRET-EMPTY-RELEASE", "260912120000.0.0",
		"TKT-91", "TKT-1", "TKT-2", "PRJ-1", "REL-1",
		"superseded", "planning", "candidate", "hide_from_release_notes", "unavailable", "project_node_id",
		"release_node_id", "tenant_id", harbour, project, relCurrent, tktLatest,
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("releases leaked %s", secret)
		}
	}

	llms := f.do(http.MethodGet, "/api/public/portal/harbour/llms.txt", "", readIP, nil, nil, map[string]string{"Host": "secret.example", "X-Forwarded-Host": "secret.example"})
	if llms.Code != http.StatusOK || !strings.HasPrefix(llms.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("llms: %d %s %s", llms.Code, llms.Header().Get("Content-Type"), llms.Body)
	}
	for _, want := range []string{
		"# Harbour catalog\n",
		"- [Deadline radar](/portal/harbour): Public summary. Live since 260926120000.0.0.",
		"- [A public wish](/portal/harbour): Join without an account. 0 votes.",
		"- [260926120000.0.0](/portal/harbour/releases): Morning remains clear. The latest note stays.",
		"- [Catalog JSON](/portal/harbour/catalog.json)",
		"- [Release history JSON](/api/public/portal/harbour/releases)",
	} {
		if !strings.Contains(llms.Body.String(), want) {
			t.Fatalf("llms missing %q\n%s", want, llms.Body)
		}
	}
	if strings.Contains(llms.Body.String(), "secret.example") || strings.Contains(llms.Body.String(), "SECRET-") || strings.Contains(llms.Body.String(), "## Comparison") {
		t.Fatalf("llms leaked\n%s", llms.Body)
	}
	if !strings.Contains(llms.Body.String(), "## Pace\n") {
		t.Fatalf("llms omitted the public pace\n%s", llms.Body)
	}
	if !strings.HasSuffix(llms.Body.String(), "\n") {
		t.Fatal("llms newline")
	}

	catalog := f.do(http.MethodGet, "/api/public/portal/harbour", "", readIP, nil, nil, nil)
	file := f.do(http.MethodGet, "/api/public/portal/harbour/catalog.json", "", readIP, nil, nil, nil)
	if catalog.Code != http.StatusOK || file.Body.String() != catalog.Body.String() || !strings.Contains(catalog.Body.String(), `"release_history":true`) || strings.Contains(catalog.Body.String(), "You can see what shipped") {
		t.Fatalf("catalog file: %d %d\n%s\n%s", catalog.Code, file.Code, catalog.Body, file.Body)
	}

	emptyReleases := f.do(http.MethodGet, "/api/public/portal/quiet-yard/releases", "", readIP, nil, nil, nil)
	var empty publicReleasesDocument
	if emptyReleases.Code != http.StatusOK || json.Unmarshal(emptyReleases.Body.Bytes(), &empty) != nil || empty.Product != nil || len(empty.Releases) != 0 || !strings.Contains(emptyReleases.Body.String(), `"releases":[]`) {
		t.Fatalf("empty portal: %d %s", emptyReleases.Code, emptyReleases.Body)
	}
	emptyLlms := f.do(http.MethodGet, "/api/public/portal/quiet-yard/llms.txt", "", readIP, nil, nil, nil)
	if emptyLlms.Code != http.StatusOK || !strings.Contains(emptyLlms.Body.String(), "> Nothing published yet.\n") || !strings.Contains(emptyLlms.Body.String(), "/portal/quiet-yard/catalog.json") {
		t.Fatalf("empty llms: %d %s", emptyLlms.Code, emptyLlms.Body)
	}
	unlinkedRec := f.do(http.MethodGet, "/api/public/portal/unlinked-yard/releases", "", readIP, nil, nil, nil)
	var unlinkedDoc publicReleasesDocument
	if unlinkedRec.Code != http.StatusOK || json.Unmarshal(unlinkedRec.Body.Bytes(), &unlinkedDoc) != nil || unlinkedDoc.Product == nil || unlinkedDoc.Product.Title != "Unlinked catalog" || len(unlinkedDoc.Releases) != 0 {
		t.Fatalf("unlinked: %d %s", unlinkedRec.Code, unlinkedRec.Body)
	}

	closedRel := f.do(http.MethodGet, "/api/public/portal/closed-yard/releases", "", missIP, nil, nil, nil)
	unknownRel := f.do(http.MethodGet, "/api/public/portal/no-such-portal/releases", "", missIP, nil, nil, nil)
	sameResponse(t, closedRel, unknownRel)
	closedLlms := f.do(http.MethodGet, "/api/public/portal/closed-yard/llms.txt", "", missIP, nil, nil, nil)
	unknownLlms := f.do(http.MethodGet, "/api/public/portal/no-such-portal/llms.txt", "", missIP, nil, nil, nil)
	sameResponse(t, closedLlms, unknownLlms)
	closedFile := f.do(http.MethodGet, "/api/public/portal/closed-yard/catalog.json", "", missIP, nil, nil, nil)
	unknownFile := f.do(http.MethodGet, "/api/public/portal/no-such-portal/catalog.json", "", missIP, nil, nil, nil)
	sameResponse(t, closedFile, unknownFile)
	if closedRel.Code != http.StatusNotFound || closedLlms.Header().Get("Content-Type") != closedRel.Header().Get("Content-Type") || strings.Contains(closedLlms.Body.String(), "Harbour") {
		t.Fatalf("404 parity: %d %s", closedLlms.Code, closedLlms.Body)
	}

	post := f.do(http.MethodPost, releasesURL, "{}", readIP, nil, nil, nil)
	if post.Code == http.StatusOK || strings.Contains(post.Body.String(), "Harbour catalog") {
		t.Fatalf("post releases: %d %s", post.Code, post.Body)
	}

	clearPace(t, d, harbour)
	after := f.do(http.MethodGet, releasesURL, "", readIP, nil, nil, nil)
	var cleared publicReleasesDocument
	if after.Code != http.StatusOK || json.Unmarshal(after.Body.Bytes(), &cleared) != nil || cleared.Product == nil || len(cleared.Releases) != 0 {
		t.Fatalf("pace cleared: %d %s", after.Code, after.Body)
	}
}

func TestPublicReadLimitIsShared(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{22}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}
	openID := makeTenant(t, d, "harbour", "Harbour")
	closedID := makeTenant(t, d, "closed-yard", "Closed")
	insertNode(t, d, openID, "PPR-1", "portal_product", "Harbour catalog", "Summary.", "published", "", "{}")
	insertNode(t, d, closedID, "PPR-1", "portal_product", "Closed catalog", "Summary.", "published", "", "{}")
	setPortal(t, d, openID, true)
	setPortal(t, d, closedID, false)

	const ip = "203.0.113.125:4125"
	paths := []string{
		"/api/public/portal/harbour",
		"/api/public/portal/harbour/releases",
		"/api/public/portal/harbour/llms.txt",
		"/api/public/portal/harbour/catalog.json",
	}
	for i := range 120 {
		rec := f.do(http.MethodGet, paths[i%len(paths)], "", ip, nil, nil, nil)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("limited at %d", i)
		}
	}
	unknown := f.do(http.MethodGet, "/api/public/portal/no-such-portal/releases", "", ip, nil, nil, nil)
	disabled := f.do(http.MethodGet, "/api/public/portal/closed-yard/llms.txt", "", ip, nil, nil, nil)
	open := f.do(http.MethodGet, "/api/public/portal/harbour/catalog.json", "", ip, nil, nil, nil)
	if unknown.Code != http.StatusTooManyRequests || !strings.Contains(unknown.Body.String(), `"error":"too many attempts"`) {
		t.Fatalf("limit: %d %s", unknown.Code, unknown.Body)
	}
	sameResponse(t, unknown, disabled)
	sameResponse(t, unknown, open)
}

func TestPublicFilesStayOffTheSPA(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{23}, 32))
	page := []byte("<!doctype html><title>SPA</title><p>SPA-MARKER</p>")
	srv := &httpapi.Server{
		Modules: []httpapi.Module{m},
		Web:     fstest.MapFS{"index.html": &fstest.MapFile{Data: page}},
	}
	handler := srv.Handler()
	harbour := makeTenant(t, d, "harbour", "Harbour")
	closed := makeTenant(t, d, "closed-yard", "Closed")
	insertNode(t, d, harbour, "PPR-1", "portal_product", "Harbour catalog", "Work that is ready in the morning.", "published", "", "{}")
	insertNode(t, d, closed, "PPR-1", "portal_product", "Closed catalog", "Closed summary.", "published", "", "{}")
	setPortal(t, d, harbour, true)
	setPortal(t, d, closed, false)

	const ip = "203.0.113.90:1900"
	call := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = ip
		req.Host = "secret.example"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	pageRec := call(http.MethodGet, "/portal/harbour")
	if pageRec.Code != http.StatusOK || !strings.Contains(pageRec.Body.String(), "SPA-MARKER") || !strings.Contains(pageRec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("portal page: %d %s", pageRec.Code, pageRec.Body)
	}
	closedPage := call(http.MethodGet, "/portal/closed-yard")
	if !strings.Contains(closedPage.Body.String(), "SPA-MARKER") {
		t.Fatalf("closed page left the SPA: %d %s", closedPage.Code, closedPage.Body)
	}
	rootReleases := call(http.MethodGet, "/portal/harbour/releases")
	if !strings.Contains(rootReleases.Body.String(), "SPA-MARKER") {
		t.Fatalf("releases page left the SPA: %s", rootReleases.Body)
	}

	llms := call(http.MethodGet, "/portal/harbour/llms.txt")
	apiLlms := call(http.MethodGet, "/api/public/portal/harbour/llms.txt")
	if llms.Code != http.StatusOK || !strings.HasPrefix(llms.Header().Get("Content-Type"), "text/plain") || strings.Contains(llms.Body.String(), "SPA-MARKER") || strings.Contains(llms.Body.String(), "secret.example") {
		t.Fatalf("root llms: %d %s %s", llms.Code, llms.Header().Get("Content-Type"), llms.Body)
	}
	if llms.Body.String() != apiLlms.Body.String() || llms.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("root and api llms differ\n%s\n%s", llms.Body, apiLlms.Body)
	}
	catalog := call(http.MethodGet, "/api/public/portal/harbour")
	rootFile := call(http.MethodGet, "/portal/harbour/catalog.json")
	apiFile := call(http.MethodGet, "/api/public/portal/harbour/catalog.json")
	if rootFile.Body.String() != catalog.Body.String() || apiFile.Body.String() != catalog.Body.String() {
		t.Fatalf("catalog file mismatch\n%s\n%s", rootFile.Body, catalog.Body)
	}
	if rootFile.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("root file missed security headers")
	}

	closedRoot := call(http.MethodGet, "/portal/closed-yard/llms.txt")
	closedAPI := call(http.MethodGet, "/api/public/portal/closed-yard/llms.txt")
	unknownRoot := call(http.MethodGet, "/portal/no-such-portal/llms.txt")
	if closedRoot.Code != http.StatusNotFound || closedRoot.Body.String() != closedAPI.Body.String() || closedRoot.Body.String() != unknownRoot.Body.String() || strings.Contains(closedRoot.Body.String(), "SPA-MARKER") || !strings.Contains(closedRoot.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("closed file: %d %s", closedRoot.Code, closedRoot.Body)
	}

	post := call(http.MethodPost, "/portal/harbour/llms.txt")
	if post.Code != http.StatusMethodNotAllowed || strings.Contains(post.Body.String(), "Harbour catalog") {
		t.Fatalf("post file: %d %s", post.Code, post.Body)
	}
	slash := call(http.MethodGet, "/portal/harbour/llms.txt/")
	if strings.HasPrefix(slash.Header().Get("Content-Type"), "text/plain") || strings.Contains(slash.Body.String(), "# Harbour catalog") {
		t.Fatalf("trailing slash matched the file: %d %s", slash.Code, slash.Body)
	}

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	headReq, err := http.NewRequest(http.MethodHead, ts.URL+"/portal/harbour/llms.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	headRes, err := ts.Client().Do(headReq)
	if err != nil {
		t.Fatal(err)
	}
	defer headRes.Body.Close()
	headBody, _ := io.ReadAll(headRes.Body)
	if headRes.StatusCode != http.StatusOK || len(headBody) != 0 || !strings.HasPrefix(headRes.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("head: %d %q %s", headRes.StatusCode, headBody, headRes.Header.Get("Content-Type"))
	}
}

func setReleaseHistory(t *testing.T, d *dbtest.DB, tenantID string, on bool) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		tag, err := tx.Exec(t.Context(), `UPDATE portal_pace SET release_history=$1`, on)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("updated %d pace rows", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func linkPace(t *testing.T, d *dbtest.DB, tenantID, project string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO portal_pace(tenant_id, project_node_id) VALUES ($1::uuid, $2::uuid)`, tenantID, project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func clearPace(t *testing.T, d *dbtest.DB, tenantID string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM portal_pace`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publishRelease(t *testing.T, d *dbtest.DB, tenantID, project, release string, number int, state, version string, offset time.Duration, tickets []string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		var projects int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_projects WHERE project_node_id=$1::uuid`, project).Scan(&projects); err != nil {
			return err
		}
		if projects == 0 {
			if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id, project_node_id) VALUES ($1::uuid,$2::uuid)`, tenantID, project); err != nil {
				return err
			}
		}
		args := []any{tenantID, release, project, number, state}
		versionSQL, schemeSQL, atSQL := "NULL", "NULL", "NULL"
		if version != "" {
			args = append(args, version)
			versionSQL = fmt.Sprintf("$%d", len(args))
			schemeSQL = "'inspr-calendar-v2'"
		}
		if state == "released" || state == "superseded" {
			args = append(args, fmt.Sprintf("%f seconds", offset.Seconds()))
			atSQL = fmt.Sprintf("clock_timestamp() + $%d::interval", len(args))
		}
		q := fmt.Sprintf(`INSERT INTO journey_releases(tenant_id, release_node_id, project_node_id, number, state, version_scheme, version, released_at)
			VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,%s,%s,%s)`, schemeSQL, versionSQL, atSQL)
		if _, err := tx.Exec(t.Context(), q, args...); err != nil {
			return err
		}
		for i, ticket := range tickets {
			if _, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id, ticket_node_id, project_node_id, release_node_id, walker_position, source)
				VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,'manual')`, tenantID, ticket, project, release, i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("publish %s: %v", release, err)
	}
}

func setNodeFields(t *testing.T, d *dbtest.DB, tenantID, id, fields string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1::uuid`, id, fields)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setReleaseState(t *testing.T, d *dbtest.DB, tenantID, release, state string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state=$2 WHERE release_node_id=$1::uuid`, release, state)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setReleaseVersion(t *testing.T, d *dbtest.DB, tenantID, release, version string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET version=$2, version_scheme='inspr-calendar-v2' WHERE release_node_id=$1::uuid`, release, version)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func clearReleaseVersion(t *testing.T, d *dbtest.DB, tenantID, release string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET version=NULL, version_scheme=NULL WHERE release_node_id=$1::uuid`, release)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publishExisting(t *testing.T, d *dbtest.DB, tenantID, release string, offset time.Duration) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released', released_at = clock_timestamp() + $2::interval WHERE release_node_id=$1::uuid`, release, fmt.Sprintf("%f seconds", offset.Seconds()))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertUnfrozenSnapshot(t *testing.T, d *dbtest.DB, tenantID, project, release, secret string) {
	t.Helper()
	raw := fmt.Sprintf(`{"schema":"aeon.release-note-snapshot.v1","version":"","frozen":false,"tickets":[{"unavailable":"","fields":{"pill_en":"Unfrozen stays hidden","pill_de":"Ungefroren","benefit_en":"%s","benefit_de":"%s","hide_from_release_notes":false}}]}`, secret, secret)
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_release_note_snapshots(tenant_id, release_node_id, project_node_id, snapshot)
			VALUES ($1::uuid,$2::uuid,$3::uuid,$4::jsonb)`, tenantID, release, project, raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func softDeleteNode(t *testing.T, d *dbtest.DB, tenantID, id string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1::uuid`, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
