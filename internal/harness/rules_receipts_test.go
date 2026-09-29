// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func receiptFixture(t *testing.T) (*harnessFixture, string, string, harness.RulesReceiptWrite) {
	t.Helper()
	f := fixture(t)
	f.agent.KeyCreatorID = f.person.ID
	f.agent.Scopes = []string{"harness.worker", "harness.read", "harness.write"}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$1 WHERE principal_id=$2`, f.person.ID, f.agent.ID)
		return err
	})
	lease := "receipt-generation-lease-" + uid()
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "receipt-test",
		"harness_session_ref": "receipt-generation-" + uid(), "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker", "ticket_node_id": f.ticket, "work_shape": "ship",
	}, "")
	expect(t, w, 201)
	sessionID := decode(t, w)["id"].(string)
	revision, size := int64(0), 72
	in := harness.RulesReceiptWrite{RequestID: uid(), ExpectedRevision: &revision,
		Context:    rules.Context{TenantID: f.agent.TenantID, ProjectID: f.project, PersonID: f.person.ID, AgentID: f.agent.ID, Role: "builder", Harness: "codex", TaskID: f.ticket},
		BodySHA256: strings.Repeat("a", 64), Version: "260928100000.0.0", ByteSize: &size, Source: "online"}
	return f, "/api/projects/" + f.project + "/harness-sessions/" + sessionID, lease, in
}

func fmtItems(rev map[string]any) string {
	raw, _ := json.Marshal(rev["items"])
	return string(raw)
}

func receiptResponse(t *testing.T, w *httptest.ResponseRecorder, replayed bool) harness.RulesReceipt {
	t.Helper()
	expect(t, w, 200)
	var out struct {
		Receipt  harness.RulesReceipt `json:"receipt"`
		Replayed bool                 `json:"replayed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Replayed != replayed || out.Receipt.Evidence != "worker_reported_received" || out.Receipt.PublicationVerified || out.Receipt.LoadVerified || out.Receipt.ExecutionVerified || out.Receipt.AuthorityGranted {
		t.Fatalf("receipt misrepresented: %s", w.Body.String())
	}
	for _, name := range []string{"publication_verified", "load_verified", "execution_verified", "authority_granted"} {
		if !strings.Contains(w.Body.String(), `"`+name+`":false`) {
			t.Fatalf("missing explicit evidence limit %s", name)
		}
	}
	return out.Receipt
}

