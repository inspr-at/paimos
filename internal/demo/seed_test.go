// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/deliveryvote"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/inspr-at/paimos/internal/ticketwork"
	"github.com/inspr-at/paimos/internal/workorders"
)

func TestDemoSeedRefusesOutsideDev(t *testing.T) {
	for _, environment := range []string{"prod", "", "staging", "DEV", "dev "} {
		t.Run("environment="+environment, func(t *testing.T) {
			t.Setenv("AEON_ENV", environment)
			const want = "aeon demo seed is allowed only when AEON_ENV=dev"
			if _, err := Seed(t.Context(), nil, "lumen-demo"); err == nil || err.Error() != want {
				t.Fatalf("Seed did not guard before the database: %v", err)
			}
			if err := Run(t.Context(), nil, []string{"seed", "--tenant", "lumen-demo"}, nil); err == nil || err.Error() != want {
				t.Fatalf("Run did not guard before the database: %v", err)
			}
		})
	}
}

func TestDemoSeedTwice(t *testing.T) {
	t.Setenv("AEON_ENV", "dev")
	database := dbtest.Open(t)
	ctx := t.Context()
	id, err := tenantbootstrap.Create(ctx, database.App, "lumen-demo", "Lumen Demo")
	if err != nil {
		t.Fatal(err)
	}
	before := seedRows(t, database, id)
	first, err := Seed(ctx, database.App, "lumen-demo")
	if err != nil {
		t.Fatal(err)
	}
	if first.Already || first.Projects != 3 || first.Tickets < 40 || first.Stage != "build" {
		t.Fatalf("summary %+v", first)
	}
	if legacy := scalar(t, database, id, `SELECT count(*) FROM node_kinds WHERE slug IN ('epic','ticket','task')`); legacy != 0 {
		t.Fatalf("demo recreated %d retired work kinds", legacy)
	}
	parents := scalar(t, database, id, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	 WHERE k.slug='work' AND n.deleted_at IS NULL AND EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id
	 WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug='work')`)
	leaves := scalar(t, database, id, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	 WHERE k.slug='work' AND n.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id
	 WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug='work')`)
	if parents < 8 || leaves != first.Tickets {
		t.Fatalf("demo work hierarchy: %d parents, %d leaves, summary %+v", parents, leaves, first)
	}
	after := seedRows(t, database, first.TenantID)
	if slices.Equal(before, after) {
		t.Fatal("seed wrote no resources")
	}
	events := scalar(t, database, first.TenantID, `SELECT count(*) FROM events`)
	tickets := scalar(t, database, first.TenantID, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='work' AND n.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug='work')`)
	kinds := scalar(t, database, first.TenantID, `SELECT count(DISTINCT k.slug) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug IN ('runbook','guideline','memory','external_system','related_project')`)
	knowledge := scalar(t, database, first.TenantID, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug IN ('runbook','guideline','memory','external_system','related_project')`)
	agents := scalar(t, database, first.TenantID, `SELECT count(*) FROM principals WHERE kind='agent' AND name IN ('Lumen Scribe','Harbor Clerk','Glass Scout')`)
	sessions := scalar(t, database, first.TenantID, `SELECT count(*) FROM harness_sessions`)
	finished := scalar(t, database, first.TenantID, `SELECT count(*) FROM agent_runs WHERE status='completed' AND started_at IS NOT NULL AND ended_at IS NOT NULL`)
	pending := scalar(t, database, first.TenantID, `SELECT count(*) FROM approval_requests a WHERE NOT EXISTS (SELECT 1 FROM approval_decisions d WHERE d.tenant_id=a.tenant_id AND d.request_id=a.id)`)
	hours := scalar(t, database, first.TenantID, `SELECT count(*) FROM time_entries`)
	rates := scalar(t, database, first.TenantID, `SELECT count(*) FROM cost_unit_rates WHERE internal_amount=80.00 AND bill_amount=140.00 AND currency='EUR'`)
	if kinds != 5 || knowledge < 8 || agents != 3 || sessions != 3 || finished != 3 || pending < 1 || hours < 3 || rates != 1 || tickets < 40 {
		t.Fatalf("kinds %d knowledge %d agents %d sessions %d finished %d pending %d hours %d rates %d tickets %d", kinds, knowledge, agents, sessions, finished, pending, hours, rates, tickets)
	}
	assertCaptureState(t, database, first.TenantID)
	assertShowcaseState(t, database, first.TenantID)
	second, err := Seed(ctx, database.App, "lumen-demo")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already || second.Projects != first.Projects || second.Tickets != first.Tickets || second.Stage != "build" {
		t.Fatalf("second summary %+v", second)
	}
	if again := scalar(t, database, first.TenantID, `SELECT count(*) FROM events`); again != events {
		t.Fatalf("second seed wrote events: %d to %d", events, again)
	}
	if again := scalar(t, database, first.TenantID, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='work' AND n.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug='work')`); again != tickets {
		t.Fatalf("tickets changed %d to %d", tickets, again)
	}
	if replay := seedRows(t, database, first.TenantID); !slices.Equal(after, replay) {
		t.Fatal("replay changed seeded nodes, keys, bindings, principals, or events")
	}
}

func TestDemoInterruptedRunRollsBackAndRetryConverges(t *testing.T) {
	for _, step := range []string{"agents", "work"} {
		t.Run(step, func(t *testing.T) { interruptedSeed(t, step) })
	}
}

func interruptedSeed(t *testing.T, interruptAt string) {
	t.Setenv("AEON_ENV", "dev")
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID, err := tenantbootstrap.Create(ctx, database.App, "retry-demo", "Retry Demo")
	if err != nil {
		t.Fatal(err)
	}
	baseline := seedRows(t, database, tenantID)
	_, err = seedWithHook(ctx, database.App, "retry-demo", func(step string) error {
		if step == interruptAt {
			return errors.New("injected interruption")
		}
		return nil
	})
	if err == nil || err.Error() != "injected interruption" {
		t.Fatalf("expected injected interruption, got %v", err)
	}
	if partial := seedRows(t, database, tenantID); !slices.Equal(baseline, partial) {
		t.Fatal("interruption left seeded nodes, keys, bindings, principals, or events")
	}
	first, err := Seed(ctx, database.App, "retry-demo")
	if err != nil || first.Already {
		t.Fatalf("retry: summary %+v, error %v", first, err)
	}
	complete := seedRows(t, database, tenantID)
	second, err := Seed(ctx, database.App, "retry-demo")
	if err != nil || !second.Already {
		t.Fatalf("replay: summary %+v, error %v", second, err)
	}
	if replay := seedRows(t, database, tenantID); !slices.Equal(complete, replay) {
		t.Fatal("retry replay changed seeded resources")
	}
}

func TestDemoJourneyScopesOnlyExtendNewKey(t *testing.T) {
	t.Setenv("AEON_ENV", "dev")
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID, err := tenantbootstrap.Create(ctx, database.App, "keys-demo", "Keys Demo")
	if err != nil {
		t.Fatal(err)
	}
	oldKey, principalID, _, err := auth.OperatorCreateAgentKey(ctx, database.App, tenantID, "Lumen Scribe", "", []string{"nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(ctx, database.App, "keys-demo"); err != nil {
		t.Fatal(err)
	}
	var oldScopes, newScopes []string
	var newKey, eventKey string
	err = db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT scopes FROM agent_keys WHERE id=$1::uuid`, oldKey).Scan(&oldScopes); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text,scopes FROM agent_keys WHERE principal_id=$1::uuid AND id<>$2::uuid`, principalID, oldKey).Scan(&newKey, &newScopes); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT after->>'key_id' FROM events WHERE type='agent_key.scopes_extended'`).Scan(&eventKey)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(oldScopes, []string{"nodes.read"}) || !slices.Contains(newScopes, "journey.requirements") || !slices.Contains(newScopes, "journey.build") || eventKey != newKey {
		t.Fatalf("old scopes %v, new scopes %v, new key %s, event key %s", oldScopes, newScopes, newKey, eventKey)
	}
}

