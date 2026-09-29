// SPDX-License-Identifier: AGPL-3.0-only

package outcomes

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
)

const benefitFields = `{"pill_en":"Clear release notes","pill_de":"Verständliche Release Notes","benefit_en":"Tickets explain the benefit.","benefit_de":"Tickets erklären den Nutzen."}`

func TestOutcomes(t *testing.T) {
	d := dbtest.Open(t)
	t.Run("api", func(t *testing.T) { testOutcomeAPI(t, d) })
	t.Run("capture", func(t *testing.T) { testOutcomeCapture(t, d) })
	t.Run("visibility", func(t *testing.T) { testOutcomeVisibility(t, d) })
}

func testOutcomeAPI(t *testing.T, d *dbtest.DB) {
	t.Helper()
	person := newPerson(t, d, "outcomes-api")
	project := insertNode(t, d, person, "project", "OUT-1", "Outcomes", nil)
	ticket := insertNode(t, d, person, "ticket", "OUT-2", "Record results", &project)
	other := newPerson(t, d, "outcomes-other")

	mod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	writerID, writerToken := agentKey(t, d, person.TenantID, "outcome-writer", []string{"nodes.read", "outcome.read", "outcome.write"})
	_, readerToken := agentKey(t, d, person.TenantID, "outcome-reader", []string{"nodes.read", "outcome.read"})
	_, writeOnlyToken := agentKey(t, d, person.TenantID, "outcome-write-only", []string{"nodes.read", "outcome.write"})
	_, blindToken := agentKey(t, d, person.TenantID, "outcome-blind", []string{"outcome.write"})
	_, noScopeToken := agentKey(t, d, person.TenantID, "outcome-noscope", []string{"nodes.read"})
	_, otherToken := agentKey(t, d, other.TenantID, "outcome-foreign", []string{"nodes.read", "outcome.read", "outcome.write"})

	sessionID := "11111111-1111-4111-8111-111111111111"
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), person), d.App, person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions (
			tenant_id, id, project_id, agent_principal_id, harness, host, management, role, ref_digest, lease_digest
		) VALUES ($1,$2::uuid,$3,$4,'codex','local','unmanaged','worker', decode(md5('ref'),'hex'), decode(md5('lease'),'hex'))`,
			person.TenantID, sessionID, project, writerID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	api := outcomesAPI{t: t, auth: mod, mod: New(d.App)}
	body := `{"kind":"review_verdict","ticket":"OUT-2","session_id":"` + sessionID + `","rules_version":"rules-1","payload":{"verdict":"pass","reviewer_model":"codex","route":"backend","round":1,"findings":0,"summary":"Clean"}}`
	first := api.call(writerToken, http.MethodPost, "/api/outcomes", body, "review-out-2")
	if first.Code != http.StatusCreated || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	created := decodeOutcome(t, first.Body.Bytes())
	if created.TicketKey != "OUT-2" || created.Kind != "review_verdict" || created.Source != "recorded" {
		t.Fatalf("created: %+v", created)
	}
	replayBody := strings.Replace(body, `"ticket":"OUT-2"`, `"ticket":"`+ticket+`"`, 1)
	replay := api.call(writerToken, http.MethodPost, "/api/outcomes", replayBody, "review-out-2")
	if replay.Code != http.StatusOK || decodeOutcome(t, replay.Body.Bytes()).ID != created.ID {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	conflict := api.call(writerToken, http.MethodPost, "/api/outcomes", strings.Replace(body, "Clean", "Different", 1), "review-out-2")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "already used") {
		t.Fatalf("conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	if n := countOutcomes(t, d, person, ticket, "review_verdict"); n != 1 {
		t.Fatalf("rows %d", n)
	}
	for _, tc := range []struct {
		token, body, key string
		want             int
	}{
		{writerToken, `{"kind":"ticket_done","ticket":"OUT-2","payload":{}}`, "manual-done-01", http.StatusBadRequest},
		{writerToken, body, "auto:review-out-2", http.StatusBadRequest},
		{noScopeToken, body, "review-denied1", http.StatusForbidden},
		{readerToken, body, "review-denied2", http.StatusForbidden},
		{blindToken, body, "review-hidden1", http.StatusNotFound},
	} {
		got := api.call(tc.token, http.MethodPost, "/api/outcomes", tc.body, tc.key)
		if got.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.key, got.Code, got.Body.String())
		}
	}
	missingSession := strings.Replace(body, sessionID, "22222222-2222-4222-8222-222222222222", 1)
	if got := api.call(writerToken, http.MethodPost, "/api/outcomes", missingSession, "review-missing"); got.Code != http.StatusBadRequest {
		t.Fatalf("missing session: %d %s", got.Code, got.Body.String())
	}

	listed := api.call(readerToken, http.MethodGet, "/api/outcomes?session_id="+sessionID, "", "")
	if listed.Code != http.StatusOK || listed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list session: %d %s", listed.Code, listed.Body.String())
	}
	page := decodePage(t, listed.Body.Bytes())
	if len(page) != 1 || page[0].ID != created.ID || page[0].RulesVersion == nil || *page[0].RulesVersion != "rules-1" {
		t.Fatalf("session page: %+v", page)
	}
	byRules := api.call(readerToken, http.MethodGet, "/api/outcomes?rules_version=rules-1", "", "")
	if rulesPage := decodePage(t, byRules.Body.Bytes()); byRules.Code != http.StatusOK || len(rulesPage) != 1 {
		t.Fatalf("rules: %d %+v", byRules.Code, rulesPage)
	}
	if empty := api.call(readerToken, http.MethodGet, "/api/outcomes?rules_version=rules-2", "", ""); empty.Code != http.StatusOK || len(decodePage(t, empty.Body.Bytes())) != 0 {
		t.Fatalf("other rules: %d %s", empty.Code, empty.Body.String())
	}
	if got := api.call(readerToken, http.MethodGet, "/api/outcomes", "", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("unfiltered: %d %s", got.Code, got.Body.String())
	}
	if got := api.call(writeOnlyToken, http.MethodGet, "/api/outcomes?ticket_node_id=OUT-2", "", ""); got.Code != http.StatusForbidden {
		t.Fatalf("write-only read: %d %s", got.Code, got.Body.String())
	}
	if got := api.call(otherToken, http.MethodGet, "/api/outcomes?ticket_node_id="+ticket, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("other tenant: %d %s", got.Code, got.Body.String())
	}
	var visible int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM outcome_events`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("unscoped app read: %d %v", visible, err)
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE outcome_events SET source='automatic' WHERE id=$1`, created.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update: %v", err)
	}
	if _, err := d.Admin.Exec(t.Context(), `DELETE FROM outcome_events WHERE id=$1`, created.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("delete: %v", err)
	}
}

func testOutcomeCapture(t *testing.T, d *dbtest.DB) {
	t.Helper()
	person := newPerson(t, d, "outcomes-capture")
	project := insertNode(t, d, person, "project", "CAP-1", "Capture", nil)
	ticket := insertNode(t, d, person, "ticket", "CAP-2", "Finish the work", &project)
	if n := countOutcomes(t, d, person, ticket, "ticket_done"); n != 0 {
		t.Fatalf("open ticket recorded %d", n)
	}
	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		return callAs(t, nodes.New(d.App, nil), person, http.MethodPatch, "/api/nodes/"+ticket, body)
	}
	if got := patch(`{"state":"done","fields":` + benefitFields + `}`); got.Code != http.StatusOK {
		t.Fatalf("done: %d %s", got.Code, got.Body.String())
	}
	from, to := outcomeStates(t, d, person, ticket)
	if len(from) != 1 || from[0] != "open" || to[0] != "done" {
		t.Fatalf("first completion: %v %v", from, to)
	}
	if got := patch(`{"title":"Finish the work today"}`); got.Code != http.StatusOK {
		t.Fatalf("title: %d %s", got.Code, got.Body.String())
	}
	if got := patch(`{"state":"accepted"}`); got.Code != http.StatusOK {
		t.Fatalf("accepted: %d %s", got.Code, got.Body.String())
	}
	if n := countOutcomes(t, d, person, ticket, "ticket_done"); n != 1 {
		t.Fatalf("completed edits recorded %d", n)
	}
	if got := patch(`{"state":"open"}`); got.Code != http.StatusOK {
		t.Fatalf("reopen: %d %s", got.Code, got.Body.String())
	}
	if got := patch(`{"state":"done","fields":` + benefitFields + `}`); got.Code != http.StatusOK {
		t.Fatalf("second done: %d %s", got.Code, got.Body.String())
	}
	if n := countOutcomes(t, d, person, ticket, "ticket_done"); n != 2 {
		t.Fatalf("recompletion recorded %d", n)
	}
	var leaked int
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), person), d.App, person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type IN ('ticket_done','release_included')`).Scan(&leaked)
	}); err != nil || leaked != 0 {
		t.Fatalf("core events %d %v", leaked, err)
	}

	release, second := insertNode(t, d, person, "release", "CAP-3", "September release", &project), insertNode(t, d, person, "release", "CAP-4", "October release", &project)
	inTenant(t, d, person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, person.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,1),($1,$4,$3,2)`, person.TenantID, release, project, second); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,walker_position,source) VALUES($1,$2,$3,0,'manual')`, person.TenantID, ticket, project)
		return err
	})
	if n := countOutcomes(t, d, person, ticket, "release_included"); n != 0 {
		t.Fatalf("backlog recorded %d", n)
	}
	inTenant(t, d, person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET release_node_id=$1 WHERE ticket_node_id=$2`, release, ticket)
		return err
	})
	inTenant(t, d, person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET walker_position=1 WHERE ticket_node_id=$1`, ticket)
		return err
	})
	if n := countOutcomes(t, d, person, ticket, "release_included"); n != 1 {
		t.Fatalf("inclusion recorded %d", n)
	}
	inTenant(t, d, person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET release_node_id=$1 WHERE ticket_node_id=$2`, second, ticket)
		return err
	})
	if n := countOutcomes(t, d, person, ticket, "release_included"); n != 2 {
		t.Fatalf("move recorded %d", n)
	}
	listed := callAs(t, New(d.App), person, http.MethodGet, "/api/outcomes?ticket_node_id=CAP-2", "")
	page := decodePage(t, listed.Body.Bytes())
	titles := map[string]bool{}
	for _, item := range page {
		if item.Kind == "release_included" && item.ReleaseTitle != nil {
			titles[*item.ReleaseTitle] = true
		}
	}
	if listed.Code != http.StatusOK || !titles["September release"] || !titles["October release"] {
		t.Fatalf("release titles: %d %+v", listed.Code, page)
	}
}

func TestOutcomeReadIsPerProject(t *testing.T) {
	d := dbtest.Open(t)
	owner := newPerson(t, d, "outcome-project-read")
	projectA := insertNode(t, d, owner, "project", "OPA-1", "Outcome grant", nil)
	projectB := insertNode(t, d, owner, "project", "OPB-1", "Nodes only", nil)
	ticketA := insertNode(t, d, owner, "ticket", "OPA-2", "Readable", &projectA)
	ticketB := insertNode(t, d, owner, "ticket", "OPB-2", "Hidden outcome", &projectB)
	person := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person, Name: "Restricted"}
	inTenant(t, d, owner, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Restricted') RETURNING id::text`, owner.TenantID).Scan(&person.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
			SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, owner.TenantID, person.ID, projectA); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'nodes_only','Nodes only') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read')`, owner.TenantID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, owner.TenantID, person.ID, role, projectB); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO outcome_events(
			tenant_id, kind, project_id, ticket_node_id, rules_version, idempotency_key,
			actor_principal_id, source, payload, request_digest
		) VALUES
			($1,'ci_result',$2,$3,'rv-leak','leak-a',$6,'recorded','{"result":"pass"}'::jsonb, decode(md5('leak-a'),'hex')),
			($1,'ci_result',$4,$5,'rv-leak','leak-b',$6,'recorded','{"result":"fail"}'::jsonb, decode(md5('leak-b'),'hex'))`,
			owner.TenantID, projectA, ticketA, projectB, ticketB, owner.ID)
		return err
	})
	mod := New(d.App)
	listed := callAs(t, mod, person, http.MethodGet, "/api/outcomes?rules_version=rv-leak", "")
	page := decodePage(t, listed.Body.Bytes())
	if listed.Code != http.StatusOK || len(page) != 1 || page[0].TicketKey != "OPA-2" {
		t.Fatalf("shared rules filter: %d %+v %s", listed.Code, page, listed.Body.String())
	}
	hidden := callAs(t, mod, person, http.MethodGet, "/api/outcomes?ticket_node_id="+ticketB, "")
	if hiddenPage := decodePage(t, hidden.Body.Bytes()); hidden.Code == http.StatusOK && len(hiddenPage) > 0 {
		t.Fatalf("outcome.read in A leaked B (status=%d count=%d)", hidden.Code, len(hiddenPage))
	}
	ownerPage := decodePage(t, callAs(t, mod, owner, http.MethodGet, "/api/outcomes?rules_version=rv-leak", "").Body.Bytes())
	if len(ownerPage) != 2 {
		t.Fatalf("workspace reader saw %+v", ownerPage)
	}
}

func testOutcomeVisibility(t *testing.T, d *dbtest.DB) {
	t.Helper()
	person := newPerson(t, d, "outcomes-vis")
	projectA := insertNode(t, d, person, "project", "VIS-1", "Visible", nil)
	projectB := insertNode(t, d, person, "project", "VIS-2", "Hidden", nil)
	ticketA := insertNode(t, d, person, "ticket", "VIS-3", "Seen", &projectA)
	ticketB := insertNode(t, d, person, "ticket", "VIS-4", "Unseen", &projectB)
	var guest tenant.Principal
	guest.Kind = tenant.Person
	guest.TenantID = person.TenantID
	guest.Name = "Guest"
	inTenant(t, d, person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, person.TenantID).Scan(&guest.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
			SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, person.TenantID, guest.ID, projectA); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO outcome_events (
			tenant_id, kind, project_id, ticket_node_id, rules_version, idempotency_key,
			actor_principal_id, source, payload, request_digest
		) VALUES
			($1,'review_verdict',$2,$3,'rv-1','vis-seen-a',$6,'recorded','{"verdict":"pass"}'::jsonb, decode(md5('a'),'hex')),
			($1,'review_verdict',$4,$5,'rv-1','vis-hidden-b',$6,'recorded','{"verdict":"fail"}'::jsonb, decode(md5('b'),'hex'))`,
			person.TenantID, projectA, ticketA, projectB, ticketB, person.ID)
		return err
	})
	listed := callAs(t, New(d.App), guest, http.MethodGet, "/api/outcomes?rules_version=rv-1", "")
	page := decodePage(t, listed.Body.Bytes())
	if listed.Code != http.StatusOK || len(page) != 1 || page[0].TicketKey != "VIS-3" {
		t.Fatalf("guest list: %d %+v %s", listed.Code, page, listed.Body.String())
	}
	var adminCount int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM outcome_events WHERE idempotency_key IN ('vis-seen-a','vis-hidden-b')`).Scan(&adminCount); err != nil || adminCount != 2 {
		t.Fatalf("admin count %d %v", adminCount, err)
	}
}

type outcomesAPI struct {
	t    *testing.T
	auth *auth.Module
	mod  *Module
}

func (a outcomesAPI) call(token, method, path, body, idem string) *httptest.ResponseRecorder {
	a.t.Helper()
	mux := http.NewServeMux()
	a.mod.Mount(mux)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	_, req.Pattern = mux.Handler(req)
	rec := httptest.NewRecorder()
	a.auth.Middleware(mux).ServeHTTP(rec, req)
	return rec
}

type mounted interface{ Mount(*http.ServeMux) }

func callAs(t *testing.T, mod mounted, p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mod.Mount(mux)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func newPerson(t *testing.T, d *dbtest.DB, slug string) tenant.Principal {
	t.Helper()
	var tenantID, id string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person',$2,$3) RETURNING id::text`, tenantID, slug, []string{"admin"}).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, tenantID, id)
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Person, Name: slug, Roles: []string{"admin"}}
}