func TestRulesReceiptAppendCASPreservesInstructionProvenance(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	path := session + "/rules-receipts"
	items := []any{}
	for _, pair := range [][2]string{{"agents", "AGENTS.md"}, {"claude", "CLAUDE.md"}, {"skill", "worker/SKILL.md"}} {
		items = append(items, map[string]any{"kind": pair[0], "logical_name": pair[1], "hash_kind": "content", "content_sha256": strings.Repeat("b", 64), "byte_size": 123})
	}
	expect(t, f.call(f.agent, "POST", session+"/provenance", map[string]any{"items": items}, lease), 200)
	old := f.call(f.person, "GET", session+"/provenance", nil, "").Body.String()
	empty := decode(t, f.call(f.person, "GET", path, nil, ""))
	if len(empty["receipts"].([]any)) != 0 || empty["next_before_revision"] != nil {
		t.Fatal(empty)
	}
	first := receiptResponse(t, f.call(f.agent, "POST", path, in, lease), false)
	if first.Revision != 1 || first.RecordedBy != f.agent.ID || first.RecordedAt.IsZero() || !reflect.DeepEqual(first.Request, in) {
		t.Fatal(first)
	}
	// Registration/heartbeat revisions are independent from receipt CAS.
	sessionBefore := decode(t, f.call(f.person, "GET", session, nil, ""))["revision"]
	replay := receiptResponse(t, f.call(f.agent, "POST", path, in, lease), true)
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("exact replay changed receipt")
	}
	stale := in
	stale.RequestID = uid()
	expect(t, f.call(f.agent, "POST", path, stale, lease), 409)
	divergent := in
	divergent.BodySHA256 = strings.Repeat("c", 64)
	expect(t, f.call(f.agent, "POST", path, divergent, lease), 409)
	rev := int64(1)
	changedRevision := in
	changedRevision.ExpectedRevision = &rev
	expect(t, f.call(f.agent, "POST", path, changedRevision, lease), 409)
	secondIn := divergent
	secondIn.RequestID, secondIn.ExpectedRevision, secondIn.Source = uid(), &rev, "cache"
	second := receiptResponse(t, f.call(f.agent, "POST", path, secondIn, lease), false)
	if second.Revision != 2 || second.ID == first.ID {
		t.Fatal(second)
	}
	if got := receiptResponse(t, f.call(f.agent, "POST", path, in, lease), true); !reflect.DeepEqual(got, first) {
		t.Fatal("historical replay changed receipt")
	}
	provenance := decode(t, f.call(f.person, "GET", session+"/provenance", nil, ""))
	revisions := provenance["revisions"].([]any)
	if len(revisions) != 3 || strings.Contains(old, "merged-rules") {
		t.Fatalf("file provenance was not kept as its own revision: %s", old)
	}
	oldest := revisions[2].(map[string]any)
	newest := revisions[0].(map[string]any)
	if oldest["revision"].(float64) != 1 || !strings.Contains(fmtItems(oldest), "AGENTS.md") || strings.Contains(fmtItems(oldest), "merged-rules") {
		t.Fatal("original instruction files were replaced", oldest)
	}
	if newest["revision"].(float64) != 3 || !strings.Contains(fmtItems(newest), "AGENTS.md") || !strings.Contains(fmtItems(newest), "merged-rules") || !strings.Contains(fmtItems(newest), strings.Repeat("c", 64)) {
		t.Fatal("latest provenance dropped files or the receipt hash", newest)
	}
	rawPage, _ := json.Marshal(provenance)
	if strings.Contains(string(rawPage), lease) {
		t.Fatal("provenance response contains the worker lease")
	}
	if after := decode(t, f.call(f.person, "GET", session, nil, ""))["revision"]; after != sessionBefore {
		t.Fatal("receipt altered session revision")
	}
	page := decode(t, f.call(f.person, "GET", path, nil, ""))
	if len(page["receipts"].([]any)) != 2 || page["receipts"].([]any)[0].(map[string]any)["id"] != second.ID {
		t.Fatal(page)
	}
	page = decode(t, f.call(f.person, "GET", path+"?before_revision=2", nil, ""))
	if len(page["receipts"].([]any)) != 1 || page["receipts"].([]any)[0].(map[string]any)["id"] != first.ID {
		t.Fatal("history cursor", page)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		var payload string
		err := tx.QueryRow(t.Context(), `SELECT count(*),string_agg(after::text,'') FROM events WHERE type='harness.rules_received'`).Scan(&count, &payload)
		if count != 2 || strings.Contains(payload, lease) || strings.Contains(payload, "AGENTS.md") || !strings.Contains(payload, "worker_reported_received") {
			t.Fatalf("audit count/content: %d", count)
		}
		return err
	})
	for _, sql := range []string{`UPDATE harness_rules_receipts SET request='{}' WHERE id=$1`, `DELETE FROM harness_rules_receipts WHERE id=$1`} {
		err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), sql, first.ID)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("receipt mutation was not blocked: %v", err)
		}
	}
}