// seedRows compares row contents, including IDs, keys, scopes, and event
// snapshots. Counts alone would miss changed or duplicated resources.
func seedRows(t *testing.T, database *dbtest.DB, tenantID string) []string {
	t.Helper()
	queries := []string{
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb)::text FROM identities t WHERE t.issuer='https://demo.aeon.invalid'`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb)::text FROM nodes t`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb)::text FROM agent_keys t`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb)::text FROM role_bindings t`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb)::text FROM roles t`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.role_id,t.permission),'[]'::jsonb)::text FROM role_permissions t`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb)::text FROM principals t`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb)::text FROM events t`,
	}
	// Compare every seed-owned ledger too: a no-op replay must not refresh
	// probes, allowance windows, approvals, journeys or historical evidence.
	for _, table := range []string{
		"node_relations", "agent_accounts", "account_allowance_windows", "account_reservations",
		"model_profiles", "model_role_routes", "model_security_role_routes", "approval_requests", "approval_decisions", "agent_runs", "run_telemetry",
		"work_orders", "work_criteria", "work_evidence", "agent_delivery_votes",
		"harness_sessions", "harness_instruction_provenance", "harness_instruction_provenance_items",
		"journey_projects", "journey_releases", "journey_requirements", "journey_features", "journey_tickets", "journey_gates", "journey_action_receipts",
		"intake_sources", "intake_drafts", "intake_citations", "intake_draft_acceptances",
		"time_entries", "cost_unit_rates",
	} {
		queries = append(queries, `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t`)
	}
	rows := make([]string, len(queries))
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, tenantID, func(tx pgx.Tx) error {
		for i, query := range queries {
			if err := tx.QueryRow(t.Context(), query).Scan(&rows[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertShowcaseState(t *testing.T, database *dbtest.DB, tenantID string) {
	t.Helper()
	admin := tenant.Principal{TenantID: tenantID, Kind: tenant.Person}
	var showcase, harbor, project, approvalID string
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT id::text,name FROM principals WHERE name='Demo Operator'`).Scan(&admin.ID, &admin.Name); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text,project_id::text FROM nodes WHERE key='LT-1'`).Scan(&showcase, &project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM nodes WHERE key='HT-1'`).Scan(&harbor); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT a.id::text FROM approval_requests a JOIN approval_decisions d ON d.tenant_id=a.tenant_id AND d.request_id=a.id JOIN principals p ON p.tenant_id=a.tenant_id AND p.id=a.agent_principal_id JOIN principals decider ON decider.tenant_id=d.tenant_id AND decider.id=d.decided_by_principal_id WHERE a.resource_id=$1::uuid AND a.scope='nodes.write' AND p.name='Lumen Scribe' AND d.decision='approved' AND decider.name='Demo Operator'`, showcase).Scan(&approvalID)
	})
	if err != nil {
		t.Fatal(err)
	}
	api, err := newAPI(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	ticketwork.New(database.App).Mount(api.mux)
	var page activity.Page
	if err := api.do(admin, "", http.MethodGet, "/api/nodes/"+showcase+"/activity", nil, http.StatusOK, &page, nil); err != nil {
		t.Fatal(err)
	}
	// Activity is newest first. Prove the authored progress, request and person
	// decision are interleaved in the chronology returned to the drawer.
	want := []struct{ author, text string }{
		{"Ivo Quill", "the brief stays in the demo tenant"},
		{"Lumen Scribe", "Fictional progress note:"},
		{"Nia Frost", "Fictional human review:"},
		{"Lumen Scribe", "Fictional approval request:"},
		{"Demo Operator", "Fictional approval decision:"},
		{"Lumen Scribe", "The fictional label review is complete."},
		{"Nia Frost", "The fictional label reads clearly."},
	}
	next, agentEntries := 0, 0
	for i := len(page.Items) - 1; i >= 0; i-- {
		item := page.Items[i]
		if item.Type != "comment" || item.BodyMarkdown == nil {
			continue
		}
		if item.Author.Name == "Lumen Scribe" {
			agentEntries++
		}
		if next < len(want) && item.Author.Name == want[next].author && strings.Contains(*item.BodyMarkdown, want[next].text) {
			if (next == 3 || next == 4) && !strings.Contains(*item.BodyMarkdown, "`"+approvalID+"`") {
				t.Fatal("showcase approval note does not reference the stored person-approved request")
			}
			next++
		}
	}
	if agentEntries < 2 || next != len(want) {
		t.Fatalf("showcase activity: %d agent comments, %d/%d interleaved notes", agentEntries, next, len(want))
	}
	var report ticketwork.Report
	if err := api.do(admin, "", http.MethodGet, "/api/nodes/"+showcase+"/agent-work", nil, http.StatusOK, &report, nil); err != nil {
		t.Fatal(err)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].Label == nil || *report.Sessions[0].Label != "Lumen Scribe · Done" || report.Sessions[0].Phase != "stopped" {
		t.Fatal("showcase agent work must identify Lumen Scribe's completed review")
	}
	var session harness.Session
	if err := api.do(admin, "", http.MethodGet, "/api/projects/"+project+"/harness-sessions/"+report.Sessions[0].ID, nil, http.StatusOK, &session, nil); err != nil {
		t.Fatal(err)
	}
	if session.WorkOrderID == nil {
		t.Fatal("showcase session has no work order")
	}
	var order workorders.Order
	if err := api.do(admin, "", http.MethodGet, "/api/work-orders/"+*session.WorkOrderID, nil, http.StatusOK, &order, nil); err != nil {
		t.Fatal(err)
	}
	if order.Status != "done" || len(order.Criteria) == 0 || order.Criteria[0].CheckedBy == nil || *order.Criteria[0].CheckedBy != admin.ID {
		t.Fatal("showcase work must be done with a person's acceptance check")
	}
	for _, example := range []struct {
		node  string
		votes int
	}{{showcase, 0}, {harbor, 1}} {
		var ratings deliveryvote.Page
		if err := api.do(admin, "", http.MethodGet, "/api/nodes/"+example.node+"/delivery-ratings", nil, http.StatusOK, &ratings, nil); err != nil {
			t.Fatal(err)
		}
		if len(ratings.Sessions) != 1 || ratings.Sessions[0].Votes != example.votes {
			t.Fatalf("ticket %s: want %d rework marks", example.node, example.votes)
		}
		if example.votes == 0 && ratings.Sessions[0].Mine != nil {
			t.Fatal("showcase must not have a rework mark")
		}
		if example.votes == 1 && (ratings.Sessions[0].Mine == nil || !slices.Contains(ratings.Sessions[0].Mine.Tags, "rework")) {
			t.Fatal("Harbor must retain an explicit rework example")
		}
	}
	if n := scalar(t, database, tenantID, `SELECT count(*) FROM approval_requests a JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.resource_id JOIN principals p ON p.tenant_id=a.tenant_id AND p.id=a.agent_principal_id JOIN approval_decisions d ON d.tenant_id=a.tenant_id AND d.request_id=a.id JOIN principals decider ON decider.tenant_id=d.tenant_id AND decider.id=d.decided_by_principal_id WHERE n.key='LT-1' AND a.scope='nodes.write' AND p.name='Lumen Scribe' AND d.decision='approved' AND decider.name='Demo Operator'`); n != 1 {
		t.Fatal("showcase must retain a real Scribe request approved by a person")
	}
	if n := scalar(t, database, tenantID, `SELECT count(*) FROM approval_requests a JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.resource_id JOIN principals p ON p.tenant_id=a.tenant_id AND p.id=a.agent_principal_id WHERE n.key='HT-1' AND a.scope='nodes.write' AND p.name='Harbor Clerk' AND a.expires_at>now() AND NOT EXISTS (SELECT 1 FROM approval_decisions d WHERE d.tenant_id=a.tenant_id AND d.request_id=a.id)`); n != 1 {
		t.Fatal("Harbor Clerk's separate approval must stay pending")
	}
}

// Assert through the same reads used by the UI: rows alone do not prove that
// the launch cascade, journey gate and historical session can be displayed.
func assertCaptureState(t *testing.T, database *dbtest.DB, tenantID string) {
	t.Helper()
	var admin tenant.Principal
	var ticket, glass, project, sessionID, scribeID string
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, tenantID, func(tx pgx.Tx) error {
		admin.TenantID, admin.Kind = tenantID, tenant.Person
		if err := tx.QueryRow(t.Context(), `SELECT id::text,name FROM principals WHERE name='Demo Operator'`).Scan(&admin.ID, &admin.Name); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM nodes WHERE key='NGLASS-1'`).Scan(&glass); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT s.id::text,s.project_id::text,s.ticket_node_id::text,s.agent_principal_id::text FROM harness_sessions s WHERE s.harness='codex'`).Scan(&sessionID, &project, &ticket, &scribeID)
	})
	if err != nil {
		t.Fatal(err)
	}
	api, err := newAPI(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	assertHarnessMix(t, api, admin)
	view, err := (&seeder{api: api, admin: admin}).journeyView(glass)
	if err != nil || view.Stage != "requirements" || view.RequirementsScope == "" {
		t.Fatalf("pending journey: %+v, error %v", view, err)
	}
	if n := scalar(t, database, tenantID, `SELECT count(*) FROM approval_requests a JOIN nodes n ON n.id=a.resource_id AND n.tenant_id=a.tenant_id WHERE n.key='NGLASS-1' AND a.scope LIKE 'journey.requirements.%' AND a.expires_at>now() AND NOT EXISTS (SELECT 1 FROM approval_decisions d WHERE d.request_id=a.id AND d.tenant_id=a.tenant_id)`); n != 1 {
		t.Fatal("missing pending revision-bound gate")
	}
	var gateView journey.Journey
	if err := api.do(admin, "", http.MethodGet, "/api/projects/"+glass+"/journey", nil, http.StatusOK, &gateView, nil); err != nil {
		t.Fatal(err)
	}
	gateVisible := false
	for _, stage := range gateView.Stages {
		if stage.Key == "requirements" && stage.GateOfferID != nil && stage.GateOfferState == "pending" && stage.GateScope == view.RequirementsScope {
			gateVisible = true
		}
	}
	if !gateVisible {
		t.Fatal("GateApprovals has no pending requirements offer")
	}
	var session harness.Session
	path := "/api/projects/" + project + "/harness-sessions/" + sessionID
	if err := api.do(admin, "", http.MethodGet, path, nil, http.StatusOK, &session, nil); err != nil {
		t.Fatal(err)
	}
	if session.RunStatus == nil || *session.RunStatus != "completed" || session.StoppedAt == nil || session.HeartbeatAt != nil || session.HasProblem == nil || *session.HasProblem {
		t.Fatal("session should have completed evidence, an end and no fake heartbeat or problem")
	}
	var provenance harness.ProvenancePage
	if err := api.do(admin, "", http.MethodGet, path+"/provenance", nil, http.StatusOK, &provenance, nil); err != nil {
		t.Fatal(err)
	}
	if len(provenance.Revisions) != 1 || provenance.Revisions[0].RecordedBy != scribeID || len(provenance.Revisions[0].Items) != 1 {
		t.Fatal("historical instruction provenance missing")
	}
	item := provenance.Revisions[0].Items[0]
	instructions := "Review the fictional lantern label. Ask a person before changing the release.\n"
	digest := sha256.Sum256([]byte(instructions))
	if item.LogicalName != "lantern-review/SKILL.md" || item.ContentSHA256 == nil || *item.ContentSHA256 != hex.EncodeToString(digest[:]) || item.ByteSize == nil || *item.ByteSize != int64(len(instructions)) {
		t.Fatal("provenance does not match the authored fictional instructions")
	}
	code, activity, err := api.call(admin, "", http.MethodGet, "/api/nodes/"+ticket+"/activity", nil, nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("activity status %d error %v", code, err)
	}
	for _, text := range []string{"Ivo Quill", "Nia Frost", "Lumen Scribe", "I work on this", "/agents/" + sessionID, *session.RunID} {
		if !strings.Contains(string(activity), text) {
			t.Fatalf("linked ticket activity lacks %q", text)
		}
	}
}

// Exercise StartAgentDialog's exact data source and the Agents list. Each
// enrollment must expose a pin for its own harness, not the first global pin.
func assertHarnessMix(t *testing.T, api *api, admin tenant.Principal) {
	t.Helper()
	want := map[string]struct{ agent, label string }{
		"codex":  {"Lumen Scribe", "Lumen desk"},
		"claude": {"Harbor Clerk", "Harbor desk"},
		"grok":   {"Glass Scout", "North Glass desk"},
	}
	var profiles []modelregistry.Profile
	if err := api.do(admin, "", http.MethodGet, "/api/models", nil, http.StatusOK, &profiles, nil); err != nil {
		t.Fatal(err)
	}
	byID := map[string]modelregistry.Profile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
		if profile.Slug == "demo-grok-history" {
			matches := slices.ContainsFunc(profiles, func(source modelregistry.Profile) bool {
				return source.Enabled && source.Harness == "cursor" && source.Family == "xai" &&
					source.Model == profile.Model && source.Effort == profile.Effort && source.Version == profile.Version && source.Tier == profile.Tier
			})
			if !matches {
				t.Fatal("demo Grok pin does not reuse an enabled xAI registry pin")
			}
		}
	}
	var catalog agentaccounts.Catalog
	if err := api.do(admin, "", http.MethodGet, "/api/agent-accounts/catalog?role=build", nil, http.StatusOK, &catalog, nil); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Hosts) != 1 || catalog.Hosts[0].Label != "Demo workstation" || len(catalog.Hosts[0].Harnesses) != len(want) {
		t.Fatalf("StartAgentDialog must offer all three harnesses on the demo host: %+v", catalog)
	}
	accounts := map[string]agentaccounts.CatalogAccount{}
	for _, harness := range catalog.Hosts[0].Harnesses {
		expected, ok := want[harness.Harness]
		if !ok || len(harness.Accounts) != 1 {
			t.Fatalf("unexpected demo harness or account count: %s", harness.Harness)
		}
		account := harness.Accounts[0]
		if account.Label != expected.label || !account.Available || len(account.Models) != 1 || len(account.Models[0].Efforts) != 1 {
			t.Fatalf("missing fictional account, availability or explicit model grant: %+v", account)
		}
		model := account.Models[0]
		effort := model.Efforts[0]
		profile := byID[effort.ProfileID]
		if !profile.Enabled || profile.Harness != harness.Harness || profile.Model != model.Model || profile.Effort != effort.Effort || profile.Version != effort.Version {
			t.Fatalf("%s account has a mismatched registry profile", harness.Harness)
		}
		if _, exists := accounts[harness.Harness]; exists {
			t.Fatalf("duplicate harness %s", harness.Harness)
		}
		accounts[harness.Harness] = account
	}
	var page struct {
		Items []harness.SessionSummary `json:"items"`
	}
	if err := api.do(admin, "", http.MethodGet, "/api/harness-sessions?view=current", nil, http.StatusOK, &page, nil); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(want) {
		t.Fatalf("Agents list has %d sessions, want three", len(page.Items))
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		expected, ok := want[item.Harness]
		account := accounts[item.Harness]
		if !ok || seen[item.Harness] || item.Agent == nil || item.Agent.Name != expected.agent || item.AgentPrincipalID != account.RegisteredBy {
			t.Fatalf("Agents list missing a distinct fictional agent for %s", item.Harness)
		}
		seen[item.Harness] = true
		var session harness.Session
		if err := api.do(admin, "", http.MethodGet, "/api/projects/"+item.ProjectID+"/harness-sessions/"+item.ID, nil, http.StatusOK, &session, nil); err != nil {
			t.Fatal(err)
		}
		model := account.Models[0]
		if session.RunID == nil || session.RunStatus == nil || *session.RunStatus != "completed" || session.StoppedAt == nil || session.HeartbeatAt != nil || session.HasProblem == nil || *session.HasProblem {
			t.Fatalf("%s session must have completed evidence and no fabricated heartbeat or problem", item.Harness)
		}
		if session.AccountLabel == nil || *session.AccountLabel != expected.label {
			t.Fatalf("%s session account/model must match its enrollment", item.Harness)
		}
		// The session separates effort suffixes while retaining the enrollment's
		// exact model string and unique registry profile for audit.
		effort := model.Efforts[0]
		if session.Model == nil || *session.Model != strings.TrimSuffix(model.Model, "-"+effort.Effort) || session.ModelRaw == nil || *session.ModelRaw != model.Model || session.ModelProfileID == nil || *session.ModelProfileID != effort.ProfileID || session.ReasoningEffort == nil || *session.ReasoningEffort != effort.Effort {
			t.Fatalf("%s session identity must match its enrollment's model, effort and registry profile", item.Harness)
		}
	}
}

