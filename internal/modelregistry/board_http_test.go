// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func boardCall(t *testing.T, p tenant.Principal, method, path string, body any, person string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request = request.WithContext(tenant.WithPrincipal(request.Context(), p))
	if person != "" {
		request.Header.Set("If-Prefs-Person", person)
	}
	mux := http.NewServeMux()
	(&Module{pool: appPool}).Mount(mux)
	out := httptest.NewRecorder()
	mux.ServeHTTP(out, request)
	return out
}
func boardDecode[T any](t *testing.T, out *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if out.Code != status {
		t.Fatalf("status %d: %s; want %d", out.Code, out.Body.String(), status)
	}
	var value T
	if err := json.Unmarshal(out.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func boardFixture(t *testing.T) (tenant.Principal, tenant.Principal) {
	t.Helper()
	reset(t)
	admin := makePrincipal(t, "board", "person", "Admin", []string{"admin"})
	member := addPrincipal(t, admin.TenantID, "person", "Member", []string{"member"})
	prefDoc(t, admin)
	return admin, member
}
func boardError(t *testing.T, out *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if out.Code != status || !strings.Contains(out.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("%d %s; want %d %s", out.Code, out.Body.String(), status, code)
	}
}

// Risk: an order could write another person's board, overwrite a concurrent
// edit, or emit a second event for a rejected write.
func TestBoardPersonRevisionIdentityAndLegacyRetirement(t *testing.T) {
	admin, member := boardFixture(t)
	body := map[string]any{"rank": []string{"anthropic:opus", "openai:sol"}, "not": []string{}, "revision": 0}
	path := "/api/model-preferences/orders/backend/first?for=me"
	boardError(t, boardCall(t, member, "PUT", path, body, ""), 428, "person_precondition_required")
	boardError(t, boardCall(t, member, "PUT", path, body, admin.ID), 409, "preference_person_changed")
	saved := boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", path, body, member.ID), 200)
	if saved.Revision != 1 || saved.PersonID == nil || *saved.PersonID != member.ID {
		t.Fatal(saved)
	}
	boardError(t, boardCall(t, member, "PUT", path, body, member.ID), 409, "stale_revision")
	doc := boardDecode[boardDocument](t, boardCall(t, member, "GET", "/api/model-preferences/board", nil, ""), 200)
	found := false
	for _, column := range doc.Columns {
		if column.Column == "backend" {
			found = true
			if len(column.List) != 2 || column.List[0].Line != "anthropic:opus" {
				t.Fatal(column)
			}
		}
	}
	if !found {
		t.Fatal("backend missing")
	}
	if eventCount(t, admin, "model.preferences_changed") != 1 {
		t.Fatal("rejected writes emitted events")
	}
	boardError(t, boardCall(t, member, "PUT", "/api/model-preferences/levels/person", map[string]any{"revision": 0}, member.ID), 405, "model_preferences_read_only")
	agent := addPrincipal(t, admin.TenantID, "agent", "Agent", []string{"admin"})
	agent.Scopes = []string{"models.read", "model_prefs.manage"}
	boardError(t, boardCall(t, agent, "PUT", path, body, member.ID), 403, "person_required")
}
func TestBoardTemplateDryRunPreservesOwnOrdersAndResetsAtomically(t *testing.T) {
	admin, member := boardFixture(t)
	order := map[string]any{"rank": []string{"anthropic:sonnet", "openai:sol"}, "not": []string{}, "revision": 0}
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/backend/first", order, member.ID), 200)
	profile := map[string]any{"template": "best", "thinking": "deep", "revision": 1}
	dry := boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/profile?dry_run=true", profile, member.ID), 200)
	if !dry.DryRun || dry.Revision != 1 {
		t.Fatal(dry)
	}
	for _, moved := range dry.Moved {
		if moved.Column == "backend" {
			t.Fatal("template overwrote an own column")
		}
	}
	if eventCount(t, admin, "model.preferences_changed") != 1 {
		t.Fatal("dry run wrote an event")
	}
	profile["replace_own"] = true
	dry = boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/profile?dry_run=true", profile, member.ID), 200)
	saved := boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/profile", profile, member.ID), 200)
	if saved.Revision != 2 || !reflect.DeepEqual(dry.Moved, saved.Moved) {
		t.Fatal("dry-run diff disagrees with committed write", dry, saved)
	}
	if eventCount(t, admin, "model.preferences_changed") != 2 {
		t.Fatal("profile write was not one event")
	}
}
func TestBoardProjectRulesOnlyTightenAndKeepCapabilityPins(t *testing.T) {
	admin, _ := boardFixture(t)
	var project string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'BOARD-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, admin.TenantID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	workspace := map[string]any{"top": []rulePin{{"anthropic:opus", "Design authority"}}, "bottom": []rulePin{{"xai:grok", "Keep for when tools exist"}}, "not": map[string]string{}, "revision": 0}
	boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", "/api/model-rules/workspace/design", workspace, ""), 200)
	boardError(t, boardCall(t, admin, "PUT", "/api/model-rules/project/design?project_id="+project, map[string]any{"top": []rulePin{}, "bottom": []rulePin{}, "not": map[string]string{}, "revision": 0}, ""), 422, "looser_than_workspace")
	workspace["top"] = []rulePin{{"openai:astra", "Project first"}, {"anthropic:opus", "Design authority"}}
	workspace["revision"] = 0
	rules := boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", "/api/model-rules/project/design?project_id="+project, workspace, ""), 200)
	if len(rules.Rules) != 3 {
		t.Fatal("inherited rules were copied or lost", rules)
	}
	board := boardDecode[boardDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/board?layer=rules&project_id="+project, nil, ""), 200)
	for _, column := range board.Columns {
		if column.Column == "design" {
			if len(column.Top) != 2 || column.Top[0].Line != "openai:astra" || len(column.Bottom) != 1 || column.Bottom[0].Line != "xai:grok" || len(column.Cant) != 1 {
				t.Fatal("project pin order or retained capability rule", column)
			}
		}
	}
}
func TestBoardMigrationPreservesRowsAndExplainsDroppedCells(t *testing.T) {
	admin, member := boardFixture(t)
	var project string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		ctx := t.Context()
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'MIG-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, admin.TenantID).Scan(&project); err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(ctx, tx, "frontend", "")
		if err != nil {
			return err
		}
		profiles, err := listProfiles(ctx, tx)
		if err != nil {
			return err
		}
		sonnet := profileBySlug(profiles, "claude-sonnet-high")
		if sonnet.ID == "" {
			return fmt.Errorf("missing fixture profile")
		}
		for _, scope := range []modelprefs.Scope{{Level: "default"}, {Level: "person", PersonID: &member.ID}, {Level: "project", ProjectID: &project}} {
			saved, err := modelprefs.SaveScope(ctx, tx, admin, scope)
			if err != nil {
				return err
			}
			row := modelprefs.Row{Locked: scope.Level == "default", Cells: map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: sonnet.ID}, "complex": {Mode: "latest", Family: "openai", Line: "sol", Effort: "xhigh"}}}
			if err := modelprefs.PutRow(ctx, tx, admin, saved, kind.ID, row); err != nil {
				return err
			}
		}
		// Recreate the pre-expansion state: no ranked workspace existed yet.
		if _, err := tx.Exec(ctx, `DELETE FROM model_pref_profiles WHERE scope='workspace'`); err != nil {
			return err
		}
		var original []byte
		if err := tx.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(c) ORDER BY scope_id,bucket) FROM model_pref_cells c`).Scan(&original); err != nil {
			return err
		}
		var audit []byte
		if err := tx.QueryRow(ctx, `SELECT aeon_migrate_model_board($1::uuid)`, admin.TenantID).Scan(&audit); err != nil {
			return err
		}
		var data struct {
			Cells   []json.RawMessage `json:"cells"`
			Dropped []json.RawMessage `json:"dropped"`
		}
		if err := json.Unmarshal(audit, &data); err != nil {
			return err
		}
		if len(data.Cells) != 6 || len(data.Dropped) < 3 {
			t.Fatalf("migration did not account for old cells: %s", audit)
		}
		var roundTrip []byte
		if err := tx.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(c) ORDER BY scope_id,bucket) FROM model_pref_cells c`).Scan(&roundTrip); err != nil {
			return err
		}
		if !bytes.Equal(original, roundTrip) {
			t.Fatal("migration destroyed legacy rows")
		}
		s, err := modelprefs.LoadBoard(ctx, tx, &member.ID, project)
		if err != nil {
			return err
		}
		if len(s.Orders) != 2 || len(s.Rules) != 1 || s.Rules[0].Line != "anthropic:sonnet" || s.Rules[0].Why != "Was AEON’s pinned UI build model (Sonnet 5.5 xhigh)" {
			t.Fatalf("migration lost effective preferences: %+v", s)
		}
		for _, o := range s.Orders {
			if o.Thinking == nil || *o.Thinking != "standard" || o.Rank[0] != "anthropic:sonnet" {
				t.Fatal(o)
			}
		}
		var again []byte
		if err := tx.QueryRow(ctx, `SELECT aeon_migrate_model_board($1::uuid)`, admin.TenantID).Scan(&again); err != nil {
			return err
		}
		if !bytes.Equal(audit, again) {
			t.Fatal("migration is not idempotent")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestBoardKindsFieldsAndSituationLimits(t *testing.T) {
	admin, member := boardFixture(t)
	boardError(t, boardCall(t, member, "PUT", "/api/model-preferences/situations", map[string]any{"small_hours": 3, "fix_rounds": 4, "revision": 0}, ""), 403, "forbidden")
	limits := boardDecode[modelprefs.SituationLimits](t, boardCall(t, admin, "PUT", "/api/model-preferences/situations", map[string]any{"small_hours": 3, "fix_rounds": 4, "revision": 0}, ""), 200)
	if limits.Revision != 1 || limits.SmallHours != 3 || limits.FixRounds != 4 {
		t.Fatal(limits)
	}
	kind := boardDecode[workKind](t, boardCall(t, admin, "POST", "/api/work-kinds", map[string]any{"label": "Data build", "hint": "Data jobs", "examples": []string{"A data import"}, "labels": []string{"data"}, "position": 20}, ""), 201)
	if len(kind.Examples) != 1 || len(kind.Labels) != 1 || kind.Position != 20 {
		t.Fatal(kind)
	}
	page := boardDecode[workKindPage](t, boardCall(t, member, "GET", "/api/work-kinds", nil, ""), 200)
	slugs := []string{}
	for _, k := range page.Items {
		slugs = append(slugs, k.Slug)
	}
	ordered := boardDecode[workKindPage](t, boardCall(t, admin, "PUT", "/api/work-kinds/order", map[string]any{"slugs": slugs}, ""), 200)
	if len(ordered.Items) != len(slugs) {
		t.Fatal(ordered)
	}
	for i, k := range ordered.Items {
		if k.Position != i {
			t.Fatal("kind positions did not follow request", ordered)
		}
	}
}

// Risk: body decoding can outlive an admin grant; the final write must recheck.
func TestBoardMutationRechecksRevokedGrant(t *testing.T) {
	admin, _ := boardFixture(t)
	barrier := &mutationBarrier{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"revision":0,"thinking":"deep"}`)}
	request := httptest.NewRequest("PUT", "/api/model-preferences/profile?for=default", barrier)
	request = request.WithContext(tenant.WithPrincipal(request.Context(), admin))
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	mux := http.NewServeMux()
	(&Module{pool: appPool}).Mount(mux)
	go func() {
		defer close(done)
		if err := authz.Require(authz.BindPool(request.Context(), appPool), "model_prefs.manage", authz.Scope{}); err != nil {
			httpapi.WriteError(recorder, 403, err.Error())
			return
		}
		mux.ServeHTTP(recorder, request)
	}()
	<-barrier.entered
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if err := preferenceFence(t.Context(), tx, admin); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, admin.ID)
		return err
	}); err != nil {
		close(barrier.release)
		<-done
		t.Fatal(err)
	}
	close(barrier.release)
	<-done
	if recorder.Code != 403 {
		t.Fatal("revoked write committed", recorder.Code, recorder.Body.String())
	}
	if eventCount(t, admin, "model.preferences_changed") != 0 {
		t.Fatal("revoked write emitted an event")
	}
}

// Risk: the board display and dispatch might disagree about residency, new lines,
// account limits, or a bottom pin. Exercise the same persisted orders in both.
func TestBoardDispatchTraceResidencyAndNewLineExclusion(t *testing.T) {
	admin, member := boardFixture(t)
	runner := addPrincipal(t, admin.TenantID, "agent", "Board runner", []string{"admin"})
	var account string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		ctx := t.Context()
		if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,'board-claude','claude','board-runner',$2,'Board',now(),true,'board-generation',$3) RETURNING id::text`, admin.TenantID, runner.ID, admin.ID).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, admin.TenantID, account); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override = "sprint"
		schedule.Reserve = capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, admin.TenantID, admin.ID, account, raw); err != nil {
			return err
		}
		_, err := insertProfile(ctx, tx, admin.TenantID, profileWrite{Slug: "board-new-line", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-terra-1.0", Effort: "high", Tier: "strong"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/backend/first", map[string]any{"rank": []string{"openai:sol"}, "not": []string{}, "revision": 0}, member.ID), 200)
	boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", "/api/model-rules/workspace/backend", map[string]any{"top": []rulePin{}, "bottom": []rulePin{{"anthropic:opus", "Approved fallback"}}, "not": map[string]string{}, "revision": 0}, ""), 200)
	doc := boardDecode[boardDocument](t, boardCall(t, member, "GET", "/api/model-preferences/board", nil, ""), 200)
	inTray := false
	for _, card := range doc.Tray {
		if card.Line == "anthropic:terra" {
			inTray = true
		}
	}
	if !inTray {
		t.Fatalf("new line did not stay in the tray: %+v", doc.Tray)
	}
	var placement WorkPlacement
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		now := time.Now()
		got, err := PlacementFor(t.Context(), tx, member, WorkQuery{Role: "build", Area: "backend"}, now)
		if err != nil {
			return err
		}
		placement = got
		if got.PlannedProfileID == nil || got.Column != "backend" || got.Situation != "first" || got.CardIndex != 2 || got.Lock == nil || got.Lock.Value != "bottom" || got.PreferenceOf.Person == nil || *got.PreferenceOf.Person != member.ID || len(got.Held) == 0 {
			t.Fatalf("board trace disagreed: %+v", got)
		}
		ps, err := listProfiles(t.Context(), tx)
		if err != nil {
			return err
		}
		var selected Profile
		for _, p := range ps {
			if p.ID == *got.PlannedProfileID {
				selected = p
			}
		}
		family, line, _ := ProfileLine(selected)
		if family+":"+line != "anthropic:opus" {
			t.Fatal("new line or wrong fallback ran", selected)
		}
		raw, err := got.JSON()
		if err != nil {
			return err
		}
		_, used, err := PinnedBottomUsed(raw, selected.ID)
		if err != nil {
			return err
		}
		if !used {
			t.Fatal("actual bottom selection did not produce evidence")
		}
		_, used, err = PinnedBottomUsed(raw, "11111111-1111-4111-8111-111111111111")
		if err != nil {
			return err
		}
		if used {
			t.Fatal("different actual profile counted as the bottom pin")
		}
		_, err = tx.Exec(t.Context(), `UPDATE account_allowance_windows SET allowance=0 WHERE account_id=$1`, account)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if placement.PlannedProfileID == nil {
		t.Fatal("no initial selection")
	}
	var blocked WorkResolution
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		var err error
		blocked, err = ResolveWork(t.Context(), tx, member, WorkQuery{Role: "build", Area: "backend"}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if blocked.Profile != nil || blocked.Trace.Blocked == "" {
		t.Fatal("exhausted allowance escaped", blocked)
	}
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/profile", map[string]any{"revision": 1, "residency": "eu"}, member.ID), 200)
	doc = boardDecode[boardDocument](t, boardCall(t, member, "GET", "/api/model-preferences/board", nil, ""), 200)
	for _, col := range doc.Columns {
		if col.Column == "backend" {
			if len(col.List) != 0 {
				t.Fatal("residency did not remove routes", col)
			}
			found := false
			for _, card := range col.Not {
				if card.Line == "anthropic:opus" && card.Lock != nil && card.Lock.Kind == "residency" {
					found = true
				}
			}
			if !found {
				t.Fatal("missing residency lock", col)
			}
		}
	}
}

