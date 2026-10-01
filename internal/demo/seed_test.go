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

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
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
	after := seedRows(t, database, first.TenantID)
	if slices.Equal(before, after) {
		t.Fatal("seed wrote no resources")
	}
	events := scalar(t, database, first.TenantID, `SELECT count(*) FROM events`)
	tickets := scalar(t, database, first.TenantID, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='ticket' AND n.deleted_at IS NULL`)
	kinds := scalar(t, database, first.TenantID, `SELECT count(DISTINCT k.slug) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug IN ('runbook','guideline','memory','external_system','related_project')`)
	knowledge := scalar(t, database, first.TenantID, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug IN ('runbook','guideline','memory','external_system','related_project')`)
	agents := scalar(t, database, first.TenantID, `SELECT count(*) FROM principals WHERE kind='agent' AND name IN ('Lumen Scribe','Harbor Clerk')`)
	sessions := scalar(t, database, first.TenantID, `SELECT count(*) FROM harness_sessions`)
	finished := scalar(t, database, first.TenantID, `SELECT count(*) FROM agent_runs WHERE status='completed' AND started_at IS NOT NULL AND ended_at IS NOT NULL`)
	pending := scalar(t, database, first.TenantID, `SELECT count(*) FROM approval_requests a WHERE NOT EXISTS (SELECT 1 FROM approval_decisions d WHERE d.tenant_id=a.tenant_id AND d.request_id=a.id)`)
	hours := scalar(t, database, first.TenantID, `SELECT count(*) FROM time_entries`)
	rates := scalar(t, database, first.TenantID, `SELECT count(*) FROM cost_unit_rates WHERE internal_amount=80.00 AND bill_amount=140.00 AND currency='EUR'`)
	if kinds != 5 || knowledge < 8 || agents != 2 || sessions < 1 || finished < 1 || pending < 1 || hours < 3 || rates != 1 || tickets < 40 {
		t.Fatalf("kinds %d knowledge %d agents %d sessions %d finished %d pending %d hours %d rates %d tickets %d", kinds, knowledge, agents, sessions, finished, pending, hours, rates, tickets)
	}
	assertCaptureState(t, database, first.TenantID)
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
	if again := scalar(t, database, first.TenantID, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='ticket' AND n.deleted_at IS NULL`); again != tickets {
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
		"model_profiles", "model_role_routes", "approval_requests", "approval_decisions", "agent_runs", "run_telemetry",
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
		return tx.QueryRow(t.Context(), `SELECT s.id::text,s.project_id::text,s.ticket_node_id::text,s.agent_principal_id::text FROM harness_sessions s`).Scan(&sessionID, &project, &ticket, &scribeID)
	})
	if err != nil {
		t.Fatal(err)
	}
	api, err := newAPI(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	var catalog agentaccounts.Catalog
	if err := api.do(admin, "", http.MethodGet, "/api/agent-accounts/catalog?role=build", nil, http.StatusOK, &catalog, nil); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Hosts) != 1 || catalog.Hosts[0].Label != "Lumen workstation" || len(catalog.Hosts[0].Harnesses) != 1 {
		t.Fatal("launch host or harness missing")
	}
	accounts := catalog.Hosts[0].Harnesses[0].Accounts
	if len(accounts) != 1 || accounts[0].Label != "Lumen desk" || len(accounts[0].Models) != 1 || len(accounts[0].Models[0].Efforts) != 1 || accounts[0].RegisteredBy != scribeID {
		t.Fatal("launch account or explicit model grant missing")
	}
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
