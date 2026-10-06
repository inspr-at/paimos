// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestProvenanceVisibilityIdempotencyAndTamper(t *testing.T) {
	f := fixture(t)
	const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	lease := "provenance-lease-00000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host",
		"harness_session_ref": "provenance-ref-" + uid(), "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker",
	}, "")
	expect(t, w, 201)
	sessionID := decode(t, w)["id"].(string)
	path := base + "/" + sessionID + "/provenance"
	item := func(hash string) map[string]any {
		return map[string]any{"kind": "agents", "logical_name": "AGENTS.md", "hash_kind": "content", "content_sha256": hash, "byte_size": 12}
	}
	prompt := map[string]any{"kind": "prompt_template", "logical_name": "prompt-template", "hash_kind": "absent", "version": "260927120000.0.0"}
	contentPrompt := map[string]any{"kind": "prompt_template", "logical_name": "prompt-template", "hash_kind": "content", "content_sha256": hashA, "version": "260927120000.0.0"}
	body := map[string]any{"items": []any{prompt, item(hashA)}}

	expect(t, f.call(f.person, "POST", path, body, lease), 403)
	expect(t, f.call(f.agent, "POST", path, body, "wrong-provenance-lease-000000000000"), 403)
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": []any{map[string]any{"kind": "agents", "logical_name": "/Users/hidden/.ssh/id_rsa", "content_sha256": hashA, "byte_size": 12, "content": "secret prompt"}}}, lease), 400)
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": []any{map[string]any{"kind": "prompt_template", "logical_name": "prompt-template", "hash_kind": "content", "content_sha256": hashA, "version": "v1", "byte_size": 40}}}, lease), 400)
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": []any{map[string]any{"kind": "prompt_template", "logical_name": "prompt-template", "hash_kind": "absent", "content_sha256": hashA, "version": "260927120000.0.0"}}}, lease), 400)
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": []any{map[string]any{"kind": "prompt_template", "logical_name": "prompt-template", "hash_kind": "content", "version": "260927120000.0.0"}}}, lease), 400)
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": []any{map[string]any{"kind": "rules_merged", "logical_name": "merged-rules", "hash_kind": "content", "content_sha256": hashA, "version": "260927120000.0.0", "byte_size": 12}}}, lease), 400)
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": []any{map[string]any{"kind": "rules_set", "logical_name": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "hash_kind": "content", "content_sha256": hashA, "version": "260927120000.0.0"}}}, lease), 400)

	w = f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 200)
	first := decode(t, w)
	versionHash := sha256.Sum256([]byte("aeon.harness.provenance.prompt-template\x00" + "260927120000.0.0"))
	if first["replayed"] != false || first["revision"].(float64) != 1 || strings.Contains(w.Body.String(), lease) || strings.Contains(w.Body.String(), "/Users") || strings.Contains(w.Body.String(), "secret prompt") || strings.Contains(w.Body.String(), hex.EncodeToString(versionHash[:])) {
		t.Fatalf("first record %s", w.Body.String())
	}
	sawAbsent := false
	for _, raw := range first["items"].([]any) {
		got := raw.(map[string]any)
		if got["logical_name"] != "prompt-template" {
			continue
		}
		sawAbsent = got["hash_kind"] == "absent" && got["content_sha256"] == nil && got["version"] == "260927120000.0.0"
	}
	if !sawAbsent {
		t.Fatalf("absent prompt digest %s", w.Body.String())
	}
	w = f.call(f.agent, "POST", path, map[string]any{"items": []any{item(hashA), prompt}}, lease)
	expect(t, w, 200)
	replay := decode(t, w)
	if replay["replayed"] != true || replay["id"] != first["id"] || replay["revision"].(float64) != 1 {
		t.Fatalf("replay %s", w.Body.String())
	}
	w = f.call(f.agent, "POST", path, map[string]any{"items": []any{item(hashB), contentPrompt}}, lease)
	expect(t, w, 200)
	second := decode(t, w)
	if second["replayed"] != false || second["revision"].(float64) != 2 || second["id"] == first["id"] || !strings.Contains(w.Body.String(), `"hash_kind":"content"`) {
		t.Fatalf("append %s", w.Body.String())
	}

	hidden := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	outsider := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	other := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Agent}
	otherProject := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, p := range []tenant.Principal{hidden, outsider, reader} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person',$3)`, p.TenantID, p.ID, "reader"); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','other')`, other.TenantID, other.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'PV1-2',kind_id,'Other project' FROM nodes WHERE id=$3`, f.person.TenantID, otherProject, f.project)
		return err
	})
	bindProject := func(p tenant.Principal, project string) {
		t.Helper()
		if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, p.TenantID, p.ID, project); err != nil {
			t.Fatal(err)
		}
	}
	bindProject(outsider, otherProject)
	bindProject(reader, f.project)
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "member")
	otherKey := mintKey(t, f, other, []string{"harness.worker", "harness.read"})
	readKey := mintKey(t, f, f.agent, []string{"harness.read"})

	expect(t, f.call(hidden, "GET", path, nil, ""), 404)
	expect(t, f.call(outsider, "GET", path, nil, ""), 404)
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	w = f.call(reader, "GET", path, nil, "")
	expect(t, w, 200)
	page := decode(t, w)
	revisions := page["revisions"].([]any)
	if len(revisions) != 2 || page["truncated"] != false || strings.Contains(w.Body.String(), lease) {
		t.Fatalf("page %s", w.Body.String())
	}
	older := revisions[1].(map[string]any)
	kept := older["items"].([]any)[0].(map[string]any)["content_sha256"]
	if older["revision"].(float64) != 1 || (kept != hashA && revisions[1].(map[string]any)["items"].([]any)[1].(map[string]any)["content_sha256"] != hashA) {
		t.Fatalf("revision 1 changed: %s", w.Body.String())
	}
	expect(t, callBearer(f, f.agent, readKey, "POST", path, body, lease), 403)
	expect(t, callBearer(f, f.agent, readKey, "GET", path, nil, ""), 200)
	expect(t, callBearer(f, other, otherKey, "POST", path, body, lease), 403)

	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE harness_instruction_provenance_items SET content_sha256=$1 WHERE content_sha256=$2`, hashB, hashA)
		return e
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update err %v", err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `INSERT INTO harness_instruction_provenance_items(tenant_id, provenance_id, ordinal, kind, logical_name, hash_kind, content_sha256, byte_size) SELECT tenant_id, id, 2, 'agents', '/Users/hidden/.ssh/id_rsa', 'content', $1, 1 FROM harness_instruction_provenance WHERE revision=1`, hashB)
		return e
	})
	if err == nil {
		t.Fatal("path logical name was stored")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var n int
		var payload string
		e := tx.QueryRow(t.Context(), `SELECT count(*), coalesce(string_agg(coalesce(before::text,'') || after::text, ''), '') FROM events WHERE type='harness.provenance_recorded'`).Scan(&n, &payload)
		if e != nil {
			return e
		}
		if n != 2 || !strings.Contains(payload, hashA) || strings.Contains(payload, lease) || strings.Contains(payload, "/Users") {
			t.Fatalf("audit n=%d payload leaked or dropped", n)
		}
		var stored string
		e = tx.QueryRow(t.Context(), `SELECT i.content_sha256 FROM harness_instruction_provenance p JOIN harness_instruction_provenance_items i ON i.provenance_id=p.id AND i.logical_name='AGENTS.md' WHERE p.revision=1`).Scan(&stored)
		if e != nil {
			return e
		}
		if stored != hashA {
			t.Fatalf("stored revision 1 hash %s", stored)
		}
		return nil
	})
	expect(t, f.call(f.agent, "POST", base+"/"+sessionID+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	expect(t, f.call(f.agent, "POST", path, body, lease), 403)
	expect(t, f.call(f.person, "GET", path, nil, ""), 200)
}

func TestArchivedSessionRejectsProvenanceReplayAndNewRevision(t *testing.T) {
	f := fixture(t)
	const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	lease := "archived-provenance-lease-00000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "offline-host",
		"harness_session_ref": "archived-provenance-ref-" + uid(), "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	provenancePath := path + "/provenance"
	report := func(hash string) map[string]any {
		return map[string]any{"items": []any{map[string]any{
			"kind": "agents", "logical_name": "AGENTS.md", "hash_kind": "content",
			"content_sha256": hash, "byte_size": 12,
		}}}
	}
	w = f.call(f.agent, "POST", provenancePath, report(hashA), lease)
	expect(t, w, 200)
	first := decode(t, w)
	if first["revision"] != float64(1) || first["replayed"] != false {
		t.Fatalf("initial provenance: %s", w.Body.String())
	}
	preview := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
	w = f.call(f.person, "POST", path+"/archive", map[string]any{
		"expected_revision": preview["observed_revision"], "confirmation": preview["confirmation"],
		"request_id": uid(), "reason": "Retire offline registration with unknown process state",
	}, "")
	expect(t, w, 200)
	if decode(t, w)["archived_at"] == nil {
		t.Fatal("session was not archived")
	}

	// The old lease is authenticated, then fenced before either the identical
	// receipt lookup or an append. Bad proof must not reveal archive state.
	expect(t, f.call(f.agent, "POST", provenancePath, report(hashA), lease), 410)
	expect(t, f.call(f.agent, "POST", provenancePath, report(hashB), lease), 410)
	expect(t, f.call(f.agent, "POST", provenancePath, report(hashA), "wrong-archived-provenance-lease-0001"), 403)
	hidden := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','no project access')`, hidden.TenantID, hidden.ID)
		return err
	})
	expect(t, f.call(hidden, "GET", provenancePath, nil, ""), 404)
	expect(t, f.call(f.foreign, "GET", provenancePath, nil, ""), 404)
	w = f.call(f.person, "GET", provenancePath, nil, "")
	expect(t, w, 200)
	revisions := decode(t, w)["revisions"].([]any)
	if len(revisions) != 1 || revisions[0].(map[string]any)["id"] != first["id"] ||
		revisions[0].(map[string]any)["items"].([]any)[0].(map[string]any)["content_sha256"] != hashA {
		t.Fatalf("archived provenance history changed: %s", w.Body.String())
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var revisions, events int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_instruction_provenance WHERE session_id=$1`, id).Scan(&revisions); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.provenance_recorded' AND after->>'session_id'=$1`, id).Scan(&events); err != nil {
			return err
		}
		if revisions != 1 || events != 1 {
			t.Fatalf("archived provenance mutated: revisions=%d events=%d", revisions, events)
		}
		return nil
	})
}

func mintKey(t *testing.T, f *harnessFixture, p tenant.Principal, scopes []string) string {
	t.Helper()
	secret := uid()
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(uid(), "-", "")
	f.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'provenance',$3,$4,$5,(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, p.TenantID, p.ID, prefix, hex.EncodeToString(sum[:]), scopes)
		return err
	})
	return "aeon_" + prefix + "_" + secret
}

func callBearer(f *harnessFixture, p tenant.Principal, key, method, path string, body any, lease string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(context.Background(), p))
	r.Header.Set("Authorization", "Bearer "+key)
	if lease != "" {
		r.Header.Set("X-Aeon-Worker-Lease", lease)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
