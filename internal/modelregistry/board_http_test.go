// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

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
	boardError(t, boardCall(t, member, "PUT", "/api/model-preferences/situations", map[string]any{"small_hours": 3, "fix_rounds": 4, "revision": 0}, ""), 403, "permission_denied")
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