func TestRulesReceiptConcurrentConflictAndReplay(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	path := session + "/rules-receipts"
	// Hold the actual session lock until BOTH HTTP transactions are waiting on
	// locks in Postgres. This is a real overlapping CAS race, not sequential calls.
	blocker, err := f.db.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM harness_sessions WHERE id=$1 FOR UPDATE`, strings.TrimPrefix(session, "/api/projects/"+f.project+"/harness-sessions/")); err != nil {
		t.Fatal(err)
	}
	other := in
	other.RequestID, other.BodySHA256 = uid(), strings.Repeat("d", 64)
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, request := range []harness.RulesReceiptWrite{in, other} {
		go func() { results <- f.call(f.agent, "POST", path, request, lease) }()
	}
	// pg_stat_activity sees separate connections blocked by the row lock.
	var waiting int
	for range 100 {
		err = f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM harness_sessions%FOR UPDATE%'`).Scan(&waiting)
		if err != nil || waiting == 2 {
			break
		}
		if _, err = f.db.Admin.Exec(t.Context(), `SELECT pg_sleep(0.01)`); err != nil {
			break
		}
	}
	if err != nil || waiting != 2 {
		t.Fatalf("concurrent requests did not both reach the row lock: %d %v", waiting, err)
	}
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var winner harness.RulesReceipt
	conflicts := 0
	for range 2 {
		w := <-results
		if w.Code == 409 {
			conflicts++
		} else {
			winner = receiptResponse(t, w, false)
		}
	}
	if conflicts != 1 || winner.Revision != 1 {
		t.Fatalf("expected one committed receipt and one conflicting write: %d %+v", conflicts, winner)
	}
	// Concurrent identical delivery is replay, not a second append/audit event.
	for range 2 {
		go func() { results <- f.call(f.agent, "POST", path, winner.Request, lease) }()
	}
	for range 2 {
		if got := receiptResponse(t, <-results, true); !reflect.DeepEqual(got, winner) {
			t.Fatal("concurrent replay differs")
		}
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count, audit int
		err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM harness_rules_receipts),(SELECT count(*) FROM events WHERE type='harness.rules_received')`).Scan(&count, &audit)
		if count != 1 || audit != 1 {
			t.Fatalf("CAS/replay duplicated storage or audit: %d %d", count, audit)
		}
		return err
	})
}

func TestRulesReceiptContextCredentialAndGenerationFencing(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	path := session + "/rules-receipts"
	otherProject := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'RR-3',kind_id,'Other receipt project' FROM nodes WHERE id=$3`, f.person.TenantID, otherProject, f.project)
		return err
	})
	first := receiptResponse(t, f.call(f.agent, "POST", path, in, lease), false)
	for _, alter := range []func(*rules.Context){
		func(c *rules.Context) { c.TenantID = f.foreign.TenantID },
		func(c *rules.Context) { c.ProjectID = otherProject },
		func(c *rules.Context) { c.PersonID = f.foreign.ID },
		func(c *rules.Context) { c.AgentID = uid() },
		func(c *rules.Context) { c.TaskID = uid() },
		func(c *rules.Context) { c.Harness = "claude-code" },
	} {
		bad := in
		alter(&bad.Context)
		expect(t, f.call(f.agent, "POST", path, bad, lease), 403)
	}
	expect(t, f.call(f.person, "POST", path, in, lease), 403)
	expect(t, f.call(f.agent, "POST", path, in, "wrong-generation-lease-0000000000001"), 403)
	expect(t, f.call(f.agent, "POST", strings.Replace(path, f.project, otherProject, 1), in, lease), 403)
	expect(t, f.call(f.person, "GET", strings.Replace(path, f.project, otherProject, 1), nil, ""), 404)
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_rules_receipts`).Scan(&count)
		if count != 0 {
			t.Fatal("receipt table leaked across tenant RLS")
		}
		return err
	})
	foreignAgent := tenant.Principal{ID: uid(), TenantID: f.foreign.TenantID, Kind: tenant.Agent, KeyCreatorID: f.foreign.ID}
	otherAgent := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Agent, KeyCreatorID: f.person.ID}
	for _, agent := range []tenant.Principal{foreignAgent, otherAgent} {
		f.tx(t, agent, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','other receipt worker')`, agent.TenantID, agent.ID)
			return err
		})
		dbtest.BindRole(t, f.db, agent.TenantID, agent.ID, "member")
		key := mintKey(t, f, agent, []string{"harness.worker", "harness.read"})
		expect(t, callBearer(f, agent, key, "POST", path, in, lease), 403)
	}
	unknownOwner := f.agent
	unknownOwner.KeyCreatorID = ""
	expect(t, f.call(unknownOwner, "POST", path, in, lease), 403)
	// Project-only readers can see the distinct table even though harness events
	// are workspace/actor restricted. Unbound people cannot see its history.
	reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','receipt reader')`, reader.TenantID, reader.ID)
		return err
	})
	expect(t, f.call(reader, "GET", path, nil, ""), 404)
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, reader.TenantID, reader.ID, f.project); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(reader, "GET", path, nil, ""), 200)
	readKey := mintKey(t, f, f.agent, []string{"harness.read"})
	expect(t, callBearer(f, f.agent, readKey, "POST", path, in, lease), 403)
	// Expired and revoked keys fail before replay; no age-only inference is used
	// for harness leases (the existing generation protocol has no lease TTL).
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	expect(t, f.call(f.agent, "POST", path, in, lease), 403)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET expires_at=NULL,revoked_at=clock_timestamp() WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	expect(t, f.call(f.agent, "POST", path, in, lease), 403)
	// Issue a new test key to the same principal: receipt idempotency is tied to
	// its report/generation, not to a particular API credential's bytes.
	f.key = mintKey(t, f, f.agent, []string{"harness.worker", "harness.read"})
	receiptResponse(t, f.call(f.agent, "POST", path, in, lease), true)
	expect(t, f.call(f.agent, "POST", session+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	expect(t, f.call(f.agent, "POST", path, in, lease), 403)
	newReport := in
	revision := int64(1)
	newReport.RequestID, newReport.ExpectedRevision = uid(), &revision
	expect(t, f.call(f.agent, "POST", path, newReport, lease), 403)
	preview := decode(t, f.call(f.person, "GET", session+"/recovery", nil, ""))
	expect(t, f.call(f.person, "POST", session+"/archive", map[string]any{"expected_revision": preview["observed_revision"], "confirmation": preview["confirmation"], "request_id": uid(), "reason": "receipt test"}, ""), 200)
	expect(t, f.call(f.agent, "POST", path, in, lease), 410)
	expect(t, f.call(f.agent, "POST", path, newReport, lease), 410)
	expect(t, f.call(f.agent, "POST", path, in, "wrong-generation-lease-0000000000001"), 403)
	page := decode(t, f.call(reader, "GET", path, nil, ""))
	if len(page["receipts"].([]any)) != 1 || page["receipts"].([]any)[0].(map[string]any)["id"] != first.ID {
		t.Fatal("failed writes mutated history", page)
	}
	// A replacement generation has a distinct proof. Knowing the retired
	// generation's lease never authenticates a request to its successor.
	newLease := "successor-receipt-generation-" + uid()
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "receipt-test",
		"harness_session_ref": "successor-receipt-" + uid(), "worker_lease": newLease,
		"management_mode": "unmanaged", "role": "worker", "ticket_node_id": f.ticket, "work_shape": "ship",
	}, "")
	expect(t, w, 201)
	newPath := "/api/projects/" + f.project + "/harness-sessions/" + decode(t, w)["id"].(string) + "/rules-receipts"
	expect(t, f.call(f.agent, "POST", newPath, in, lease), 403)
	nextGeneration := receiptResponse(t, f.call(f.agent, "POST", newPath, in, newLease), false)
	if nextGeneration.Revision != 1 || nextGeneration.ID == first.ID {
		t.Fatal("receipt request identity escaped its generation")
	}
}