func TestDemoProfileSelectsEnabledHarness(t *testing.T) {
	profiles := []modelregistry.Profile{
		{ID: "unrelated", Harness: "pi", Enabled: true},
		{ID: "disabled", Harness: "claude", Enabled: false},
		{ID: "claude-pin", Harness: "claude", Enabled: true},
		{ID: "codex-pin", Harness: "codex", Enabled: true},
		{ID: "grok-pin", Harness: "grok", Enabled: true},
	}
	for _, harness := range []string{"codex", "claude", "grok"} {
		profile, err := (&seeder{}).demoProfile(profiles, harness)
		if err != nil || profile.ID != harness+"-pin" {
			t.Fatalf("%s: profile %+v, error %v", harness, profile, err)
		}
	}
	if _, err := (&seeder{}).demoProfile(profiles[:2], "claude"); err == nil {
		t.Fatal("missing enabled harness must fail")
	}
}

func TestDemoMissingHarnessRollsBack(t *testing.T) {
	t.Setenv("AEON_ENV", "dev")
	database := dbtest.Open(t)
	tenantID, err := tenantbootstrap.Create(t.Context(), database.App, "missing-harness-demo", "Missing Harness Demo")
	if err != nil {
		t.Fatal(err)
	}
	// An incomplete registry must not borrow another harness's profile or
	// commit Scribe's completed history before finding Claude unavailable.
	// Pins are immutable, so create this state rather than updating a pin.
	// Pin the current catalog: an older catalog would intentionally upgrade
	// and fill in the missing harness before the demo accesses it.
	err = db.InTenant(dbtest.Seed(t.Context()), database.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_refresh_settings(tenant_id,catalog_version) VALUES($1,$2)`, tenantID, modelregistry.CatalogVersion); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled)
			VALUES($1::uuid,'demo-enabled-codex','1','codex','openai','test-model','high','standard',true),
			      ($1::uuid,'demo-disabled-claude','1','claude','anthropic','test-model','high','standard',false)`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	before := seedRows(t, database, tenantID)
	if _, err := Seed(t.Context(), database.App, "missing-harness-demo"); err == nil || err.Error() != "no enabled claude model profile" {
		t.Fatalf("missing registry harness: %v", err)
	}
	if after := seedRows(t, database, tenantID); !slices.Equal(before, after) {
		t.Fatal("missing harness left partial seed resources")
	}
}

func scalar(t *testing.T, database *dbtest.DB, tenantID, query string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), query).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
