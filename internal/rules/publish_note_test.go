// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// legacySnapshot is the snapshot shape from before publish notes. Its digest
// bytes are the compatibility baseline.
type legacySnapshot struct {
	SetID       string    `json:"set_id"`
	Scope       Scope     `json:"scope"`
	Name        string    `json:"name"`
	Revision    int64     `json:"revision"`
	Version     string    `json:"version"`
	SHA256      string    `json:"sha256"`
	Rules       []Rule    `json:"rules"`
	PublishedAt time.Time `json:"published_at"`
}

func TestEmptyNotePreservesSnapshotDigest(t *testing.T) {
	rule := testRule("safety", "Preserve safety.")
	rule.Strength = "locked"
	legacy := legacySnapshot{SetID: "floor", Scope: Scope{Layer: "company"}, Name: "test", Revision: 1, Version: "260928100000.0.0", Rules: []Rule{rule}}
	oldRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	current := Snapshot{SetID: legacy.SetID, Scope: legacy.Scope, Name: legacy.Name, Revision: legacy.Revision, Version: legacy.Version, Rules: legacy.Rules}
	newRaw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oldRaw, newRaw) {
		t.Fatalf("empty note changed digest bytes\nold %s\nnew %s", oldRaw, newRaw)
	}
	var loaded Snapshot
	if err = json.Unmarshal(oldRaw, &loaded); err != nil {
		t.Fatal(err)
	}
	loaded.PublishedAt = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	loaded.SHA256 = SnapshotDigest(loaded)
	if loaded.SHA256 != SnapshotDigest(loaded) || loaded.SHA256 != digest(oldRaw) {
		t.Fatal("historical snapshot failed its digest check")
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(oldRaw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["note"] = []byte(`""`)
	patched, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var explicit Snapshot
	if err = json.Unmarshal(patched, &explicit); err != nil {
		t.Fatal(err)
	}
	if SnapshotDigest(explicit) != digest(oldRaw) {
		t.Fatal("explicit empty note changed the historical digest")
	}
	noted := loaded
	noted.Note = "Kept the floor."
	noted.SHA256 = SnapshotDigest(noted)
	if noted.SHA256 == digest(oldRaw) || noted.SHA256 != SnapshotDigest(noted) {
		t.Fatal("publish note was left out of the immutable digest")
	}
	if strings.TrimSpace(normalizeOrEmpty(t, "  \n\t ")) != "" {
		t.Fatal("whitespace note was kept")
	}
	if _, err = normalizeNote(strings.Repeat("a", maxPublishNote+1)); err == nil {
		t.Fatal("overlong note accepted")
	}
	if _, err = normalizeNote("bad\x00note"); err == nil {
		t.Fatal("control character accepted")
	}
	kept, err := normalizeNote("  Kept the floor.\nStill locked.\t")
	if err != nil || kept != "Kept the floor.\nStill locked." {
		t.Fatal(err, kept)
	}
}

func normalizeOrEmpty(t *testing.T, note string) string {
	t.Helper()
	got, err := normalizeNote(note)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPublishNotePersistsWithoutRewritingHistory(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-note','Rules note') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	person := func(name, role string) tenant.Principal {
		p := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: name}
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, tid, name).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, tid, p.ID, role)
		return p
	}
	admin := person("owner", "admin")
	member := person("member", "member")
	agent := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: "builder", KeyCreatorID: admin.ID, Scopes: []string{"rules.read", "rules.write", "rules.publish"}}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','builder') RETURNING id::text`, tid).Scan(&agent.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, agent.ID, "admin")
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(p tenant.Principal, method, path string, in any, want int) []byte {
		t.Helper()
		body := ""
		if in != nil {
			body = string(jsonBytes(in))
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var layer Layer
	if err = json.Unmarshal(call(admin, "POST", "/api/rules/layers", Scope{Layer: "company"}, 200), &layer); err != nil {
		t.Fatal(err)
	}
	var set Set
	if err = json.Unmarshal(call(admin, "POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": "Safety"}, 200), &set); err != nil {
		t.Fatal(err)
	}
	locked := testRule("safety", "Keep the locked company floor.")
	locked.Strength = "locked"
	call(admin, "PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{ExpectedRevision: 1, Name: "Safety", Rules: []Rule{locked}}, 200)
	plain := map[string]any{"expected_revision": 2, "version": "260928120000.0.0"}
	call(agent, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120000.0.0", "note": "Agents do not publish."}, 403)
	call(member, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120000.0.0", "note": "Members do not publish company rules."}, 403)
	first := call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", plain, 200)
	if bytes.Contains(first, []byte(`"note"`)) {
		t.Fatalf("empty note was stored: %s", first)
	}
	replay := call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", plain, 200)
	if !bytes.Equal(first, replay) {
		t.Fatal("publish replay without a note changed the snapshot")
	}
	spaced := map[string]any{"expected_revision": 2, "version": "260928120000.0.0", "note": "  \n\t "}
	if !bytes.Equal(first, call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", spaced, 200)) {
		t.Fatal("whitespace note was not treated as empty")
	}
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120000.0.0", "note": "Too late."}, 409)
	locked.Text = "Keep the locked company floor in force."
	call(admin, "PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{ExpectedRevision: 2, Name: "Safety", Rules: []Rule{locked}}, 200)
	notedBody := map[string]any{"expected_revision": 3, "version": "260928120001.0.0", "note": "  Kept the floor.\nStill locked.\t"}
	second := call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", notedBody, 200)
	var published Snapshot
	if err = json.Unmarshal(second, &published); err != nil {
		t.Fatal(err)
	}
	if published.Note != "Kept the floor.\nStill locked." || published.SHA256 != SnapshotDigest(published) {
		t.Fatalf("note was not part of the snapshot: %+v", published)
	}
	same := call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 3, "version": "260928120001.0.0", "note": "Kept the floor.\nStill locked."}, 200)
	if !bytes.Equal(second, same) {
		t.Fatal("exact note replay changed the snapshot")
	}
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 3, "version": "260928120001.0.0"}, 409)
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 3, "version": "260928120001.0.0", "note": "A different note."}, 409)
	historical := call(admin, "GET", "/api/rules/sets/"+set.ID+"/versions/260928120000.0.0", nil, 200)
	if !bytes.Equal(first, historical) {
		t.Fatalf("noted publish rewrote the older snapshot\nfirst %s\nhistorical %s", first, historical)
	}
	blank := call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 3, "version": "260928120002.0.0", "note": "   "}, 200)
	if bytes.Contains(blank, []byte(`"note"`)) {
		t.Fatalf("whitespace note stored on a new version: %s", blank)
	}
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 3, "version": "260928120005.0.0", "note": strings.Repeat("a", maxPublishNote+1)}, 400)
	call(admin, "POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 1, "version": "260928120006.0.0", "note": "Stale revision."}, 409)
	restored := call(admin, "POST", "/api/rules/sets/"+set.ID+"/restore", map[string]any{"expected_revision": 3, "version": "260928120000.0.0", "new_version": "260928120003.0.0", "note": "Brought the floor back."}, 200)
	var restoredSnap Snapshot
	if err = json.Unmarshal(restored, &restoredSnap); err != nil || restoredSnap.Note != "Brought the floor back." {
		t.Fatalf("restore note: %s %v", restored, err)
	}
	if !bytes.Equal(first, call(admin, "GET", "/api/rules/sets/"+set.ID+"/versions/260928120000.0.0", nil, 200)) {
		t.Fatal("restore rewrote the source snapshot")
	}
	var still Snapshot
	if err = json.Unmarshal(call(admin, "GET", "/api/rules/sets/"+set.ID+"/versions/260928120001.0.0", nil, 200), &still); err != nil || still.Note != published.Note || still.SHA256 != published.SHA256 {
		t.Fatalf("source note changed: %+v", still)
	}
	again := call(admin, "POST", "/api/rules/sets/"+set.ID+"/restore", map[string]any{"expected_revision": 4, "version": "260928120001.0.0", "new_version": "260928120004.0.0", "note": "Published again."}, 200)
	var againSnap Snapshot
	if err = json.Unmarshal(again, &againSnap); err != nil || againSnap.Note != "Published again." || againSnap.Note == still.Note {
		t.Fatalf("new restore note did not stay distinct: %s", again)
	}
	if err = json.Unmarshal(call(admin, "GET", "/api/rules/sets/"+set.ID+"/versions/260928120001.0.0", nil, 200), &still); err != nil || still.Note != "Kept the floor.\nStill locked." {
		t.Fatalf("original note was rewritten: %+v", still)
	}
	silent := call(admin, "POST", "/api/rules/sets/"+set.ID+"/restore", map[string]any{"expected_revision": 5, "version": "260928120001.0.0", "new_version": "260928120007.0.0"}, 200)
	if bytes.Contains(silent, []byte(`"note"`)) {
		t.Fatalf("restore without a note copied the source note: %s", silent)
	}
	var eventNote, storedNote string
	var emptyHasNote bool
	err = d.Admin.QueryRow(t.Context(), `SELECT after->>'note' FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='rules.published' AND after->>'version'='260928120001.0.0'`, tid, set.ID).Scan(&eventNote)
	if err != nil || eventNote != "Kept the floor.\nStill locked." {
		t.Fatal("publish event missed the note", err, eventNote)
	}
	err = d.Admin.QueryRow(t.Context(), `SELECT jsonb_exists(after, 'note') FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='rules.published' AND after->>'version'='260928120000.0.0'`, tid, set.ID).Scan(&emptyHasNote)
	if err != nil || emptyHasNote {
		t.Fatal("empty note entered the publish event", err, emptyHasNote)
	}
	err = d.Admin.QueryRow(t.Context(), `SELECT coalesce(fields->'snapshot'->>'note','') FROM nodes WHERE parent_id=$1 AND rule_resource='version' AND fields->>'version'='260928120001.0.0'`, set.ID).Scan(&storedNote)
	if err != nil || storedNote != "Kept the floor.\nStill locked." {
		t.Fatal("version row missed the note", err, storedNote)
	}
	err = d.Admin.QueryRow(t.Context(), `SELECT after->>'note' FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='rules.restored' AND after->>'version'='260928120004.0.0'`, tid, set.ID).Scan(&eventNote)
	if err != nil || eventNote != "Published again." {
		t.Fatal("restore event missed the new note", err, eventNote)
	}
}
