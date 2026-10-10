// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPortalMarketAndPace(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytesRepeat())
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	tenantA := makeTenant(t, d, "market-a", "Market A")
	tenantB := makeTenant(t, d, "market-b", "Market B")
	admin := makePerson(t, d, tenantA, "SECRET-ADMIN-NAME", "admin")
	member := makePerson(t, d, tenantA, "Mina Member", "member")
	agent := tenant.Principal{ID: admin.ID, TenantID: tenantA, Kind: tenant.Agent, Name: "Portal agent", Scopes: []string{"settings.manage"}}

	product := insertNode(t, d, tenantA, "PPR-1", "portal_product", "Harbour catalog", "Work that is ready in the morning.", "published", "", "{}")
	feature := insertNode(t, d, tenantA, "PCF-1", "portal_feature", "Deadline radar", "Public summary of the radar", "live", product,
		`{"live_since":"260926120000.0.0","assignee_email":"leak-pace@example.com","ticket_key":"AEON-SECRET-PACE","internal_note":"SECRET-NOTE-PACE"}`)
	unlived := insertNode(t, d, tenantA, "PCF-2", "portal_feature", "Still planned", "Not live", "planned", product, "{}")
	noDate := insertNode(t, d, tenantA, "PCF-3", "portal_feature", "Live without a date", "No event", "live", product, "{}")
	wishFast := insertNode(t, d, tenantA, "PWS-1", "portal_wish", "A public wish", "Join without an account.", "published", product, "{}")
	wishSlow := insertNode(t, d, tenantA, "PWS-2", "portal_wish", "SECRET-HIDDEN-WISH", "hidden body", "hidden", product, "{}")
	wishLate := insertNode(t, d, tenantA, "PWS-3", "portal_wish", "Opened too late", "After live.", "published", product, "{}")
	insertNode(t, d, tenantA, "TKT-9", "work", "SECRET-TICKET-PACE", "SECRET-TICKET-BODY", "open", "", "{}")
	project := insertNode(t, d, tenantA, "PRJ-1", "project", "SECRET-PROJECT-NAME", "SECRET-PROJECT-BODY", "open", "", "{}")
	rel1 := insertNode(t, d, tenantA, "REL-1", "release", "SECRET-RELEASE-TITLE", "notes", "open", "", "{}")
	rel2 := insertNode(t, d, tenantA, "REL-2", "release", "SECRET-RELEASE-TITLE", "notes", "open", "", "{}")
	rel3 := insertNode(t, d, tenantA, "REL-3", "release", "SECRET-RELEASE-TITLE", "notes", "open", "", "{}")
	insertNode(t, d, tenantB, "PPR-1", "portal_product", "Other catalog", "Other summary.", "published", "", "{}")
	setPortal(t, d, tenantA, true)
	setPortal(t, d, tenantB, true)
	setCreated(t, d, tenantA, wishFast, "2026-01-11 12:00:00+00")
	setCreated(t, d, tenantA, wishSlow, "2026-01-01 12:00:00+00")
	setCreated(t, d, tenantA, wishLate, "2026-02-01 12:00:00+00")
	insertLiveEvent(t, d, tenantA, admin.ID, feature, "2026-01-21 12:00:00+00")
	insertRelease(t, d, tenantA, project, rel1, 1, "clock_timestamp() - interval '2 hours'")
	insertRelease(t, d, tenantA, project, rel2, 2, "clock_timestamp() - interval '10 days'")
	insertRelease(t, d, tenantA, project, rel3, 3, "timestamptz '2020-01-01 12:00:00+00'")

	const (
		readA   = "/api/public/portal/market-a"
		readB   = "/api/public/portal/market-b"
		missing = "/api/public/portal/no-such-portal"
		corrA   = "/api/public/portal/market-a/corrections"
		adminIP = "203.0.113.10:1000"
	)
	secrets := []string{
		"SECRET-ADMIN-NAME", "SECRET-HIDDEN-WISH", "SECRET-TICKET-PACE", "SECRET-TICKET-BODY",
		"SECRET-PROJECT-NAME", "SECRET-PROJECT-BODY", "SECRET-RELEASE-TITLE", "SECRET-NOTE-PACE",
		"AEON-SECRET-PACE", "leak-pace@example.com", "SECRET-HIDDEN-RIVAL", "SECRET-UNAPPROVED-QUOTE",
		"SECRET-OLD-QUOTE", "SECRET-CORRECTION-NOTE", "secret-unapproved.example",
	}

	if rec := f.do(http.MethodPost, "/api/portal/competitors", `{"name":"Northwind"}`, adminIP, &member, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member competitor: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, "/api/portal/competitors", `{"name":"Northwind"}`, adminIP, &agent, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("agent competitor: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, "/api/portal/market", "", adminIP, nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous market: %d", rec.Code)
	}

	north := decodeItem[competitorItem](t, f.do(http.MethodPost, "/api/portal/competitors", `{"name":"Northwind"}`, adminIP, &admin, nil, nil))
	linden := decodeItem[competitorItem](t, f.do(http.MethodPost, "/api/portal/competitors", `{"name":"Linden"}`, adminIP, &admin, nil, nil))
	hidden := decodeItem[competitorItem](t, f.do(http.MethodPost, "/api/portal/competitors", `{"name":"SECRET-HIDDEN-RIVAL"}`, adminIP, &admin, nil, nil))
	if north.Published || hidden.Published {
		t.Fatal("new competitors are public before they are published")
	}
	if rec := f.do(http.MethodPost, "/api/portal/competitors", `{"name":"northwind"}`, adminIP, &admin, nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate competitor: %d %s", rec.Code, rec.Body)
	}
	mustOK(t, f.do(http.MethodPatch, "/api/portal/competitors/"+north.ID, `{"published":true}`, adminIP, &admin, nil, nil))
	mustOK(t, f.do(http.MethodPatch, "/api/portal/competitors/"+linden.ID, `{"published":true}`, adminIP, &admin, nil, nil))

	deadlines := decodeItem[aspectItem](t, f.do(http.MethodPost, "/api/portal/aspects", `{"label":"Statutory deadlines"}`, adminIP, &admin, nil, nil))
	assembly := decodeItem[aspectItem](t, f.do(http.MethodPost, "/api/portal/aspects", `{"label":"Owner assembly"}`, adminIP, &admin, nil, nil))
	today := portalDate(t, d, tenantA)
	fresh := shiftDay(t, today, -10)
	old := shiftDay(t, today, -181)

	first := putCell(t, f, &admin, deadlines.ID, north.ID, "yes", "SECRET-OLD-QUOTE from the old page.", "https://northwind.example/deadlines", fresh)
	if first.Approved {
		t.Fatal("a draft cell was approved")
	}
	approved := decodeItem[cellAdmin](t, f.do(http.MethodPost, "/api/portal/cells/"+first.ID+"/approve", `{}`, adminIP, &admin, nil, nil))
	if !approved.Approved {
		t.Fatalf("approve: %+v", approved)
	}
	revised := putCell(t, f, &admin, deadlines.ID, north.ID, "yes", "QUOTED-FACT-ALPHA on the public help page.", "https://northwind.example/deadlines", fresh)
	if revised.Approved {
		t.Fatal("editing a cell kept it approved")
	}
	if rec := f.do(http.MethodPost, "/api/portal/cells/"+revised.ID+"/approve", `{}`, adminIP, &admin, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("re-approve: %d %s", rec.Code, rec.Body)
	}
	staleCell := putCell(t, f, &admin, assembly.ID, north.ID, "no", "STALE-QUOTED-FACT from an old brochure.", "https://northwind.example/assembly", old)
	if !staleCell.Stale || !staleCell.Recheck {
		t.Fatalf("admin stale flags %+v", staleCell)
	}
	mustOK(t, f.do(http.MethodPost, "/api/portal/cells/"+staleCell.ID+"/approve", `{}`, adminIP, &admin, nil, nil))
	draft := putCell(t, f, &admin, deadlines.ID, linden.ID, "yes", "SECRET-UNAPPROVED-QUOTE stays in review.", "https://secret-unapproved.example/page", fresh)
	if draft.Approved {
		t.Fatal("unapproved cell")
	}
	if rec := f.do(http.MethodPut, "/api/portal/cells", `{"aspect_id":"`+deadlines.ID+`","competitor_id":"`+linden.ID+`","stance":"yes","quote":"short","source_url":"http://linden.example/x","retrieved_on":"`+fresh+`"}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad source: %d %s", rec.Code, rec.Body)
	}

	mustOK(t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+project+`"}`, adminIP, &admin, nil, nil))
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+feature+`"}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("feature as pace project: %d %s", rec.Code, rec.Body)
	}
	mustOK(t, f.do(http.MethodPut, "/api/portal/wishes/"+wishFast+"/fulfillment", `{"feature_id":"`+feature+`"}`, adminIP, &admin, nil, nil))
	mustOK(t, f.do(http.MethodPut, "/api/portal/wishes/"+wishSlow+"/fulfillment", `{"feature_id":"`+feature+`"}`, adminIP, &admin, nil, nil))
	if rec := f.do(http.MethodPut, "/api/portal/wishes/"+wishLate+"/fulfillment", `{"feature_id":"`+feature+`"}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("late wish: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/wishes/"+wishFast+"/fulfillment", `{"feature_id":"`+unlived+`"}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("planned feature: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/wishes/"+wishFast+"/fulfillment", `{"feature_id":"`+noDate+`"}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing live date: %d %s", rec.Code, rec.Body)
	}

	pace := decodeItem[paceAdmin](t, f.do(http.MethodGet, "/api/portal/pace", "", adminIP, &admin, nil, nil))
	if pace.ProjectTitle != "SECRET-PROJECT-NAME" || pace.ReleaseHistory || pace.Revision != 1 || pace.WishToLiveMedianDays == nil || *pace.WishToLiveMedianDays != 15 {
		t.Fatalf("admin pace %+v", pace)
	}
	market := decodeItem[marketAdmin](t, f.do(http.MethodGet, "/api/portal/market", "", adminIP, &admin, nil, nil))
	if !historyHas(market.History, "SECRET-OLD-QUOTE") {
		t.Fatalf("history lost the previous quote: %+v", market.History)
	}

	body := f.do(http.MethodGet, readA, "", "203.0.113.20:1000", nil, nil, nil)
	if body.Code != http.StatusOK {
		t.Fatalf("public read: %d %s", body.Code, body.Body)
	}
	assertNoSecrets(t, body.Body.String(), secrets)
	assertPublicShape(t, body.Body.Bytes())
	var doc publicMarketDoc
	if err := json.Unmarshal(body.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Pace != nil {
		t.Fatalf("public pace from two wishes and three releases %+v", doc.Pace)
	}
	if doc.ReleaseHistory || strings.Contains(body.Body.String(), "release_history") {
		t.Fatalf("existing link advertised release history: %s", body.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true}`, adminIP, &member, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member history: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":"yes"}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad history: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"project_id":null,"release_history":true}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("clear and publish: %d %s", rec.Code, rec.Body)
	}
	opted := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true,"project_id":"`+project+`","revision":1}`, adminIP, &admin, nil, nil))
	if !opted.ReleaseHistory || opted.ProjectID != project || opted.Revision != 2 {
		t.Fatalf("opt-in %+v", opted)
	}
	kept := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+project+`"}`, adminIP, &admin, nil, nil))
	if !kept.ReleaseHistory || kept.ProjectTitle != "SECRET-PROJECT-NAME" || kept.Revision != 3 {
		t.Fatalf("same link cleared history %+v", kept)
	}
	published := f.do(http.MethodGet, readA, "", "203.0.113.20:1000", nil, nil, nil)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"release_history":true`) || strings.Contains(published.Body.String(), "SECRET-PROJECT-NAME") {
		t.Fatalf("public opt-in: %d %s", published.Code, published.Body)
	}
	assertPublicShape(t, published.Body.Bytes())
	privateProject := insertNode(t, d, tenantA, "PRJ-9", "project", "SECRET-PRIVATE-PROJECT", "SECRET-PRIVATE-BODY", "open", "", "{}")
	switched := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+privateProject+`"}`, adminIP, &admin, nil, nil))
	if switched.ReleaseHistory || switched.ProjectTitle != "SECRET-PRIVATE-PROJECT" || switched.Revision != 4 {
		t.Fatalf("new link kept history %+v", switched)
	}
	withheld := f.do(http.MethodGet, readA, "", "203.0.113.22:1000", nil, nil, nil)
	if withheld.Code != http.StatusOK || strings.Contains(withheld.Body.String(), "release_history") || strings.Contains(withheld.Body.String(), "SECRET-PRIVATE") {
		t.Fatalf("private project without opt-in: %d %s", withheld.Code, withheld.Body)
	}
	mustOK(t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":null}`, adminIP, &admin, nil, nil))
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true}`, adminIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("history without a project: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true,"project_id":"`+project+`","revision":4}`, adminIP, &admin, nil, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), paceLinkConflict) {
		t.Fatalf("history after the link was cleared: %d %s", rec.Code, rec.Body)
	}
	mustOK(t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+project+`"}`, adminIP, &admin, nil, nil))
	fact := findPublicCell(t, doc, "Statutory deadlines", "Northwind")
	if fact.Stance != "yes" || fact.Quote != "QUOTED-FACT-ALPHA on the public help page." || fact.Stale || fact.SourceURL != "https://northwind.example/deadlines" {
		t.Fatalf("fresh cell %+v", fact)
	}
	aged := findPublicCell(t, doc, "Owner assembly", "Northwind")
	if aged.Stance != "no" || !aged.Stale || aged.Quote != "STALE-QUOTED-FACT from an old brochure." {
		t.Fatalf("stale cell %+v", aged)
	}
	unknown := findPublicCell(t, doc, "Statutory deadlines", "Linden")
	if unknown.Stance != "unknown" || unknown.Quote != "" || unknown.SourceURL != "" {
		t.Fatalf("unapproved cell leaked %+v", unknown)
	}
	for _, wish := range doc.Wishes {
		if strings.Contains(wish.Title, "SECRET") {
			t.Fatalf("wish title %s", wish.Title)
		}
	}
	other := f.do(http.MethodGet, readB, "", "203.0.113.21:1000", nil, nil, nil)
	if other.Code != http.StatusOK || strings.Contains(other.Body.String(), "QUOTED-FACT-ALPHA") || strings.Contains(other.Body.String(), "Harbour catalog") {
		t.Fatalf("other tenant: %d %s", other.Code, other.Body)
	}

	before := correctionCount(t, d, tenantA)
	created := f.do(http.MethodPost, corrA, `{"competitor":"northwind","aspect":"Statutory deadlines","statement":"SECRET-CORRECTION-NOTE is not what the page says.","source_url":"https://northwind.example/correction","website":""}`, "203.0.113.70:1000", nil, nil, nil)
	if created.Code != http.StatusCreated || strings.TrimSpace(created.Body.String()) != `{"accepted":true}` {
		t.Fatalf("correction: %d %s", created.Code, created.Body)
	}
	if correctionCount(t, d, tenantA) != before+1 {
		t.Fatalf("stored corrections %d", correctionCount(t, d, tenantA))
	}
	honeypot := f.do(http.MethodPost, corrA, `{"competitor":"Northwind","aspect":"Owner assembly","statement":"HONEYPOT-CORRECTION should not be stored.","source_url":"","website":"https://evil.test"}`, "203.0.113.71:1000", nil, nil, nil)
	if honeypot.Code != http.StatusCreated || strings.TrimSpace(honeypot.Body.String()) != strings.TrimSpace(created.Body.String()) {
		t.Fatalf("honeypot: %d %s", honeypot.Code, honeypot.Body)
	}
	miss := f.do(http.MethodPost, corrA, `{"competitor":"Nobody","aspect":"Owner assembly","statement":"This rival is not listed anywhere.","source_url":"","website":""}`, "203.0.113.72:1000", nil, nil, nil)
	if miss.Code != http.StatusCreated || correctionCount(t, d, tenantA) != before+1 {
		t.Fatalf("unknown competitor stored: %d count %d", miss.Code, correctionCount(t, d, tenantA))
	}
	if rec := f.do(http.MethodPost, corrA, `{"competitor":"Northwind","aspect":"Owner assembly","statement":"A real correction.","email":"leak@example.com"}`, "203.0.113.73:1000", nil, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("email field: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, corrA, `{"competitor":"Northwind","aspect":"Owner assembly","statement":"Write to leak@example.com about this."}`, "203.0.113.74:1000", nil, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("address in statement: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, corrA, `{"competitor":"Northwind","aspect":"reader@example.com","statement":"The public page quotes the wrong line."}`, "203.0.113.75:1000", nil, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("address in aspect: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, corrA, `{"competitor":"reader%40example.com","aspect":"Owner assembly","statement":"The public page quotes the wrong line."}`, "203.0.113.76:1000", nil, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("encoded address in competitor: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, corrA, `{"competitor":"Northwind","aspect":"Owner assembly","statement":"The public page quotes the wrong line.","source_url":"https://northwind.example/reader%40example.com"}`, "203.0.113.77:1000", nil, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("encoded address in source: %d %s", rec.Code, rec.Body)
	}
	if correctionCount(t, d, tenantA) != before+1 {
		t.Fatal("rejected corrections were stored")
	}
	assertCorrectionColumns(t, d, tenantA)
	again := f.do(http.MethodGet, readA, "", "203.0.113.22:1000", nil, nil, nil)
	assertNoSecrets(t, again.Body.String(), secrets)
	adminMarket := decodeItem[marketAdmin](t, f.do(http.MethodGet, "/api/portal/market", "", adminIP, &admin, nil, nil))
	if len(adminMarket.Corrections) != 1 || !strings.Contains(adminMarket.Corrections[0].Statement, "SECRET-CORRECTION-NOTE") {
		t.Fatalf("admin corrections %+v", adminMarket.Corrections)
	}
	mustOK(t, f.do(http.MethodPost, "/api/portal/corrections/"+adminMarket.Corrections[0].ID+"/close", `{}`, adminIP, &admin, nil, nil))
	closedMarket := decodeItem[marketAdmin](t, f.do(http.MethodGet, "/api/portal/market", "", adminIP, &admin, nil, nil))
	if len(closedMarket.Corrections) != 0 {
		t.Fatalf("closed correction still listed %+v", closedMarket.Corrections)
	}

	limitIP := "203.0.113.90:1000"
	for i := 0; i < correctionLimit; i++ {
		rec := f.do(http.MethodPost, missing+"/corrections", `{"competitor":"Northwind","aspect":"Owner assembly","statement":"Limit this correction attempt."}`, limitIP, nil, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("limit attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	blockedMissing := f.do(http.MethodPost, missing+"/corrections", `{"competitor":"Northwind","aspect":"Owner assembly","statement":"Limit this correction attempt."}`, limitIP, nil, nil, nil)
	blockedOpen := f.do(http.MethodPost, corrA, `{"competitor":"Northwind","aspect":"Owner assembly","statement":"Limit this correction attempt."}`, limitIP, nil, nil, nil)
	if blockedMissing.Code != http.StatusTooManyRequests || blockedMissing.Header().Get("Retry-After") == "" {
		t.Fatalf("correction limit: %d", blockedMissing.Code)
	}
	sameResponse(t, blockedMissing, blockedOpen)

	if rec := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":false}`, adminIP, &admin, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	closed := f.do(http.MethodGet, readA, "", "203.0.113.30:1000", nil, nil, nil)
	unknownRes := f.do(http.MethodGet, missing, "", "203.0.113.31:1000", nil, nil, nil)
	if closed.Code != http.StatusNotFound {
		t.Fatalf("disabled portal: %d %s", closed.Code, closed.Body)
	}
	sameResponse(t, closed, unknownRes)
	assertNoSecrets(t, closed.Body.String(), secrets)
	closedPost := f.do(http.MethodPost, corrA, `{"competitor":"Northwind","aspect":"Owner assembly","statement":"A correction after close."}`, "203.0.113.32:1000", nil, nil, nil)
	unknownPost := f.do(http.MethodPost, missing+"/corrections", `{"competitor":"Northwind","aspect":"Owner assembly","statement":"A correction after close."}`, "203.0.113.33:1000", nil, nil, nil)
	if closedPost.Code != http.StatusNotFound {
		t.Fatalf("closed correction: %d %s", closedPost.Code, closedPost.Body)
	}
	sameResponse(t, closedPost, unknownPost)
	if correctionCount(t, d, tenantA) != before+1 {
		t.Fatal("closed portal stored a correction")
	}
}

type publicCell struct {
	Competitor  string `json:"competitor"`
	Stance      string `json:"stance"`
	Quote       string `json:"quote"`
	SourceURL   string `json:"source_url"`
	RetrievedOn string `json:"retrieved_on"`
	Stale       bool   `json:"stale"`
}

type publicMarketDoc struct {
	Wishes []struct {
		Title string `json:"title"`
	} `json:"wishes"`
	Comparison []struct {
		Aspect string       `json:"aspect"`
		Cells  []publicCell `json:"cells"`
	} `json:"comparison"`
	Pace *struct {
		Releases30d          *int `json:"releases_30d"`
		MedianReleaseGapDays *int `json:"median_release_gap_days"`
		WishToLiveMedianDays *int `json:"wish_to_live_median_days"`
	} `json:"pace"`
	ReleaseHistory bool `json:"release_history"`
}

func bytesRepeat() []byte {
	return []byte{11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11}
}

func mustOK(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func decodeItem[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var item T
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func putCell(t *testing.T, f *fixture, admin *tenant.Principal, aspect, competitor, stance, quote, source, day string) cellAdmin {
	t.Helper()
	body, err := json.Marshal(cellWrite{AspectID: aspect, CompetitorID: competitor, Stance: stance, Quote: quote, SourceURL: source, RetrievedOn: day})
	if err != nil {
		t.Fatal(err)
	}
	return decodeItem[cellAdmin](t, f.do(http.MethodPut, "/api/portal/cells", string(body), "203.0.113.10:1000", admin, nil, nil))
}

func historyHas(items []cellRevision, quote string) bool {
	for _, item := range items {
		if strings.Contains(item.Quote, quote) {
			return true
		}
	}
	return false
}

func findPublicCell(t *testing.T, doc publicMarketDoc, aspect, competitor string) publicCell {
	t.Helper()
	for _, row := range doc.Comparison {
		if row.Aspect != aspect {
			continue
		}
		for _, cell := range row.Cells {
			if cell.Competitor == competitor {
				return cell
			}
		}
	}
	t.Fatalf("missing %s / %s in %+v", aspect, competitor, doc.Comparison)
	return publicCell{}
}

func assertNoSecrets(t *testing.T, body string, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("public body leaked %s", secret)
		}
	}
}

func assertPublicShape(t *testing.T, body []byte) {
	t.Helper()
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"product": true, "catalog": true, "wishes": true, "key": true, "title": true, "summary": true,
		"status": true, "live_since": true, "legal_basis": true, "decline_reason": true, "votes": true,
		"comparison": true, "aspect": true, "cells": true, "competitor": true, "stance": true,
		"quote": true, "source_url": true, "retrieved_on": true, "stale": true, "pace": true,
		"releases_30d": true, "median_release_gap_days": true, "wish_to_live_median_days": true,
		"release_history": true,
	}
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			for key, child := range n {
				if !allowed[key] {
					t.Fatalf("unexpected public field %s", key)
				}
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(tree)
}

func portalDate(t *testing.T, d *dbtest.DB, tenantID string) string {
	t.Helper()
	var day string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (clock_timestamp() AT TIME ZONE 'UTC')::date::text`).Scan(&day)
	})
	if err != nil {
		t.Fatal(err)
	}
	return day
}

func shiftDay(t *testing.T, day string, n int) string {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.AddDate(0, 0, n).Format("2006-01-02")
}

func setCreated(t *testing.T, d *dbtest.DB, tenantID, id, at string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation', 'on', true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET created_at = $2::timestamptz WHERE id = $1::uuid`, id, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertLiveEvent(t *testing.T, d *dbtest.DB, tenantID, actor, feature, at string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO events(tenant_id, actor_principal_id, node_id, type, before, after, at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'node.updated', '{"state":"planned"}'::jsonb, '{"state":"live"}'::jsonb, $4::timestamptz)`,
			tenantID, actor, feature, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertRelease(t *testing.T, d *dbtest.DB, tenantID, project, release string, number int, atExpr string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if number == 1 {
			if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id, project_node_id) VALUES($1::uuid,$2::uuid)`, tenantID, project); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO journey_releases(tenant_id, release_node_id, project_node_id, number, state, released_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'released', `+atExpr+`)`, tenantID, release, project, number)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func correctionCount(t *testing.T, d *dbtest.DB, tenantID string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM portal_corrections`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func assertCorrectionColumns(t *testing.T, d *dbtest.DB, tenantID string) {
	t.Helper()
	banned := map[string]bool{"email": true, "address": true, "ip": true, "phone": true, "principal_id": true, "voter_hash": true, "actor_principal_id": true}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'portal_corrections'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			if banned[name] {
				t.Fatalf("corrections column %s", name)
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
