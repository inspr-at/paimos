// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
)

func TestPublicRoadmapTarget(t *testing.T) {
	number := 16
	target, rank, ok := publicRoadmapTarget([]string{"roadmap-later", "roadmap-r30"}, &number, "Release 99")
	if !ok || target != "r16" || rank != 16 {
		t.Fatalf("assignment wins: %q %d %v", target, rank, ok)
	}
	target, rank, ok = publicRoadmapTarget([]string{"roadmap-r17", "Roadmap-R3", "roadmap-later"}, nil, "Release 18")
	if !ok || target != "r3" || rank != 3 {
		t.Fatalf("smallest tag: %q %d %v", target, rank, ok)
	}
	target, rank, ok = publicRoadmapTarget([]string{"ROADMAP", "roadmap-later"}, nil, "")
	if !ok || target != "later" || rank != roadmapLaterRank {
		t.Fatalf("later tag: %q %d %v", target, rank, ok)
	}
	target, rank, ok = publicRoadmapTarget([]string{"roadmap"}, nil, "Release 18")
	if !ok || target != "r18" || rank != 18 {
		t.Fatalf("label: %q %d %v", target, rank, ok)
	}
	target, _, ok = publicRoadmapTarget([]string{"roadmap"}, nil, "later")
	if !ok || target != "later" {
		t.Fatalf("later label: %q %v", target, ok)
	}
	if _, _, ok = publicRoadmapTarget([]string{"roadmap"}, nil, "v4.7.8"); ok {
		t.Fatal("version label became a target")
	}
	if _, _, ok = publicRoadmapTarget([]string{"roadmap"}, nil, "r016"); ok {
		t.Fatal("leading zero became a target")
	}
	if _, _, ok = publicRoadmapTarget([]string{"roadmap-r016"}, nil, ""); ok {
		t.Fatal("leading-zero tag became a target")
	}
	if _, _, ok = publicRoadmapTarget([]string{"roadmap"}, nil, ""); ok {
		t.Fatal("bare roadmap tag became a target")
	}
}

