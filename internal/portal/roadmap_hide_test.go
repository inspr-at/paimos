// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestPublicRoadmapOmittedHideFlag(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{31}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	harbour := makeTenant(t, d, "harbour", "Harbour")
	person := makePerson(t, d, harbour, "Ada", "admin")
	insertNode(t, d, harbour, "PPR-1", "portal_product", "Harbour catalog", "Ready.", "published", "", "{}")
	setPortal(t, d, harbour, true)
	project := insertNode(t, d, harbour, "PRJ-1", "project", "Pace", "", "open", "", "{}")
	linkPace(t, d, harbour, project)

	add := func(key, title string, fields map[string]any) {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		insertNode(t, d, harbour, key, "work", title, "", "open", project, string(raw))
	}
	base := func(pill string, hide any, present bool) map[string]any {
		fields := map[string]any{
			"pill_en": pill, "pill_de": "Klare Notiz",
			"benefit_en": "People can read this.", "benefit_de": "Menschen können das lesen.",
			"roadmap_public": true, "roadmap_public_source": "person",
			"roadmap_public_by": person.ID, "roadmap_public_at": "2026-09-30T12:00:00Z",
			"tags": []any{"roadmap", "roadmap-r16"},
		}
		if present {
			fields["hide_from_release_notes"] = hide
		}
		return fields
	}
	add("TKT-1", "SECRET-OMITTED", base("Omitted hide note", nil, false))
	add("TKT-2", "SECRET-HIDDEN", base("Hidden release note", true, true))
	add("TKT-3", "SECRET-SHOWN", base("Shown release note", false, true))

	readIP := "203.0.113.92:1902"
	feed := f.do(http.MethodGet, "/api/public/portal/harbour/roadmap", "", readIP, nil, nil, nil)
	if feed.Code != http.StatusOK || feed.Header().Get("Cache-Control") != roadmapCache {
		t.Fatalf("feed: %d %s", feed.Code, feed.Header().Get("Cache-Control"))
	}
	var doc struct {
		Items []struct {
			Pill string `json:"pill_en"`
		} `json:"items"`
	}
	if err := json.Unmarshal(feed.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v %s", err, feed.Body)
	}
	got := map[string]bool{}
	for _, item := range doc.Items {
		got[item.Pill] = true
	}
	if !got["Omitted hide note"] || !got["Shown release note"] || got["Hidden release note"] || len(doc.Items) != 2 {
		t.Fatalf("pills %#v\n%s", got, feed.Body)
	}
	if strings.Contains(feed.Body.String(), "SECRET-") {
		t.Fatalf("feed leaked a title\n%s", feed.Body)
	}
	file := f.do(http.MethodGet, "/api/public/portal/harbour/roadmap.json", "", readIP, nil, nil, nil)
	if file.Body.String() != feed.Body.String() || file.Header().Get("Cache-Control") != roadmapCache {
		t.Fatalf("json file: %s %s", file.Header().Get("Cache-Control"), file.Body)
	}
	catalog := f.do(http.MethodGet, "/api/public/portal/harbour", "", readIP, nil, nil, nil)
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), `"roadmap":true`) || strings.Contains(catalog.Body.String(), "Hidden release note") || strings.Contains(catalog.Body.String(), "Omitted hide note") || strings.Contains(catalog.Body.String(), "SECRET-") {
		t.Fatalf("catalog: %d %s", catalog.Code, catalog.Body)
	}
	llms := f.do(http.MethodGet, "/api/public/portal/harbour/llms.txt", "", readIP, nil, nil, nil)
	if llms.Code != http.StatusOK || !strings.Contains(llms.Body.String(), "- [Roadmap](/portal/harbour/roadmap)\n") || !strings.Contains(llms.Body.String(), "- [Roadmap JSON](/portal/harbour/roadmap.json)\n") || strings.Contains(llms.Body.String(), "Hidden release note") || strings.Contains(llms.Body.String(), "Omitted hide note") || strings.Contains(llms.Body.String(), "SECRET-") {
		t.Fatalf("llms: %s", llms.Body)
	}
}
