// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/portal"
)

func TestConvertClearsForgedRoadmapPublication(t *testing.T) {
	p := newPrincipal(t, "roadmap-convert")
	customKind(t, p, "chore", "task")
	agent := routeAgent(t, p.TenantID)
	agent.Scopes = []string{"nodes.read", "nodes.write"}
	projectKind := kindBySlug(t, p, "project")
	taskKind := kindBySlug(t, p, "chore")
	ticketKind := kindBySlug(t, p, "work")
	productKind := kindBySlug(t, p, "portal_product")
	project := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Pace","state":"active"}`)
	mustNode(t, p, `{"kind_id":"`+productKind.ID+`","title":"Harbour catalog","state":"published","body":"Ready."}`)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO portal_settings(tenant_id, enabled) VALUES ($1::uuid, true)`, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		// This fixture represents a published legacy portal, not a new pilot.
		if _, err := tx.Exec(t.Context(), `UPDATE portal_products SET published=true, participation_policy='legacy' WHERE tenant_id=$1::uuid`, p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO portal_pace(tenant_id, project_node_id) VALUES ($1::uuid, $2::uuid)`, p.TenantID, project.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	content := map[string]any{}
	if err := json.Unmarshal([]byte(benefitFields), &content); err != nil {
		t.Fatal(err)
	}
	content["tags"] = []any{"roadmap", "roadmap-r16"}
	forged := cloneFields(content)
	forged["roadmap_public"] = true
	forged["roadmap_public_source"] = "person"
	forged["roadmap_public_by"] = p.ID
	forged["roadmap_public_at"] = "2026-09-30T12:00:00Z"
	task := mustNode(t, agent, string(mustJSON(t, map[string]any{
		"kind_id": taskKind.ID, "parent_id": project.ID, "title": "SECRET-FORGED-TITLE", "fields": forged,
	})))
	if !forgedPublication(t, task.Fields, p.ID) || !strings.Contains(string(task.Fields), "pill_en") {
		t.Fatalf("task did not keep the forged publication: %s", task.Fields)
	}
	if code, raw := call(t, &agent, http.MethodPost, "/api/nodes/"+task.ID+"/convert", `{"to_kind":"work"}`); code != http.StatusForbidden {
		t.Fatalf("agent convert: %d %s", code, raw)
	}
	same, _ := getNode(t, p, task.ID)
	if same.KindID != taskKind.ID || !forgedPublication(t, same.Fields, p.ID) {
		t.Fatalf("agent convert changed the task: %s %s", same.KindID, same.Fields)
	}
	status, raw := call(t, &p, http.MethodPost, "/api/nodes/"+task.ID+"/convert", `{"to_kind":"chore"}`)
	kept := decode[nodeJSON](t, status, raw, http.StatusOK)
	if kept.KindID != taskKind.ID || !forgedPublication(t, kept.Fields, p.ID) {
		t.Fatalf("same kind stripped the task: %s", kept.Fields)
	}

	mod := portal.New(appPool, false, bytes.Repeat([]byte{31}, 32))
	mux := http.NewServeMux()
	mod.Mount(mux)
	slug := "roadmap-convert"
	assertUnpublished := func() {
		t.Helper()
		feed, catalog, llms := publicBodies(t, mux, slug)
		if strings.Contains(feed, "Clear release notes") || strings.Contains(feed, "SECRET-FORGED-TITLE") || strings.Contains(catalog, `"roadmap":true`) || strings.Contains(llms, "[Roadmap]") || strings.Contains(catalog, "SECRET-FORGED-TITLE") || strings.Contains(llms, "SECRET-FORGED-TITLE") {
			t.Fatalf("published early\nfeed %s\ncatalog %s\nllms %s", feed, catalog, llms)
		}
	}
	assertPublished := func() {
		t.Helper()
		feed, catalog, llms := publicBodies(t, mux, slug)
		if !strings.Contains(feed, `"pill_en":"Clear release notes"`) || strings.Contains(feed, "SECRET-FORGED-TITLE") || !strings.Contains(catalog, `"roadmap":true`) || strings.Contains(catalog, "Clear release notes") || strings.Contains(catalog, "SECRET-FORGED-TITLE") || !strings.Contains(llms, "- [Roadmap](/portal/"+slug+"/roadmap)\n") || !strings.Contains(llms, "- [Roadmap JSON](/portal/"+slug+"/roadmap.json)\n") || strings.Contains(llms, "Clear release notes") || strings.Contains(llms, "SECRET-FORGED-TITLE") {
			t.Fatalf("published surface\nfeed %s\ncatalog %s\nllms %s", feed, catalog, llms)
		}
	}

	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+task.ID+"/convert", `{"to_kind":"work"}`)
	converted := decode[nodeJSON](t, status, raw, http.StatusOK)
	if converted.KindID != ticketKind.ID || publicationPresent(t, converted.Fields) || !strings.Contains(string(converted.Fields), "pill_en") || !strings.Contains(string(converted.Fields), "roadmap") {
		t.Fatalf("convert kept a publication: %s", converted.Fields)
	}
	assertUnpublished()

	undoMod := events.New(appPool, events.WithUndoHandlers(UndoHandlers()))
	undo := func() {
		t.Helper()
		id := latestKindChange(t, p.TenantID)
		status, raw := callAs(t, undoMod, &p, http.MethodPost, "/api/events/"+strconv.FormatInt(id, 10)+"/undo", "")
		if status != http.StatusCreated {
			t.Fatalf("undo: %d %s", status, raw)
		}
	}
	undo()
	restored, _ := getNode(t, p, task.ID)
	if restored.KindID != taskKind.ID || !forgedPublication(t, restored.Fields, p.ID) {
		t.Fatalf("undo dropped the task publication: %s %s", restored.KindID, restored.Fields)
	}
	assertUnpublished()

	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+task.ID+"/convert", `{"to_kind":"work"}`)
	converted = decode[nodeJSON](t, status, raw, http.StatusOK)
	if converted.KindID != ticketKind.ID || publicationPresent(t, converted.Fields) {
		t.Fatalf("second convert: %s", converted.Fields)
	}
	assertUnpublished()

	if code, raw := call(t, &agent, http.MethodPost, "/api/nodes", string(mustJSON(t, map[string]any{
		"kind_id": ticketKind.ID, "parent_id": project.ID, "title": "Agent ticket", "fields": forged,
	}))); code != http.StatusForbidden {
		t.Fatalf("agent create: %d %s", code, raw)
	}
	approved := cloneFields(content)
	approved["roadmap_public"] = true
	if code, raw := call(t, &agent, http.MethodPatch, "/api/nodes/"+task.ID, string(mustJSON(t, map[string]any{"fields": approved}))); code != http.StatusForbidden {
		t.Fatalf("agent approve: %d %s", code, raw)
	}
	stored, _ := getNode(t, p, task.ID)
	if stored.KindID != ticketKind.ID || publicationPresent(t, stored.Fields) {
		t.Fatalf("agent approve wrote: %s", stored.Fields)
	}

	status, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+task.ID, string(mustJSON(t, map[string]any{"fields": approved})))
	published := decode[nodeJSON](t, status, raw, http.StatusOK)
	fields := routeFieldMap(t, published.Fields)
	if fields["roadmap_public"] != true || fields["roadmap_public_source"] != "person" || fields["roadmap_public_by"] != p.ID || fields["roadmap_public_at"] == "" || fields["roadmap_public_at"] == "2026-09-30T12:00:00Z" {
		t.Fatalf("person stamp: %#v", fields)
	}
	assertPublished()

	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+task.ID+"/convert", `{"to_kind":"chore"}`)
	demoted := decode[nodeJSON](t, status, raw, http.StatusOK)
	if demoted.KindID != taskKind.ID || publicationPresent(t, demoted.Fields) {
		t.Fatalf("demote kept the approval: %s", demoted.Fields)
	}
	assertUnpublished()
	undo()
	back, _ := getNode(t, p, task.ID)
	backFields := routeFieldMap(t, back.Fields)
	if back.KindID != ticketKind.ID || backFields["roadmap_public"] != true || backFields["roadmap_public_source"] != "person" || backFields["roadmap_public_by"] != p.ID {
		t.Fatalf("undo lost the person approval: %s %s", back.KindID, back.Fields)
	}
	assertPublished()
}

func cloneFields(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+4)
	for key, value := range in {
		out[key] = value
	}
	return out
}

func forgedPublication(t *testing.T, raw []byte, personID string) bool {
	t.Helper()
	fields := routeFieldMap(t, raw)
	return fields["roadmap_public"] == true && fields["roadmap_public_source"] == "person" && fields["roadmap_public_by"] == personID && fields["roadmap_public_at"] == "2026-09-30T12:00:00Z"
}

func publicationPresent(t *testing.T, raw []byte) bool {
	t.Helper()
	fields := routeFieldMap(t, raw)
	for _, key := range []string{"roadmap_public", "roadmap_public_source", "roadmap_public_by", "roadmap_public_at"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

func publicBodies(t *testing.T, h http.Handler, slug string) (feed, catalog, llms string) {
	t.Helper()
	get := func(path string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "203.0.113.91:1901"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	return get("/api/public/portal/" + slug + "/roadmap"), get("/api/public/portal/" + slug), get("/api/public/portal/" + slug + "/llms.txt")
}