func TestPublicRoadmapFeed(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{31}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	harbour := makeTenant(t, d, "harbour", "Harbour")
	other := makeTenant(t, d, "other-yard", "Other")
	closed := makeTenant(t, d, "closed-yard", "Closed")
	unlinked := makeTenant(t, d, "unlinked-yard", "Unlinked")
	person := makePerson(t, d, harbour, "Ada", "admin")
	otherPerson := makePerson(t, d, other, "Bea", "admin")
	agentID := makeAgent(t, d, harbour, "roadmap-agent", "admin", nil).ID

	insertNode(t, d, harbour, "PPR-1", "portal_product", "Harbour catalog", "Work that is ready in the morning.", "published", "", "{}")
	insertNode(t, d, closed, "PPR-1", "portal_product", "Closed catalog", "Closed.", "published", "", "{}")
	insertNode(t, d, unlinked, "PPR-1", "portal_product", "Unlinked catalog", "No pace.", "published", "", "{}")
	setPortal(t, d, harbour, true)
	setPortal(t, d, closed, false)
	setPortal(t, d, unlinked, true)

	project := insertNode(t, d, harbour, "PRJ-1", "project", "SECRET-PROJECT-NAME", "SECRET-PROJECT-BODY", "open", "", "{}")
	otherProject := insertNode(t, d, harbour, "PRJ-2", "project", "SECRET-OTHER-PROJECT", "SECRET-OTHER-BODY", "open", "", "{}")
	epic := insertNode(t, d, harbour, "EPC-1", "epic", "SECRET-EPIC", "", "open", project, "{}")
	linkPace(t, d, harbour, project)

	add := func(key, title, state, parent string, fields map[string]any) string {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return insertNode(t, d, harbour, key, "ticket", title, "", state, parent, string(raw))
	}
	approved := func(pill string, tags any) map[string]any {
		return map[string]any{
			"pill_en": pill, "pill_de": "Klare Notiz",
			"benefit_en": "People can read this.", "benefit_de": "Menschen können das lesen.",
			"hide_from_release_notes": false,
			"roadmap_public":          true, "roadmap_public_source": "person",
			"roadmap_public_by": person.ID, "roadmap_public_at": "2026-09-30T12:00:00Z",
			"tags": tags,
		}
	}

	assigned := approved("Assigned release note", []any{"roadmap", "roadmap-later"})
	assigned["priority"] = "high"
	assignedID := add("TKT-1", "SECRET-ASSIGNED", "open", project, assigned)

	nested := approved("Nested roadmap note", []any{"roadmap", "roadmap-r15"})
	nested["priority"] = "low"
	add("TKT-2", "SECRET-NESTED", "open", epic, nested)

	high := approved("High release note", []any{"roadmap", "roadmap-r16"})
	high["priority"] = "high"
	high["benefit_en"] = "People can read the high note."
	add("TKT-3", "SECRET-HIGH", "in_progress", project, high)

	low := approved("Low release note", []any{"roadmap", "roadmap-r16"})
	low["priority"] = "low"
	add("TKT-4", "SECRET-LOW", "open", project, low)

	next := approved("Next release note", []any{map[string]any{"name": "Roadmap"}, map[string]any{"name": "Roadmap-R17"}})
	next["priority"] = "medium"
	add("TKT-5", "SECRET-NEXT", "open", project, next)

	label := approved("Label release note", []any{"roadmap"})
	label["priority"] = "low"
	label["release"] = "Release 18"
	add("TKT-6", "SECRET-LABEL", "open", project, label)

	objectLabel := approved("Object release note", []any{"roadmap"})
	objectLabel["priority"] = "low"
	objectLabel["release"] = map[string]any{"label": "Release 19"}
	add("TKT-7", "SECRET-OBJECT", "open", project, objectLabel)

	hidden := approved("Visible after unhide", []any{"roadmap", "roadmap-r21"})
	hidden["priority"] = "high"
	hidden["hide_from_release_notes"] = true
	hidden["benefit_en"] = "SECRET-HIDDEN-FREEZE"
	hidden["benefit_de"] = "SECRET-HIDDEN-FREEZE"
	hiddenID := add("TKT-8", "SECRET-HIDDEN", "open", project, hidden)

	frozen := approved("Frozen public note", []any{"roadmap", "roadmap-r22"})
	frozen["priority"] = "low"
	frozen["benefit_en"] = "The frozen line is public."
	frozen["benefit_de"] = "Die eingefrorene Zeile ist öffentlich."
	frozenID := add("TKT-9", "SECRET-FROZEN", "done", project, frozen)

	later := approved("Later high note", []any{"ROADMAP", "roadmap-later"})
	later["priority"] = "high"
	add("TKT-10", "SECRET-LATER", "done", project, later)

	missingTarget := approved("Missing target note", []any{"roadmap"})
	add("TKT-11", "SECRET-MISSING-TARGET", "open", project, missingTarget)
	versionLabel := approved("Version label note", []any{"roadmap"})
	versionLabel["release"] = "v4.7.8"
	add("TKT-12", "SECRET-VERSION", "open", project, versionLabel)
	hiddenLive := approved("Hidden roadmap note", []any{"roadmap", "roadmap-r30"})
	hiddenLive["hide_from_release_notes"] = true
	add("TKT-13", "SECRET-HIDDEN-LIVE", "open", project, hiddenLive)
	add("TKT-14", "SECRET-CANCELLED", "cancelled", project, approved("Cancelled roadmap note", []any{"roadmap", "roadmap-r31"}))
	deleted := add("TKT-15", "SECRET-DELETED", "open", project, approved("Deleted roadmap note", []any{"roadmap", "roadmap-r32"}))
	softDeleteNode(t, d, harbour, deleted)
	add("TKT-16", "SECRET-UNTAGGED", "open", project, approved("Untagged public note", []any{"ship"}))
	short := approved("Nope", []any{"roadmap", "roadmap-r33"})
	short["benefit_en"] = "SECRET-GAP-NOTE"
	short["benefit_de"] = "SECRET-GAP-NOTE"
	add("TKT-17", "SECRET-SHORT", "open", project, short)
	markup := approved("Bad <markup> note", []any{"roadmap", "roadmap-r34"})
	markup["benefit_en"] = "SECRET-MARKUP"
	add("TKT-18", "SECRET-MARKUP-TITLE", "open", project, markup)
	forged := approved("Agent source note", []any{"roadmap", "roadmap-r35"})
	forged["roadmap_public_source"] = "agent"
	add("TKT-19", "SECRET-AGENT-SOURCE", "open", project, forged)
	agentBy := approved("Agent owner note", []any{"roadmap", "roadmap-r36"})
	agentBy["roadmap_public_by"] = agentID
	add("TKT-20", "SECRET-AGENT-BY", "open", project, agentBy)
	unknownBy := approved("Unknown person note", []any{"roadmap", "roadmap-r37"})
	unknownBy["roadmap_public_by"] = "33333333-3333-3333-3333-333333333333"
	add("TKT-21", "SECRET-UNKNOWN-BY", "open", project, unknownBy)
	add("TKT-22", "SECRET-OTHER-PROJECT-TICKET", "open", otherProject, approved("Other project note", []any{"roadmap", "roadmap-r12"}))
	foreign := approved("Foreign tenant note", []any{"roadmap", "roadmap-r12"})
	foreign["roadmap_public_by"] = otherPerson.ID
	rawForeign, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	insertNode(t, d, other, "TKT-1", "ticket", "SECRET-FOREIGN", "", "open", "", string(rawForeign))

	relAssigned := insertNode(t, d, harbour, "REL-4", "release", "SECRET-RELEASE-TITLE", "", "open", project, "{}")
	publishRelease(t, d, harbour, project, relAssigned, 4, "planning", "", 0, []string{assignedID})
	relFrozen := insertNode(t, d, harbour, "REL-20", "release", "SECRET-FROZEN-RELEASE", "", "open", project, "{}")
	publishRelease(t, d, harbour, project, relFrozen, 20, "released", "260930120000.0.0", -time.Hour, []string{hiddenID, frozenID})
	shown := approved("Visible after unhide", []any{"roadmap", "roadmap-r21"})
	shown["priority"] = "high"
	shown["benefit_en"] = "Visible after the freeze."
	shown["benefit_de"] = "Sichtbar nach dem Einfrieren."
	rawShown, err := json.Marshal(shown)
	if err != nil {
		t.Fatal(err)
	}
	setNodeFields(t, d, harbour, hiddenID, string(rawShown))

	const readIP = "203.0.113.70:1700"
	before := f.do(http.MethodGet, "/api/public/portal/harbour/roadmap", "", readIP, nil, nil, nil)
	if before.Code != http.StatusOK || before.Header().Get("Cache-Control") != roadmapCache {
		t.Fatalf("roadmap: %d %s %s", before.Code, before.Header().Get("Cache-Control"), before.Body)
	}
	assertRoadmap(t, before.Body.Bytes(), []roadExpect{
		{"Assigned release note", "r4", "planned"},
		{"Nested roadmap note", "r15", "planned"},
		{"High release note", "r16", "in_progress"},
		{"Low release note", "r16", "planned"},
		{"Next release note", "r17", "planned"},
		{"Label release note", "r18", "planned"},
		{"Object release note", "r19", "planned"},
		{"Visible after unhide", "r20", "planned"},
		{"Frozen public note", "r20", "shipped"},
		{"Later high note", "later", "shipped"},
	})
	for _, secret := range []string{"SECRET-", "TKT-", "REL-", "PRJ-", "EPC-", "hide_from_release_notes", hiddenID, frozenID, "internal_note"} {
		if strings.Contains(before.Body.String(), secret) {
			t.Fatalf("leaked %s\n%s", secret, before.Body)
		}
	}

	setReleaseHistory(t, d, harbour, true)
	after := f.do(http.MethodGet, "/api/public/portal/harbour/roadmap", "", readIP, nil, nil, nil)
	assertRoadmap(t, after.Body.Bytes(), []roadExpect{
		{"Assigned release note", "r4", "planned"},
		{"Nested roadmap note", "r15", "planned"},
		{"High release note", "r16", "in_progress"},
		{"Low release note", "r16", "planned"},
		{"Next release note", "r17", "planned"},
		{"Label release note", "r18", "planned"},
		{"Object release note", "r19", "planned"},
		{"Visible after unhide", "r20", "planned"},
		{"Later high note", "later", "shipped"},
	})
	if strings.Contains(after.Body.String(), "The frozen line is public.") || strings.Contains(after.Body.String(), "SECRET-") {
		t.Fatalf("shipped item stayed or leaked\n%s", after.Body)
	}
	releases := f.do(http.MethodGet, "/api/public/portal/harbour/releases", "", readIP, nil, nil, nil)
	if releases.Code != http.StatusOK || !strings.Contains(releases.Body.String(), "The frozen line is public.") || strings.Contains(releases.Body.String(), "SECRET-HIDDEN-FREEZE") || strings.Contains(releases.Body.String(), "Visible after the freeze.") {
		t.Fatalf("history: %d %s", releases.Code, releases.Body)
	}

	file := f.do(http.MethodGet, "/api/public/portal/harbour/roadmap.json", "", readIP, nil, nil, nil)
	if file.Body.String() != after.Body.String() || file.Header().Get("Cache-Control") != roadmapCache {
		t.Fatalf("json file: %s %s", file.Header().Get("Cache-Control"), file.Body)
	}
	catalog := f.do(http.MethodGet, "/api/public/portal/harbour", "", readIP, nil, nil, nil)
	if catalog.Code != http.StatusOK || catalog.Header().Get("Cache-Control") != "no-store" || !strings.Contains(catalog.Body.String(), `"roadmap":true`) || strings.Contains(catalog.Body.String(), "People can read") || strings.Contains(catalog.Body.String(), "The frozen line") {
		t.Fatalf("catalog: %d %s %s", catalog.Code, catalog.Header().Get("Cache-Control"), catalog.Body)
	}
	llms := f.do(http.MethodGet, "/api/public/portal/harbour/llms.txt", "", readIP, nil, nil, nil)
	if llms.Code != http.StatusOK || !strings.Contains(llms.Body.String(), "- [Roadmap](/portal/harbour/roadmap)\n") || !strings.Contains(llms.Body.String(), "- [Roadmap JSON](/portal/harbour/roadmap.json)\n") || strings.Contains(llms.Body.String(), "High release note") || strings.Contains(llms.Body.String(), "People can read") {
		t.Fatalf("llms: %s", llms.Body)
	}

	empty := f.do(http.MethodGet, "/api/public/portal/unlinked-yard/roadmap", "", "203.0.113.71:1701", nil, nil, nil)
	assertRoadmap(t, empty.Body.Bytes(), nil)
	if empty.Header().Get("Cache-Control") != roadmapCache {
		t.Fatalf("empty cache: %s", empty.Header().Get("Cache-Control"))
	}
	quiet := f.do(http.MethodGet, "/api/public/portal/unlinked-yard", "", "203.0.113.71:1701", nil, nil, nil)
	if strings.Contains(quiet.Body.String(), `"roadmap"`) {
		t.Fatalf("empty catalog advertised a roadmap\n%s", quiet.Body)
	}
	quietLlms := f.do(http.MethodGet, "/api/public/portal/unlinked-yard/llms.txt", "", "203.0.113.71:1701", nil, nil, nil)
	if strings.Contains(quietLlms.Body.String(), "Roadmap") {
		t.Fatalf("empty llms linked a roadmap\n%s", quietLlms.Body)
	}
	closedRec := f.do(http.MethodGet, "/api/public/portal/closed-yard/roadmap", "", "203.0.113.72:1702", nil, nil, nil)
	if closedRec.Code != http.StatusNotFound || closedRec.Header().Get("Cache-Control") != "no-store" || strings.Contains(closedRec.Body.String(), "Closed catalog") {
		t.Fatalf("closed: %d %s %s", closedRec.Code, closedRec.Header().Get("Cache-Control"), closedRec.Body)
	}

	page := []byte("<!doctype html><title>SPA</title><p>SPA-MARKER</p>")
	handler := (&httpapi.Server{
		Modules: []httpapi.Module{m},
		Web:     fstest.MapFS{"index.html": &fstest.MapFile{Data: page}},
	}).Handler()
	call := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "203.0.113.73:1703"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	root := call(http.MethodGet, "/portal/harbour/roadmap.json")
	api := call(http.MethodGet, "/api/public/portal/harbour/roadmap.json")
	if root.Code != http.StatusOK || root.Body.String() != api.Body.String() || strings.Contains(root.Body.String(), "SPA-MARKER") || root.Header().Get("Cache-Control") != roadmapCache {
		t.Fatalf("root file: %d %s %s", root.Code, root.Header().Get("Cache-Control"), root.Body)
	}
	if strings.Contains(call(http.MethodGet, "/portal/harbour/roadmap").Body.String(), "SPA-MARKER") == false {
		t.Fatal("roadmap page left the SPA")
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	headReq, err := http.NewRequest(http.MethodHead, ts.URL+"/api/public/portal/harbour/roadmap", nil)
	if err != nil {
		t.Fatal(err)
	}
	headRes, err := ts.Client().Do(headReq)
	if err != nil {
		t.Fatal(err)
	}
	defer headRes.Body.Close()
	headBody, _ := io.ReadAll(headRes.Body)
	if headRes.StatusCode != http.StatusOK || len(headBody) != 0 || headRes.Header.Get("Cache-Control") != roadmapCache {
		t.Fatalf("head: %d %q %s", headRes.StatusCode, headBody, headRes.Header.Get("Cache-Control"))
	}
}

type roadExpect struct {
	pill, target, status string
}

func assertRoadmap(t *testing.T, raw []byte, want []roadExpect) {
	t.Helper()
	var doc struct {
		Schema string           `json:"schema"`
		Items  []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("json: %v %s", err, raw)
	}
	if doc.Schema != roadmapSchema {
		t.Fatalf("schema %s", doc.Schema)
	}
	if len(doc.Items) != len(want) {
		t.Fatalf("items %d want %d\n%s", len(doc.Items), len(want), raw)
	}
	allowed := map[string]bool{"pill_en": true, "pill_de": true, "benefit_en": true, "benefit_de": true, "target": true, "status": true}
	for i, item := range doc.Items {
		for key := range item {
			if !allowed[key] {
				t.Fatalf("extra key %s in %v", key, item)
			}
		}
		if item["pill_en"] != want[i].pill || item["target"] != want[i].target || item["status"] != want[i].status {
			t.Fatalf("item %d: %#v want %+v", i, item, want[i])
		}
	}
}