func TestBoardMigrationLineNormalizationMatchesCatalog(t *testing.T) {
	admin, _ := boardFixture(t)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		cases := []Profile{{Family: "openai", Harness: "codex", Model: "gpt-6.1-sol"}, {Family: "anthropic", Harness: "claude", Model: "claude-opus-5-5"}, {Family: "anthropic", Harness: "pi", Model: "anthropic/claude-sonnet-5-5"}, {Family: "openai", Harness: "cursor", Model: "sol-6.1-high-fast"}, {Family: "google", Harness: "opencode", Model: "google/gemini-3.1-pro"}, {Family: "anthropic", Harness: "claude", Model: "opus"}, {Family: "anthropic", Harness: "cursor", Model: "opus-5.5-xhigh"}}
		for _, p := range cases {
			family, line, _ := ProfileLine(p)
			var got string
			if err := tx.QueryRow(t.Context(), `SELECT aeon_model_board_line($1,$2,$3)`, p.Family, p.Harness, p.Model).Scan(&got); err != nil {
				return err
			}
			if got != family+":"+line {
				t.Fatalf("migration line %s; catalog %s:%s", got, family, line)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: a tenant-wide bootstrap could leak or omit a receipt/event, or replay it.
func TestBoardMigrationAllTenantsKeepsReceiptsAndEventsIsolated(t *testing.T) {
	reset(t)
	first := makePrincipal(t, "board-migrate-one", "person", "First", []string{"admin"})
	second := makePrincipal(t, "board-migrate-two", "person", "Second", []string{"admin"})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, first.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT aeon_migrate_all_model_boards()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{first, second} {
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			var receipts, profiles, events int
			if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_pref_migrations),(SELECT count(*) FROM model_pref_profiles),(SELECT count(*) FROM events WHERE type='model.preferences_migrated')`).Scan(&receipts, &profiles, &events); err != nil {
				return err
			}
			if receipts != 1 || profiles != 1 || events != 1 {
				t.Fatal("tenant migration was incomplete or leaked", receipts, profiles, events)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, first.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT aeon_migrate_all_model_boards()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{first, second} {
		if eventCount(t, p, "model.preferences_migrated") != 1 {
			t.Fatal("migration replay appended another event")
		}
	}
}