// Receipts accept the upgraded 64,000-byte ceiling and reject one byte more.
func TestRulesReceiptByteSizeBoundary(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	path := session + "/rules-receipts"
	size := rules.MaxBytes
	in.ByteSize = &size
	receiptResponse(t, f.call(f.agent, "POST", path, in, lease), false)
	rev, over := int64(1), rules.MaxBytes+1
	next := in
	next.RequestID, next.ExpectedRevision, next.ByteSize = uid(), &rev, &over
	expect(t, f.call(f.agent, "POST", path, next, lease), 400)
	if rules.MaxBytes != 64000 {
		t.Fatalf("unexpected upgraded session file ceiling %d", rules.MaxBytes)
	}
}

func TestRulesReceiptRejectsUnboundedOrAuthorityClaims(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	path := session + "/rules-receipts"
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { delete(m, "expected_revision") },
		func(m map[string]any) { m["expected_revision"] = nil },
		func(m map[string]any) { m["expected_revision"] = -1 },
		func(m map[string]any) { m["body"] = "instruction text must never be stored" },
		func(m map[string]any) { m["version"] = "/Users/worker/private" },
		func(m map[string]any) { m["version"] = "260230100000.0.0" },
		func(m map[string]any) { m["body_sha256"] = "wrong" },
		func(m map[string]any) { m["body_sha256"] = strings.Repeat("a", 9000) },
		func(m map[string]any) { m["byte_size"] = rules.MaxBytes + 1 },
		func(m map[string]any) { m["byte_size"] = 0 },
		func(m map[string]any) { delete(m, "byte_size") },
		func(m map[string]any) { m["source"] = "published" },
		func(m map[string]any) { m["source"] = "floor-only" },
		func(m map[string]any) { m["execution_verified"] = true },
		func(m map[string]any) { m["load_verified"] = true },
		func(m map[string]any) { m["authority_granted"] = true },
		func(m map[string]any) { m["context"].(map[string]any)["credentials"] = "synthetic disallowed field" },
		func(m map[string]any) { m["context"].(map[string]any)["role"] = "admin" },
	} {
		raw, _ := json.Marshal(in)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mutate(body)
		expect(t, f.call(f.agent, "POST", path, body, lease), 400)
	}
	for _, query := range []string{"?before_revision=0", "?before_revision=abc", "?before_revision=1&before_revision=2", "?unexpected=1"} {
		expect(t, f.call(f.person, "GET", path+query, nil, ""), 400)
	}
	in.Version, in.Source = "floor-only", "floor-only"
	got := receiptResponse(t, f.call(f.agent, "POST", path, in, lease), false)
	if got.Revision != 1 || got.Request.Source != "floor-only" {
		t.Fatal("fallback mislabeled or invalid inputs stored", got)
	}
}