func insertNode(t *testing.T, d *dbtest.DB, p tenant.Principal, kind, key, title string, parent *string) string {
	t.Helper()
	var id string
	inTenant(t, d, p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
			SELECT $1,id,$2,$3,$4 FROM node_kinds WHERE tenant_id=$1 AND slug=$5
			RETURNING nodes.id::text`, p.TenantID, key, title, parent, kind).Scan(&id)
	})
	return id
}

func inTenant(t *testing.T, d *dbtest.DB, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), d.App, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

func agentKey(t *testing.T, d *dbtest.DB, tenantID, name string, scopes []string) (string, string) {
	t.Helper()
	_, id, token, err := auth.OperatorCreateAgentKey(t.Context(), d.App, tenantID, name, "", scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func countOutcomes(t *testing.T, d *dbtest.DB, p tenant.Principal, ticket, kind string) int {
	t.Helper()
	var n int
	inTenant(t, d, p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM outcome_events WHERE ticket_node_id=$1 AND kind=$2`, ticket, kind).Scan(&n)
	})
	return n
}

func outcomeStates(t *testing.T, d *dbtest.DB, p tenant.Principal, ticket string) (from, to []string) {
	t.Helper()
	inTenant(t, d, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT payload->>'from_state', payload->>'to_state' FROM outcome_events
			WHERE ticket_node_id=$1 AND kind='ticket_done' ORDER BY recorded_at, id`, ticket)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				return err
			}
			from = append(from, a)
			to = append(to, b)
		}
		return rows.Err()
	})
	return from, to
}

func decodeOutcome(t *testing.T, raw []byte) outcome {
	t.Helper()
	var item outcome
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return item
}

func decodePage(t *testing.T, raw []byte) []outcome {
	t.Helper()
	var page struct {
		Outcomes []outcome `json:"outcomes"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return page.Outcomes
}
