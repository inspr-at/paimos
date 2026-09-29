// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestSessionFileOmitsDoctrineAndKeepsBudgetsApart(t *testing.T) {
	const identity = "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#secrets"
	bulk := strings.Repeat("D", 20000)
	commit := strings.Repeat("ab", 20)
	cat := doctrine.Catalog{
		Releases: []doctrine.Release{{Repository: "inspr-at/inspr-modules", Ref: "v260922101217.0.0", Commit: commit}},
		Rules: []doctrine.Indexed{
			{Identity: identity, Key: "no-env-dump", Text: "Never print the environment."},
			{Identity: "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#bulk", Key: "bulk", Text: bulk},
		},
	}
	byText := testRule("copied", "Never print the environment.")
	byID := testRule("aliased", "Say this another way.")
	byID.Source.Identity = identity
	kept := testRule("local", "Use the project voice.")
	m, err := MergeDelivered(testContext(), []Snapshot{
		floorSnapshot(),
		testSnapshot("project", Scope{Layer: "project", ProjectID: testProject}, byText, byID, kept),
	}, time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), cat)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Body, "Never print the environment.") || strings.Contains(m.Body, bulk) || strings.Contains(m.Body, "Say this another way.") {
		t.Fatalf("session file repeated doctrine\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "Use the project voice.") || !strings.Contains(m.Body, "Preserve safety.") {
		t.Fatalf("session file dropped an Aeon rule\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "inspr-at/inspr-modules@v260922101217.0.0 "+commit) || !strings.HasPrefix(m.Body, SessionHeader) {
		t.Fatalf("pointer\n%s", m.Body)
	}
	if m.ByteSize != len(m.Body) || m.ByteSize > MaxBytes || m.ByteSize >= len(bulk) {
		t.Fatalf("session budget counted doctrine bytes: %d body %d", m.ByteSize, len(m.Body))
	}
	for _, rule := range m.Rules {
		if rule.Identity == "copied" || rule.Identity == "aliased" {
			t.Fatalf("doctrine copy stayed in the rule list: %s", rule.Identity)
		}
	}
}

func TestPublishBlocksDoctrineDuplicateAndSessionOmitsIt(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-channel','Rules channel') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	admin := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: "owner"}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','owner') RETURNING id::text`, tid).Scan(&admin.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, admin.ID, "admin")
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	creds := t.TempDir()
	handler := (doctrine.Credentials{Dir: creds}).CatalogMiddleware(mux)
	call := func(method, path string, in any, want int) []byte {
		t.Helper()
		body := ""
		if in != nil {
			body = string(jsonBytes(in))
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), admin))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	doc := []byte("# Kernel\n\n## Secrets\n\n<!-- aeon-rule: no-env-dump -->\n- Never print the environment.\n  Why: transcripts keep it.\n")
	const repo = "inspr-at/fixture-doctrine"
	const commit = "1111111111111111111111111111111111111111"
	views := doctrine.Render(repo, commit, false, []doctrine.File{{Path: "docs/AGENTS-KERNEL.md", Content: doc}})
	if len(views) != 1 || len(views[0].Rules) != 1 || views[0].Rules[0].Text != "Never print the environment." {
		t.Fatalf("index %+v", views)
	}
	doctrineID := views[0].Rules[0].Identity

	var layer Layer
	json.Unmarshal(call("POST", "/api/rules/layers", Scope{Layer: "company"}, 200), &layer)
	var set Set
	json.Unmarshal(call("POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": "Safety"}, 200), &set)
	floor := testRule("safety", "Keep the locked company floor.")
	floor.Strength = "locked"
	call("PUT", "/api/rules/sets/"+set.ID+"/draft", draftInput{1, "Safety", []Rule{floor}}, 200)
	call("POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120000.0.0"}, 200)

	var projectID string
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'CH-1','Channel' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&projectID)
	})
	if err != nil {
		t.Fatal(err)
	}
	var projectLayer Layer
	json.Unmarshal(call("POST", "/api/rules/layers", Scope{Layer: "project", ProjectID: projectID}, 200), &projectLayer)
	var projectSet Set
	json.Unmarshal(call("POST", "/api/rules/sets", map[string]any{"layer_id": projectLayer.ID, "name": "Project"}, 200), &projectSet)
	copied := testRule("copied", "Never print the environment.")
	call("PUT", "/api/rules/sets/"+projectSet.ID+"/draft", draftInput{1, "Project", []Rule{copied}}, 200)
	call("POST", "/api/rules/sets/"+projectSet.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120001.0.0"}, 200)

	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id, repository, visibility, ref, commit_sha, paths) VALUES($1,$2,'public','v260922101217.0.0',$3,ARRAY['docs/AGENTS-KERNEL.md']) RETURNING id::text`, tid, repo, commit).Scan(&id); e != nil {
			return e
		}
		if _, e := tx.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id, source_id, commit_sha, path, blob_sha, content) VALUES($1,$2,$3,'docs/AGENTS-KERNEL.md',$4,$5)`, tid, id, commit, doctrine.BlobSHA(doc), doc); e != nil {
			return e
		}
		_, e := tx.Exec(t.Context(), `UPDATE doctrine_sources SET indexed_at=clock_timestamp() WHERE id=$1`, id)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	merged := call("GET", "/api/rules/merged?project_id="+projectID+"&person_id="+admin.ID+"&role=builder&harness=codex", nil, 200)
	if strings.Contains(string(merged), "Never print the environment.") || !strings.Contains(string(merged), "Keep the locked company floor") || !strings.Contains(string(merged), repo+"@v260922101217.0.0 "+commit) {
		t.Fatalf("merged session file\n%s", merged)
	}
	var body struct {
		ByteSize int    `json:"byte_size"`
		Body     string `json:"body"`
	}
	if err = json.Unmarshal(merged, &body); err != nil {
		t.Fatal(err)
	}
	if body.ByteSize != len(body.Body) || body.ByteSize > MaxBytes || strings.Contains(body.Body, string(doc)) {
		t.Fatalf("budget %d body includes doctrine file", body.ByteSize)
	}
	channels := call("GET", "/api/rules/channels", nil, 200)
	if !strings.Contains(string(channels), `"identity":"copied"`) || !strings.Contains(string(channels), doctrineID) || strings.Contains(string(channels), "Never print the environment.") {
		t.Fatalf("channels %s", channels)
	}
	call("PUT", "/api/rules/sets/"+projectSet.ID+"/draft", draftInput{2, "Project", []Rule{copied}}, 200)
	refused := call("POST", "/api/rules/sets/"+projectSet.ID+"/publish", map[string]any{"expected_revision": 3, "version": "260928120002.0.0"}, 409)
	if !strings.Contains(string(refused), `"code":"doctrine_duplicate"`) || !strings.Contains(string(refused), "Propose a change") || !strings.Contains(string(refused), doctrineID) {
		t.Fatalf("publish %s", refused)
	}
	batch := call("POST", "/api/rules/publish", map[string]any{"items": []any{map[string]any{"set_id": projectSet.ID, "expected_revision": 3, "version": "260928120003.0.0"}}}, 409)
	if !strings.Contains(string(batch), "doctrine_duplicate") || !strings.Contains(string(batch), "Propose a change") {
		t.Fatalf("batch %s", batch)
	}
	// A cached source needs the current tenant/repository grant even when its
	// visibility says public. Revocation must close every catalog consumer.
	if _, err := d.Admin.Exec(t.Context(), `UPDATE doctrine_sources SET credential_ref='catalog-read' WHERE tenant_id=$1`, tid); err != nil {
		t.Fatal(err)
	}
	grant := func(tenantID, repository string) {
		t.Helper()
		raw := jsonBytes(map[string]any{"grants": []any{map[string]string{"tenant_id": tenantID, "repository": repository}}})
		if err := os.WriteFile(filepath.Join(creds, "catalog-read.allowlist.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, denied := range []string{"missing", "wrong tenant", "wrong repository", "revoked", "missing credentials directory"} {
		switch denied {
		case "wrong tenant":
			grant(testTenant, repo)
		case "wrong repository":
			grant(tid, "other/repository")
		case "missing credentials directory":
			grant(tid, repo)
			handler = (doctrine.Credentials{Dir: filepath.Join(creds, "missing")}).CatalogMiddleware(mux)
		case "revoked":
			grant(tid, repo)
			if got := call("GET", "/api/rules/channels", nil, 200); !strings.Contains(string(got), doctrineID) {
				t.Fatal("authorized catalog lost index")
			}
			grant(tid, "other/repository")
		}
		for _, request := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/rules/channels", nil},
			{"GET", "/api/rules/merged?project_id=" + projectID + "&person_id=" + admin.ID + "&role=builder&harness=codex", nil},
			{"POST", "/api/rules/sets/" + projectSet.ID + "/publish", map[string]any{"expected_revision": 3, "version": "260928120002.0.0"}},
			{"POST", "/api/rules/sets/" + projectSet.ID + "/restore", map[string]any{"expected_revision": 3, "version": "260928120001.0.0", "new_version": "260928120004.0.0"}},
			{"POST", "/api/rules/publish", map[string]any{"items": []any{map[string]any{"set_id": projectSet.ID, "expected_revision": 3, "version": "260928120003.0.0"}}}},
		} {
			got := call(request.method, request.path, request.body, 503)
			if !strings.Contains(string(got), `"code":"doctrine_unavailable"`) {
				t.Fatalf("%s did not explain unavailable doctrine: %s", denied, got)
			}
			if strings.Contains(string(got), doctrineID) || strings.Contains(string(got), copied.Text) || strings.Contains(string(got), "doctrine_duplicate") {
				t.Fatalf("%s leaked cached matching identity", denied)
			}
		}
	}
	grant(tid, repo)
	handler = mux // A missing server policy context must never grant access.
	call("GET", "/api/rules/channels", nil, 503)
}

func TestDoctrineSelectionPreservesPrecedenceAndFloor(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	floor := floorSnapshot()
	cat := doctrine.Catalog{
		Releases: []doctrine.Release{{Repository: "org/repo", Commit: strings.Repeat("a", 40)}},
		Rules:    []doctrine.Indexed{{Identity: "org/repo/kernel#safety", Text: floor.Rules[0].Text}},
	}
	project := testSnapshot("project", Scope{Layer: "project", ProjectID: testProject}, testRule("safety", "Weaken safety."), testRule("local", "Keep the project rule."))
	m, err := MergeDelivered(testContext(), []Snapshot{project, floor}, now, cat)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Body, "Weaken safety") || strings.Contains(m.Body, floor.Rules[0].Text) || !strings.Contains(m.Body, "Keep the project rule.") || !strings.Contains(m.Floor, "org/repo/kernel#safety at commit "+strings.Repeat("a", 40)) {
		t.Fatalf("lost precedence or doctrine floor: %+v", m)
	}
	if err := ValidateMerged(m, testContext(), now); err != nil {
		t.Fatal(err)
	}
	if stub, err := Stub(m); err != nil || !strings.Contains(stub, m.Floor) || strings.Contains(stub, floor.Rules[0].Text) {
		t.Fatalf("stub %q %v", stub, err)
	}
	cache, err := EncodeCache("https://fixture.test", m, now)
	if err != nil {
		t.Fatal(err)
	}
	if offline, err := Offline(cache, "https://fixture.test", testContext(), m.Floor, now); err != nil || offline.Body != m.Body {
		t.Fatal("doctrine floor cache failed", err)
	}
	// Existing independently retained text pins are never silently weakened.
	if _, err := Offline(cache, "https://fixture.test", testContext(), ruleLine(floor.Rules[0]), now); err == nil {
		t.Fatal("old floor pin silently replaced")
	}
	if _, err := MergeDelivered(testContext(), []Snapshot{project, floor, testSnapshot("ambiguous", floor.Scope, floor.Rules[0])}, now, cat); err == nil {
		t.Fatal("doctrine filtering hid ambiguity")
	}
	// A disabled doctrine copy still suppresses a lower rule. Expiry and
	// selectors keep their original meaning before delivery-channel filtering.
	for _, mode := range []string{"enabled", "disabled", "expired", "other-role"} {
		r := testRule("preference", "Doctrine preference.")
		cat.Rules = append(cat.Rules[:1], doctrine.Indexed{Identity: "org/repo/kernel#preference", Text: r.Text})
		switch mode {
		case "disabled":
			r.Enabled = false
		case "expired":
			at := now.Add(-time.Second)
			r.ExpiresAt = &at
		case "other-role":
			r.Roles = []string{"reviewer"}
		}
		m, err := MergeDelivered(testContext(), []Snapshot{floor, testSnapshot("company", floor.Scope, r), testSnapshot("project", project.Scope, testRule("preference", "Lower preference."))}, now, cat)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(m.Body, "Lower preference.") != (mode == "expired" || mode == "other-role") {
			t.Fatalf("%s changed precedence: %s", mode, m.Body)
		}
	}
}

func TestChannelsOnlyExposeReadableSets(t *testing.T) {
	w := newBatchWorld(t, "channels-permissions")
	admin := w.principal(tenant.Person, "owner", "admin")
	reader := w.principal(tenant.Person, "scoped reader", "viewer")
	var hiddenProject string
	err := db.InTenant(dbtest.Seed(t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'CH-2','Hidden' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, w.tid).Scan(&hiddenProject)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, w.tid, reader.ID, w.project); err != nil {
		t.Fatal(err)
	}
	floor := testRule("floor", "Keep the company floor.")
	floor.Strength = "locked"
	w.publish(admin, w.set(admin, w.layer(admin, Scope{Layer: "company"}), "Floor", floor), "260929010000.0.0")
	visibleLayer := w.layer(admin, Scope{Layer: "project", ProjectID: w.project})
	hiddenLayer := w.layer(admin, Scope{Layer: "project", ProjectID: hiddenProject})
	const text = "Never print the environment."
	visible := w.set(admin, visibleLayer, "Visible", testRule("visible-copy", text))
	hidden := w.set(admin, hiddenLayer, "Hidden", testRule("hidden-copy", text))
	w.publish(admin, visible, "260929010001.0.0")
	w.publish(admin, hidden, "260929010001.0.0")

	// Existing publications predate the doctrine index, so both are copies.
	doc := []byte("# Kernel\n\n## Secrets\n\n<!-- aeon-rule: no-env-dump -->\n- " + text + "\n")
	const repo, commit = "inspr-at/fixture-doctrine", "1111111111111111111111111111111111111111"
	err = db.InTenant(dbtest.Seed(t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id,repository,visibility,ref,commit_sha,paths,indexed_at) VALUES($1,$2,'public','fixture',$3,ARRAY['docs/AGENTS-KERNEL.md'],clock_timestamp()) RETURNING id::text`, w.tid, repo, commit).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id,source_id,commit_sha,path,blob_sha,content) VALUES($1,$2,$3,'docs/AGENTS-KERNEL.md',$4,$5)`, w.tid, id, commit, doctrine.BlobSHA(doc), doc)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var all channelView
	if err := json.Unmarshal(w.call(admin, "GET", "/api/rules/channels", nil, 200), &all); err != nil || len(all.Duplicates) != 2 {
		t.Fatalf("admin channels: %+v, %v", all, err)
	}
	w.call(reader, "GET", "/api/rules/sets?layer_id="+hiddenLayer.ID, nil, 403)
	w.call(reader, "GET", "/api/rules/sets?layer_id="+visibleLayer.ID, nil, 200)
	old := beforeSnapshotLoad
	t.Cleanup(func() { beforeSnapshotLoad = old })
	beforeSnapshotLoad = func(id, _ string) {
		if id == hidden.ID {
			t.Fatal("loaded a snapshot without rules.read in its project")
		}
	}
	var scoped channelView
	if err := json.Unmarshal(w.call(reader, "GET", "/api/rules/channels", nil, 200), &scoped); err != nil {
		t.Fatal(err)
	}
	if len(scoped.Duplicates) != 1 || scoped.Duplicates[0].Identity != "visible-copy" || len(scoped.Releases) != 1 {
		t.Fatalf("scoped channel report: %+v", scoped)
	}
}

type catalogCountingTx struct {
	pgx.Tx
	reads int
}

func (tx *catalogCountingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "FROM doctrine_sources") {
		tx.reads++
	}
	return tx.Tx.Query(ctx, sql, args...)
}

func TestPublicationCatalogIsLoadedOncePerRequest(t *testing.T) {
	w := newBatchWorld(t, "catalog-once")
	err := db.InTenant(dbtest.Seed(t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
		counted := &catalogCountingTx{Tx: tx}
		for request := 1; request <= 2; request++ {
			ctx := withDoctrineCatalog(t.Context())
			for _, text := range []string{"First set.", "Second set."} {
				if err := rejectDoctrineCopy(ctx, counted, []Rule{testRule("local", text)}); err != nil {
					return err
				}
			}
			// The post-publication budget check shares this catalog too.
			if _, err := loadDoctrineCatalog(ctx, counted); err != nil {
				return err
			}
			if counted.reads != request {
				t.Fatalf("%d requests made %d catalog reads", request, counted.reads)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