func TestRulesReceiptProductionAuthAndNoAuthorityChange(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	// Exercise actual key authentication/creator derivation and the registry
	// permission boundary, not only direct handler calls with a test principal.
	parts := strings.Split(f.key, "_")
	prefix := strings.ReplaceAll(f.agent.TenantID, "-", "") + "0123456789abcdef"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET prefix=$1 WHERE principal_id=$2`, prefix, f.agent.ID)
		return err
	})
	f.key = "aeon_" + prefix + "_" + parts[2]
	mod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	rules.New(f.db.App).Mount(f.mux)
	server := &httpapi.Server{Mux: f.mux, Pool: f.db.App, Middleware: []func(http.Handler) http.Handler{mod.Middleware}}
	handler := server.Handler()
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+f.key)
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := session + "/rules-receipts"
	noPublish := "/api/rules/sets/" + uid() + "/publish"
	expect(t, call("POST", noPublish, map[string]any{}), 403)
	receiptResponse(t, call("POST", path, in), false)
	expect(t, call("POST", noPublish, map[string]any{}), 403)
	expect(t, call("GET", path, nil), 200)
	expect(t, call("HEAD", path, nil), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var published int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE rule_resource='version'`).Scan(&published); err != nil {
			return err
		}
		if published != 0 {
			t.Fatal("receipt published rules")
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.read'] WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	expect(t, call("POST", path, in), 403)
	expect(t, call("GET", path, nil), 200)
	// Same agent with an API key created by another authorized person cannot
	// replay the former owner's context, even with the correct generation lease.
	otherOwner := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','other key owner')`, f.person.TenantID, otherOwner)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.read','harness.worker'],created_by_principal_id=$1 WHERE principal_id=$2`, otherOwner, f.agent.ID)
		return err
	})
	dbtest.BindRole(t, f.db, f.person.TenantID, otherOwner, "admin")
	expect(t, call("POST", path, in), 403)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	expect(t, call("POST", path, in), 401)
}

func TestRulesReceiptBoundedHistoryAndOldReplay(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	path := session + "/rules-receipts"
	first := receiptResponse(t, f.call(f.agent, "POST", path, in, lease), false)
	for revision := int64(1); revision < 33; revision++ {
		next := in
		next.RequestID, next.ExpectedRevision = uid(), &revision
		receiptResponse(t, f.call(f.agent, "POST", path, next, lease), false)
	}
	page := decode(t, f.call(f.person, "GET", path, nil, ""))
	items := page["receipts"].([]any)
	if len(items) != 32 || page["next_before_revision"] != float64(2) || items[0].(map[string]any)["revision"] != float64(33) {
		t.Fatal("unbounded/incorrect history", page)
	}
	page = decode(t, f.call(f.person, "GET", path+"?before_revision=2", nil, ""))
	if len(page["receipts"].([]any)) != 1 || page["next_before_revision"] != nil {
		t.Fatal("old history unavailable", page)
	}
	if got := receiptResponse(t, f.call(f.agent, "POST", path, in, lease), true); !reflect.DeepEqual(got, first) {
		t.Fatal("pagination window destroyed idempotency")
	}
}
