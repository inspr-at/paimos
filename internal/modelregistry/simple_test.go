// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: editing level 2 fails after inserting level 1, leaving a partial
// replacement, or a successful edit loses ranked order/rules/history.
func TestMinimalModelEditAtomicVersionAndReferences(t *testing.T) {
	admin, _ := boardFixture(t)
	ps := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	gemini := profileBySlug(ps, "gemini-gemini-2-5-pro-1024")
	beforeCount := len(ps)
	invalid := lineWrite{DisplayName: "Gemini revised", Note: "Careful reasoning", Route: "Google", Efforts: []string{"1024", "bogus"}}
	boardDecode[map[string]any](t, boardCall(t, admin, "PUT", "/api/models/lines/gemini/gemini-2.5-pro", invalid, ""), 400)
	after := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	if len(after) != beforeCount || profileBySlug(after, gemini.Slug).Retired || eventCount(t, admin, "model.line_edited") != 0 {
		t.Fatal("failed edit changed the registry")
	}
	custom := decode[Profile](t, &admin, "POST", "/api/models", `{"slug":"manual-qwen","version":"1","harness":"pi","family":"unknown","model":"openrouter/qwen/qwen3-coder","effort":"off","tier":"standard","note":"Trial model"}`, 201)
	oldLine := boardLineID(custom)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", map[string]any{"rank": []string{"openai:sol", oldLine, "anthropic:opus"}, "not": []string{}, "revision": 0}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_rules(tenant_id,scope,column_key,line,lock,why,set_by) VALUES($1,'workspace','docs',$2,'top','Locked model',$3)`, admin.TenantID, oldLine, admin.ID)
		return err
	})
	usage := boardDecode[lineUsageDocument](t, boardCall(t, admin, "GET", "/api/models/lines/pi/"+url.PathEscape(custom.Model)+"/usage", nil, ""), 200)
	revision := usage.Revision
	body := lineWrite{DisplayName: "Qwen Coder", Note: "Changed note", Route: "OpenRouter", Model: "openrouter/qwen/qwen4-coder", Efforts: []string{"off", "high"}, Revision: &revision}
	edited := boardDecode[struct {
		Profiles []Profile
		Revision string
	}](t, boardCall(t, admin, "PUT", "/api/models/lines/pi/"+url.PathEscape(custom.Model), body, ""), 200)
	if len(edited.Profiles) != 2 || edited.Profiles[0].ID == custom.ID || edited.Revision == revision || edited.Profiles[0].Note != "Changed note" || edited.Profiles[0].Source != "manual" {
		t.Fatal(edited)
	}
	newLine := boardLineID(edited.Profiles[0])
	inRegistry(t, admin, func(tx pgx.Tx) error {
		s, err := modelprefs.LoadBoard(t.Context(), tx, nil, "")
		if err != nil {
			return err
		}
		if !slices.Equal(s.Orders[0].Rank, []string{"openai:sol", newLine, "anthropic:opus"}) || len(s.Rules) != 1 || s.Rules[0].Line != newLine || s.Rules[0].Why != "Locked model" {
			t.Fatal("edit lost order or rule", s)
		}
		profiles, err := listProfiles(t.Context(), tx)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			if p.ID == custom.ID && (!p.Retired || p.Note != "Trial model") {
				t.Fatal("old immutable pin changed or remained active", p)
			}
		}
		return nil
	})
	if eventCount(t, admin, "model.line_edited") != 1 {
		t.Fatal("missing atomic edit event")
	}
	boardDecode[map[string]any](t, boardCall(t, admin, "PUT", "/api/models/lines/pi/"+url.PathEscape(body.Model), body, ""), 409)
}

// Risk: callers can forge discovery source or overflow the admin-owned note.
func TestMinimalModelSourceAndNoteOwnership(t *testing.T) {
	admin, member := boardFixture(t)
	payload := `{"slug":"notes","version":"1","harness":"codex","family":"openai","model":"gpt-6-sol","effort":"high","tier":"strong","note":"Useful for building"}`
	p := decode[Profile](t, &admin, "POST", "/api/models", payload, 201)
	if p.Source != "manual" || p.Note != "Useful for building" {
		t.Fatal(p)
	}
	status, _ := call(t, &member, "POST", "/api/models", payload)
	if status != 403 {
		t.Fatal("member created model", status)
	}
	status, _ = call(t, &admin, "POST", "/api/models", strings.TrimSuffix(payload, "}")+`,"source":"auto"}`)
	if status != 400 {
		t.Fatal("source was writable", status)
	}
	status, _ = call(t, &admin, "POST", "/api/models", strings.TrimSuffix(payload, "}")+`,"origin":"shipped"}`)
	if status != 400 {
		t.Fatal("origin was writable", status)
	}
	status, _ = call(t, &admin, "POST", "/api/models", strings.Replace(payload, "Useful for building", strings.Repeat("x", 81), 1))
	if status != 400 {
		t.Fatal("oversized note", status)
	}
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	if profileBySlug(profiles, "codex-6-1-sol-high").Source != "auto" || profileBySlug(profiles, "codex-6-1-sol-high").Origin != "shipped" {
		t.Fatal("seed source")
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var incompatible int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_profiles WHERE source NOT IN ('auto','manual')`).Scan(&incompatible); err != nil {
			return err
		}
		if incompatible != 0 {
			t.Fatal("provenance broke the source vocabulary for older readers")
		}
		return nil
	})
}

// Risk: a scheduled retirement blocks early, never becomes effective on the
// clock boundary, or emits the event again on each scheduler poll.
func TestMinimalScheduledRetirementBoundaryAndUndo(t *testing.T) {
	admin, _ := boardFixture(t)
	ps := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	profile := profileBySlug(ps, "codex-luna-medium")
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	result := boardDecode[map[string]any](t, boardCall(t, admin, "POST", "/api/models/"+profile.ID+"/retire", map[string]any{"reason": "Removed in Settings › Models", "retire_at": at}, ""), 200)
	if result["retired"] != false {
		t.Fatal("scheduled pin immediately retired", result)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		before, err := resolveRole(t.Context(), tx, resolveQuery{Role: "scout"}, at.Add(-time.Microsecond))
		if err != nil {
			return err
		}
		after, err := resolveRole(t.Context(), tx, resolveQuery{Role: "scout"}, at)
		if err != nil {
			return err
		}
		if before.Profile == nil || before.Profile.ID != profile.ID || after.Profile == nil || after.Profile.ID == profile.ID {
			t.Fatal("incorrect retirement boundary", before, after)
		}
		pending, err := applyScheduledRetirements(t.Context(), tx, at)
		if err != nil {
			return err
		}
		if len(pending) != 1 {
			t.Fatal(pending)
		}
		again, err := applyScheduledRetirements(t.Context(), tx, at.Add(time.Minute))
		if err != nil {
			return err
		}
		if len(again) != 0 {
			t.Fatal("repeated normal retirement")
		}
		return flushCatalogChanges(t.Context(), tx, admin, pending)
	})
	if eventCount(t, admin, "model.profile_retired") != 1 {
		t.Fatal("retirement event count")
	}
	boardDecode[map[string]any](t, boardCall(t, admin, "DELETE", "/api/models/"+profile.ID+"/retire", nil, ""), 200)
	if eventCount(t, admin, "model.profile_restored") != 1 {
		t.Fatal("undo event")
	}
}

func minimalAccount(t *testing.T, p tenant.Principal, profile Profile) string {
	t.Helper()
	var id string
	inRegistry(t, p, func(tx pgx.Tx) error {
		var runner string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Minimal model runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin'`, p.TenantID, runner); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,allowed_model_profile_ids) VALUES($1,$2,$3,'minimal',$4,'Minimal model',now(),true,'generation',$5,ARRAY[$6::uuid]) RETURNING id::text`, p.TenantID, profile.ID, profile.Harness, runner, p.ID, profile.ID).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, p.TenantID, id); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, id, raw)
		return err
	})
	return id
}

// Risk: preview mutates the saved order or shows a fallback that dispatch
// would reject, and members learn other people's private picks.
func TestMinimalRemovePreviewQualificationAndPrivacy(t *testing.T) {
	admin, member := boardFixture(t)
	ps := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	sol, opus := profileBySlug(ps, "codex-6-1-sol-high"), profileBySlug(ps, "claude-opus-high")
	minimalAccount(t, admin, sol)
	order := map[string]any{"rank": []string{"anthropic:opus", "openai:sol"}, "not": []string{}, "revision": 0}
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", order, ""), 200)
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/docs/first", order, member.ID), 200)
	preview := boardDecode[lineUsageDocument](t, boardCall(t, admin, "GET", "/api/models/lines/claude/"+opus.Model+"/usage", nil, ""), 200)
	if len(preview.UsedBy) != 2 || preview.Incomplete || preview.Replacement.Line == nil || *preview.Replacement.Line != "openai:sol" || preview.Revision == "" || len(preview.LineProfiles) == 0 {
		t.Fatal(preview)
	}
	for _, profile := range preview.LineProfiles {
		if profile.Harness != "claude" || profile.Model != opus.Model || profile.Retired {
			t.Fatal("usage snapshot is not the line it revised", profile)
		}
	}
	memberView := boardDecode[lineUsageDocument](t, boardCall(t, member, "GET", "/api/models/lines/claude/"+opus.Model+"/usage", nil, ""), 200)
	if len(memberView.UsedBy) != 2 {
		t.Fatal(memberView)
	}
	other := addPrincipal(t, admin.TenantID, "person", "Other", []string{"member"})
	hidden := boardDecode[lineUsageDocument](t, boardCall(t, other, "GET", "/api/models/lines/claude/"+opus.Model+"/usage", nil, ""), 200)
	if !hidden.Incomplete || len(hidden.UsedBy) != 1 {
		t.Fatal("private usage leaked", hidden)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		s, err := modelprefs.LoadBoard(t.Context(), tx, nil, "")
		if err != nil {
			return err
		}
		if !slices.Equal(s.Orders[0].Rank, order["rank"].([]string)) {
			t.Fatal("preview rewrote order")
		}
		return nil
	})
}

// Risk: the simple view disagrees with rank[0], canonical person/revision,
// native effort, locks or its visible inability to run the selected model.
func TestMinimalSimpleReadPreservesFirstAndFallback(t *testing.T) {
	admin, member := boardFixture(t)
	ps := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	minimalAccount(t, admin, profileBySlug(ps, "codex-6-1-sol-high"))
	order := map[string]any{"rank": []string{"anthropic:opus", "openai:sol"}, "not": []string{}, "revision": 0}
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/other/first", order, member.ID), 200)
	doc := boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	if doc.PersonID == nil || *doc.PersonID != member.ID || doc.Revision != 1 || !doc.All.Mine || doc.All.Line == nil || *doc.All.Line != "anthropic:opus" || len(doc.Unavailable) != 1 || doc.Unavailable[0].RunsInstead == nil || *doc.Unavailable[0].RunsInstead != "openai:sol" || !strings.Contains(doc.Unavailable[0].Reason, "account") {
		t.Fatal(doc)
	}
	defaultDoc := boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
	if defaultDoc.All.Mine || defaultDoc.All.Line == nil || *defaultDoc.All.Line != "openai:sol" || defaultDoc.Revision != 0 {
		t.Fatal(defaultDoc)
	}
	minimalAccount(t, admin, profileBySlug(ps, "claude-opus-xhigh"))
	project := editorProject(t, admin, "MINIMAL-3")
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var ticket, order, runner string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,project_id,fields) SELECT $1,id,'MINIMAL-1','Queued work','open',$2,'{"area":"backend"}' FROM node_kinds WHERE slug='work' RETURNING id::text`, admin.TenantID, project).Scan(&ticket); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,'MINIMAL-2','Queued order',$2,$3 FROM node_kinds WHERE slug='work_order' RETURNING id::text`, admin.TenantID, ticket, project).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, admin.TenantID, order, member.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT registered_by_principal_id::text FROM agent_accounts WHERE harness='codex' LIMIT 1`).Scan(&runner); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,queue_node_id,queue_by_principal_id,queue_at,queue_security_review_required) VALUES($1,$2,$3,$4,$5,now(),false)`, admin.TenantID, order, runner, ticket, member.ID)
		return err
	})
	inRegistry(t, member, func(tx pgx.Tx) error {
		c, err := loadBoardCatalog(t.Context(), tx)
		if err != nil {
			return err
		}
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		_, err = simpleNextFor(t.Context(), tx, member, &member.ID, false, now, c)
		return err
	})
	doc = boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	if doc.Next == nil || doc.Next.Ticket != "MINIMAL-1" || doc.Next.Line == nil || *doc.Next.Line != "openai:sol" || doc.Next.Reviewer.Line == nil || *doc.Next.Reviewer.Line != "anthropic:opus" || doc.Next.Reviewer.Effort == nil || *doc.Next.Reviewer.Effort != "xhigh" {
		t.Fatal("queued work or independent reviewer differs", doc.Next)
	}
}

// Risk: a queued scout or mechanical ticket is shown as blocked because the
// board does not apply. Dispatch keeps the role ladder; the page must too.
func TestMinimalQueuedScoutUsesRoleLadderWhenBoardDoesNotApply(t *testing.T) {
	admin, member := boardFixture(t)
	ps := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	minimalAccount(t, admin, profileBySlug(ps, "codex-luna-medium"))
	project := editorProject(t, admin, "SCOUT-3")
	var ticket string
	inRegistry(t, admin, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,project_id,fields) SELECT $1,id,'SCOUT-1','Queued scout','open',$2,'{"area":"backend","route_role":"scout"}' FROM node_kinds WHERE slug='work' RETURNING id::text`, admin.TenantID, project).Scan(&ticket); err != nil {
			return err
		}
		var order, runner string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,'SCOUT-2','Queued order',$2,$3 FROM node_kinds WHERE slug='work_order' RETURNING id::text`, admin.TenantID, ticket, project).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, admin.TenantID, order, member.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT registered_by_principal_id::text FROM agent_accounts WHERE harness='codex' LIMIT 1`).Scan(&runner); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,queue_node_id,queue_by_principal_id,queue_at,queue_security_review_required) VALUES($1,$2,$3,$4,$5,now(),false)`, admin.TenantID, order, runner, ticket, member.ID)
		return err
	})
	var want string
	inRegistry(t, member, func(tx pgx.Tx) error {
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		q := WorkQuery{TicketID: ticket, PersonID: &member.ID, ProjectID: project, Role: "scout", Area: "backend", Situation: "first", ExplicitBoard: true, Queued: true}
		board, err := resolveBoardWork(t.Context(), tx, member, q, now, nil)
		if err != nil {
			return err
		}
		if board != nil {
			t.Fatal("scout unexpectedly used the board; this no longer proves the ladder fallback")
		}
		mechanical, err := resolveBoardWork(t.Context(), tx, member, WorkQuery{Role: "mechanical", TicketID: ticket, Area: "backend", ExplicitBoard: true}, now, nil)
		if err != nil {
			return err
		}
		if mechanical != nil {
			t.Fatal("mechanical unexpectedly used the board")
		}
		resolved, err := ResolveWork(t.Context(), tx, member, q, now)
		if err != nil {
			return err
		}
		if resolved.Profile == nil {
			t.Fatal("scout ladder itself has nothing runnable")
		}
		want = boardLineID(*resolved.Profile)
		return nil
	})
	doc := boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	if doc.Next == nil || doc.Next.Ticket != "SCOUT-1" || doc.Next.Column != "backend" || doc.Next.Line == nil || *doc.Next.Line != want {
		t.Fatalf("queued scout role was reported blocked or left the ladder: next=%+v want=%s", doc.Next, want)
	}
}

// Risk: fallback stops at a failed column/default or borrows the wrong
// account. Every skip must explain the actual selected replacement.
func TestMinimalFallbackColumnDefaultRoleAndSkipReasons(t *testing.T) {
	admin, _ := boardFixture(t)
	ps := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	sol := profileBySlug(ps, "codex-6-1-sol-high")
	minimalAccount(t, admin, sol)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", map[string]any{"rank": []string{"xai:grok", "anthropic:opus"}, "not": []string{}, "revision": 0}, ""), 200)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"anthropic:sonnet"}, "not": []string{}, "revision": 1}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		resolved, err := resolveBoardWork(t.Context(), tx, admin, WorkQuery{Role: "build", Column: "backend", Area: "backend", Situation: "first", WorkspaceOnly: true}, now, nil)
		if err != nil {
			return err
		}
		if resolved == nil || resolved.Profile == nil || resolved.Profile.ID != sol.ID {
			t.Fatal("role fallback failed", resolved)
		}
		c, err := loadBoardCatalog(t.Context(), tx)
		if err != nil {
			return err
		}
		trace := simpleTraceFor(resolved, c)
		reasons := map[string]string{}
		for _, s := range trace {
			reasons[s.Line] += s.Reason
		}
		if !strings.Contains(reasons["xai:grok"], "tools") || !strings.Contains(reasons["anthropic:opus"], "account") || !strings.Contains(reasons["anthropic:sonnet"], "default:") {
			t.Fatal("skip reasons missing", trace)
		}
		return nil
	})
}

// Risk: accepting an arbitrary new or retired line silently grants dispatch,
// or the auto-accepted successor needs a manual account grant (AEON-1000).
func TestMinimalAutoAcceptOnlyUsedSuccessors(t *testing.T) {
	admin, _ := boardFixture(t)
	ps := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	sol := profileBySlug(ps, "codex-6-1-sol-high")
	account := minimalAccount(t, admin, sol)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"openai:sol"}, "not": []string{}, "revision": 0}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		pin, _ := observedPin(Observation{Harness: "codex", Model: "gpt-6.2-sol", Effort: "high"})
		accepted, err := acceptUsedSuccessor(t.Context(), tx, admin, pin)
		if err != nil {
			return err
		}
		if !accepted {
			t.Fatal("used successor remained pending")
		}
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		resolved, err := resolveBoardWork(t.Context(), tx, admin, WorkQuery{Role: "build", Column: "other", Situation: "first", WorkspaceOnly: true}, now, nil)
		if err != nil {
			return err
		}
		if resolved.Profile == nil || resolved.Profile.Model != "gpt-6.2-sol" || !slices.Equal(resolved.Trace.QualifyingAccountIDs, []string{account}) {
			t.Fatal("successor required a new account grant", resolved)
		}
		unused, _ := observedPin(Observation{Harness: "grok", Model: "grok-4.8", Effort: "high"})
		accepted, err = acceptUsedSuccessor(t.Context(), tx, admin, unused)
		if err != nil {
			return err
		}
		if accepted {
			t.Fatal("unused line auto accepted")
		}
		newLine, _ := observedPin(Observation{Harness: "codex", Model: "gpt-7-newline", Effort: "high"})
		accepted, err = acceptUsedSuccessor(t.Context(), tx, admin, newLine)
		if err != nil {
			return err
		}
		if accepted {
			t.Fatal("new line auto accepted")
		}
		return nil
	})
	// Exercise the actual refresh path: API lists contain model IDs only,
	// so the successor inherits the registered native efforts, never default.
	mod := NewWithVault(appPool, []byte(strings.Repeat("x", 32)))
	inRegistry(t, admin, func(tx pgx.Tx) error {
		cipher, err := linkvault.Encrypt(mod.vaultKey, admin.TenantID, "models/"+account+"/openai", "synthetic-discovery-fixture")
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO model_discovery_credentials(tenant_id,account_id,vendor,ciphertext) VALUES($1,$2,'openai',$3)`, admin.TenantID, account, cipher); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE model_refresh_settings SET api_enabled=true,auto_add_profiles=true`)
		return err
	})
	mod.discovery = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != vendorURLs["openai"] {
			t.Fatal("unexpected vendor")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-6.3-sol"},{"id":"gpt-7-newline"}]}`)), Header: http.Header{}}, nil
	})}
	result, err := mod.runRefresh(tenant.WithPrincipal(t.Context(), admin), admin, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added < 1 {
		t.Fatal("refresh did not accept a used successor", result)
	}
	if len(result.AcceptedModels) != 1 || result.AcceptedModels[0] != "openai:sol" {
		t.Fatalf("one accepted successor counted as %d profiles, models %v", result.Added, result.AcceptedModels)
	}
	for _, id := range result.NewLines {
		if slices.Contains(result.AcceptedModels, id) {
			t.Fatal("a new line was reported as accepted", id)
		}
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var accepted, bad int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE model='gpt-6.3-sol' AND effort='high' AND enabled),count(*) FILTER(WHERE (model='gpt-7-newline' AND enabled) OR (model='gpt-6.3-sol' AND effort='default')) FROM model_profiles`).Scan(&accepted, &bad); err != nil {
			return err
		}
		if accepted != 1 || bad != 0 {
			t.Fatal("incorrect successor admission", accepted, bad)
		}
		return nil
	})
}

// Risk: people remain interval-limited, concurrent checks reserve twice, or
// 429 lacks actionable retry information. The clock never sleeps.
func TestMinimalManualRefreshCooldownBypassesInterval(t *testing.T) {
	admin, _ := boardFixture(t)
	clock := time.Now().UTC().Truncate(time.Microsecond)
	mod := &Module{pool: appPool, validationClock: func(context.Context, pgx.Tx) (time.Time, error) { return clock, nil }}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET interval_minutes=1440,last_run_at=$1`, clock.Add(-time.Minute))
		return err
	})
	result, err := mod.runRefresh(tenant.WithPrincipal(t.Context(), admin), admin, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.At.Equal(clock) {
		t.Fatal(result)
	}
	_, err = mod.runRefresh(tenant.WithPrincipal(t.Context(), admin), admin, false)
	var cooldown *refreshCooldown
	if !errorsAsCooldown(err, &cooldown) || cooldown.RetryAfter != 300 {
		t.Fatal("missing exact cooldown", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/models/refresh", nil)
	request = request.WithContext(tenant.WithPrincipal(request.Context(), admin))
	response := httptest.NewRecorder()
	mod.refresh(response, request)
	if response.Code != 429 || response.Header().Get("Retry-After") != "300" || !strings.Contains(response.Body.String(), `"retry_after":300`) {
		t.Fatal(response.Code, response.Body.String())
	}
	clock = clock.Add(5 * time.Minute)
	if _, err := mod.runRefresh(tenant.WithPrincipal(t.Context(), admin), admin, false); err != nil {
		t.Fatal("cooldown did not end at boundary", err)
	}
	clock = clock.Add(time.Minute)
	scheduled, err := mod.runRefresh(tenant.WithPrincipal(t.Context(), admin), admin, true)
	if err != nil || scheduled.Added != 0 || len(scheduled.Sources) != 0 {
		t.Fatal("scheduler ignored interval", scheduled, err)
	}
}
func errorsAsCooldown(err error, out **refreshCooldown) bool {
	c, ok := err.(*refreshCooldown)
	*out = c
	return ok
}

// Risk: changing the pick loses the native name, maps an equidistant level
// downward, or permits a native review effort below xhigh.
func TestMinimalNativeEffortRetainedAndNearestTieUp(t *testing.T) {
	admin, member := boardFixture(t)
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/other/first", map[string]any{"rank": []string{"openai:sol"}, "not": []string{}, "revision": 0}, member.ID), 200)
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/other/first/thinking", map[string]any{"effort": "medium", "revision": 1}, member.ID), 200)
	doc := boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	if doc.All.Effort == nil || *doc.All.Effort != "medium" {
		t.Fatal(doc.All)
	}
	inRegistry(t, member, func(tx pgx.Tx) error {
		s, err := modelprefs.LoadBoard(t.Context(), tx, &member.ID, "")
		if err != nil {
			return err
		}
		d := modelprefs.ResolveBoard(s, modelprefs.BoardQuery{Column: "backend"}, nil)
		if d.Effort != "medium" || d.EffortLevel != 2 {
			t.Fatal("column lost All work native effort", d)
		}
		return nil
	})
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/other/first", map[string]any{"rank": []string{"anthropic:sonnet"}, "not": []string{}, "revision": 2}, member.ID), 200)
	doc = boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	if doc.All.Effort == nil || *doc.All.Effort != "high" {
		t.Fatal(doc.All)
	}
	boardError(t, boardCall(t, member, "PUT", "/api/model-preferences/orders/review:openai/first/thinking", map[string]any{"effort": "high", "revision": 3}, member.ID), 422, "review_requires_xhigh")
	low, high := 1, 3
	nearest := nearestProfile([]Profile{{Effort: "low", EffortLevel: &low}, {Effort: "high", EffortLevel: &high}}, "medium", ptrInt(2))
	if nearest.Effort != "high" {
		t.Fatal("tie mapped downward", nearest)
	}
	boardDecode[boardWriteResult](t, boardCall(t, member, "DELETE", "/api/model-preferences/orders/other/first?revision=3", nil, member.ID), 200)
	doc = boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	if doc.All.Mine {
		t.Fatal("reset retained a native person row", doc.All)
	}
	_ = admin
}

// Risk: a legacy {thinking, revision} write omits effort and clears the stored
// native name, so the column runs the thinking adjustment instead. JSON null
// remains the only clear.
func TestMinimalThinkingWriteKeepsNativeEffort(t *testing.T) {
	_, member := boardFixture(t)
	path := "/api/model-preferences/orders/other/first/thinking"
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/other/first", map[string]any{"rank": []string{"openai:sol"}, "not": []string{}, "revision": 0}, member.ID), 200)
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", path, map[string]any{"effort": "medium", "revision": 1}, member.ID), 200)
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", path, map[string]any{"thinking": "deep", "revision": 2}, member.ID), 200)
	doc := boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	if doc.All.Effort == nil || *doc.All.Effort != "medium" {
		got := "<nil>"
		if doc.All.Effort != nil {
			got = *doc.All.Effort
		}
		t.Fatalf("thinking-only write changed the running effort to %s", got)
	}
	inRegistry(t, member, func(tx pgx.Tx) error {
		s, err := modelprefs.LoadBoard(t.Context(), tx, &member.ID, "")
		if err != nil {
			return err
		}
		if s.Person == nil {
			t.Fatal("missing person profile")
		}
		found := false
		for _, o := range s.Orders {
			if o.ProfileID != s.Person.ID || o.Column != "other" || o.Situation != "first" {
				continue
			}
			found = true
			if o.Thinking == nil || *o.Thinking != "deep" || o.Effort == nil || *o.Effort != "medium" || o.EffortLevel == nil || *o.EffortLevel != 2 || !slices.Equal(o.Rank, []string{"openai:sol"}) {
				t.Fatalf("stored native effort was not kept: %+v", o)
			}
		}
		if !found {
			t.Fatal("missing other/first order")
		}
		for _, column := range []string{"other", "backend"} {
			d := modelprefs.ResolveBoard(s, modelprefs.BoardQuery{Column: column, Situation: "first"}, nil)
			if d.Effort != "medium" || d.EffortLevel != 2 {
				t.Fatalf("%s resolved through thinking instead of native effort: %q level %d", column, d.Effort, d.EffortLevel)
			}
		}
		return nil
	})
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", path, map[string]any{"thinking": "deep", "effort": nil, "revision": 3}, member.ID), 200)
	inRegistry(t, member, func(tx pgx.Tx) error {
		s, err := modelprefs.LoadBoard(t.Context(), tx, &member.ID, "")
		if err != nil {
			return err
		}
		for _, o := range s.Orders {
			if s.Person == nil || o.ProfileID != s.Person.ID || o.Column != "other" || o.Situation != "first" {
				continue
			}
			if o.Thinking == nil || *o.Thinking != "deep" || o.Effort != nil || o.EffortLevel != nil {
				t.Fatalf("null effort did not clear only the native override: %+v", o)
			}
		}
		d := modelprefs.ResolveBoard(s, modelprefs.BoardQuery{Column: "other", Situation: "first"}, nil)
		if d.Effort != "" || d.EffortLevel != 4 {
			t.Fatalf("cleared native effort did not return to stored thinking: %q level %d", d.Effort, d.EffortLevel)
		}
		return nil
	})
}

// Risk: an omitted kind dispatches on its own template while the page shows only
// Default, and a first pick rewrites away a line the resolved list hid.
func TestMinimalOmittedKindFollowsDefaultAndKeepsStoredTail(t *testing.T) {
	admin, _ := boardFixture(t)
	shown := func(doc simpleDocument, column string) (simpleRow, bool) {
		t.Helper()
		if doc.All.Column == column {
			return doc.All, true
		}
		for _, row := range doc.Exceptions {
			if row.Column == column {
				return row, true
			}
		}
		return simpleRow{}, false
	}
	simple := boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
	if _, ok := shown(simple, "concept"); ok || simple.All.Line == nil || *simple.All.Line != "openai:sol" {
		t.Fatalf("empty concept left Default: all=%+v exceptions=%+v", simple.All, simple.Exceptions)
	}
	rank := []string{"anthropic:opus", "xai:grok", "openai:sol"}
	excluded := []string{"anthropic:sonnet"}
	saved := boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", map[string]any{"rank": rank, "not": excluded, "revision": 0}, ""), 200)
	if saved.Revision != 1 {
		t.Fatal(saved)
	}
	board := boardDecode[boardDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/board?layer=default", nil, ""), 200)
	var backend boardColumn
	for _, column := range board.Columns {
		if column.Column == "backend" {
			backend = column
		}
	}
	if !slices.Equal(backend.Stored.Rank, rank) || !slices.Equal(backend.Stored.Not, excluded) {
		t.Fatalf("stored order dropped a line the list can hide: %+v", backend.Stored)
	}
	for _, card := range backend.List {
		if card.Line == "xai:grok" {
			t.Fatal("resolved list kept the tool-free line")
		}
	}
	held := false
	for _, line := range backend.Cant {
		if line.Line == "xai:grok" {
			held = true
		}
	}
	if !held {
		t.Fatalf("tool-free line missing from cant: %+v", backend.Cant)
	}
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"anthropic:sonnet"}, "not": []string{}, "revision": 1}, ""), 200)
	concept := boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/concept/first?for=default", map[string]any{"rank": []string{"anthropic:opus"}, "not": []string{}, "revision": 2}, ""), 200)
	if concept.Revision != 3 {
		t.Fatal(concept)
	}
	withConcept := boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
	row, ok := shown(withConcept, "concept")
	if !ok || row.Line == nil || *row.Line != "anthropic:opus" {
		t.Fatalf("explicit concept was hidden: %+v exceptions=%+v", row, withConcept.Exceptions)
	}
	boardDecode[boardWriteResult](t, boardCall(t, admin, "DELETE", "/api/model-preferences/orders/concept/first?for=default&revision=3", nil, ""), 200)
	after := boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
	if _, ok := shown(after, "concept"); ok || after.All.Line == nil || *after.All.Line != "anthropic:sonnet" {
		t.Fatalf("reset concept stayed its own pick: all=%+v exceptions=%+v", after.All, after.Exceptions)
	}
	resolved := boardDecode[boardDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/board?layer=default", nil, ""), 200)
	var conceptColumn boardColumn
	for _, column := range resolved.Columns {
		if column.Column == "concept" {
			conceptColumn = column
		}
	}
	if conceptColumn.Source != "follows" || len(conceptColumn.List) == 0 || conceptColumn.List[0].Line != "anthropic:sonnet" || !slices.Equal(conceptColumn.Stored.Rank, []string{"anthropic:sonnet"}) {
		t.Fatalf("concept dispatch left Default: %+v", conceptColumn)
	}
}
func ptrInt(n int) *int { return &n }

// Risk: models.read discloses another account's wait cause or reset/schedule
// through either resolver surface, including after usage sharing is revoked.
func TestModelWaitReasonsRespectCurrentAccountSharing(t *testing.T) {
	for _, cause := range []string{"allowance", "schedule"} {
		t.Run(cause, func(t *testing.T) {
			owner, member := boardFixture(t)
			admin := addPrincipal(t, owner.TenantID, "person", "Nonowning admin", []string{"admin"})
			agent := addPrincipal(t, owner.TenantID, "agent", "Scoped model reader", []string{"admin"})
			profiles := decode[[]Profile](t, &owner, "GET", "/api/models", "", 200)
			sol := profileBySlug(profiles, "codex-6-1-sol-xhigh")
			opus := profileBySlug(profiles, "claude-opus-xhigh")
			account := minimalAccount(t, owner, sol)
			fallback := minimalAccount(t, owner, opus)
			boardDecode[boardWriteResult](t, boardCall(t, owner, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"openai:sol", "anthropic:opus"}, "not": []string{}, "revision": 0}, ""), 200)
			boardDecode[boardWriteResult](t, boardCall(t, owner, "PUT", "/api/model-preferences/orders/other/first/thinking?for=default", map[string]any{"effort": "xhigh", "revision": 1}, ""), 200)
			var until time.Time
			inRegistry(t, owner, func(tx pgx.Tx) error {
				now, err := dbNow(t.Context(), tx)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=$3,share_usage=false WHERE id=ANY($1::uuid[])`, []string{account, fallback}, owner.ID, now); err != nil {
					return err
				}
				if cause == "allowance" {
					until = now.Add(24 * time.Hour)
					_, err = tx.Exec(t.Context(), `UPDATE account_allowance_windows SET used=allowance,ends_at=$2 WHERE account_id=$1`, account, until)
					return err
				}
				// Unknown quota with the next band two days away keeps this
				// fixture outside working hours throughout all HTTP reads.
				until = time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day()+2, 8, 0, 0, 0, time.UTC)
				schedule := capacity.DefaultSchedule("UTC")
				schedule.Reserve, schedule.OffDays = capacity.ReserveOff, "rest"
				for i := range schedule.Week {
					schedule.Week[i].On = i == int(until.Weekday()+6)%7
				}
				raw, err := json.Marshal(schedule)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET removed_at=$2 WHERE account_id=$1`, account, now); err != nil {
					return err
				}
				_, err = tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=$2 WHERE account_id=$1`, account, raw)
				return err
			})
			detail := "Codex account is at its allowance or floor until " + until.UTC().Format(time.RFC3339)
			if cause == "schedule" {
				detail = "Codex account is outside its scheduled hours until " + until.Format(time.RFC3339)
			}
			for _, phase := range []struct {
				name  string
				share bool
			}{{"private", false}, {"shared", true}, {"revoked", false}} {
				inRegistry(t, owner, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=$2 WHERE id=$1`, account, phase.share)
					return err
				})
				for _, reader := range []tenant.Principal{owner, member, admin, agent} {
					t.Run(phase.name+"/"+reader.Name, func(t *testing.T) {
						want := "Codex account cannot take new work right now"
						if reader.ID == owner.ID || phase.share {
							want = detail
						}
						resolved := decode[Resolution](t, &reader, "GET", "/api/models/resolve?role=build-hard", "", 200)
						if resolved.Profile == nil || resolved.Profile.ID != opus.ID || resolved.OwnerRequired {
							t.Fatalf("privacy changed the eligible fallback: %+v", resolved)
						}
						found := false
						for _, candidate := range resolved.Ladder {
							if candidate.ProfileID == sol.ID {
								found = true
								if candidate.Selected || !slices.Equal(candidate.SkipReasons, []string{want}) {
									t.Errorf("resolver reason=%v selected=%v, want one %q", candidate.SkipReasons, candidate.Selected, want)
								}
							}
						}
						if !found {
							t.Fatal("resolver omitted the blocked model")
						}
						simple := decode[simpleDocument](t, &reader, "GET", "/api/model-preferences/simple?for=default", "", 200)
						found = false
						for _, row := range simple.Unavailable {
							if row.Column == "other" && row.Line == "openai:sol" {
								found = true
								if row.Reason != want || row.RunsInstead == nil || *row.RunsInstead != "anthropic:opus" {
									t.Errorf("simple reason=%q fallback=%v, want %q and Claude", row.Reason, row.RunsInstead, want)
								}
							}
						}
						if !found {
							t.Fatal("simple view omitted the blocked model")
						}
					})
				}
			}
		})
	}
}

// Risk (AEON-1146 / AEON-990): missing quota stops every harness, or a real
// vendor limit suppresses a healthy sibling and repeats once per effort/stage.
func TestModelAllowanceUnknownAndMeasuredLimit(t *testing.T) {
	admin, _ := boardFixture(t)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	opus := profileBySlug(profiles, "claude-opus-xhigh")
	sol := profileBySlug(profiles, "codex-6-1-sol-xhigh")
	runner := addPrincipal(t, admin.TenantID, "agent", "Allowance runner", nil)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"anthropic:opus", "openai:sol"}, "not": []string{}, "revision": 0}, ""), 200)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first/thinking?for=default", map[string]any{"effort": "xhigh", "revision": 1}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		reset := now.Add(time.Hour)
		schedule := capacity.DefaultSchedule("UTC")
		schedule.Override, schedule.OverrideUntil, schedule.Reserve = "sprint", &reset, capacity.ReserveOff
		raw, err := json.Marshal(schedule)
		if err != nil {
			return err
		}
		ids := map[string]string{}
		for _, h := range []string{"claude", "codex"} {
			profileID := opus.ID
			if h == "codex" {
				profileID = sol.ID
			}
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,state,last_probe_at,last_probe_ok,last_daemon_generation,owner_person_id,linked_at,allowed_model_profile_ids) VALUES($1,$2,$2,'fixture',$3,$2,'available',$4,true,'fixture',$5,$7,ARRAY[$6::uuid]) RETURNING id::text`, admin.TenantID, h, runner.ID, now, admin.ID, profileID, now.Add(-2*time.Minute)).Scan(&id); err != nil {
				return err
			}
			ids[h] = id
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, admin.TenantID, admin.ID, id, raw); err != nil {
				return err
			}
		}
		query := WorkQuery{Role: "build", Column: "other", Area: "other", Situation: "first", WorkspaceOnly: true, ExplicitBoard: true}
		ctx := tenant.WithPrincipal(t.Context(), admin)
		unknown, err := resolveBoardWork(ctx, tx, admin, query, now, nil)
		if err != nil {
			return err
		}
		if unknown == nil || unknown.Profile == nil || unknown.Profile.ID != opus.ID || len(unknown.Ladder[0].SkipReasons) != 0 {
			t.Fatalf("unknown quota must select top Claude profile: %+v", unknown)
		}
		used := 14.0
		for _, amount := range []float64{used, 100} {
			readAt := now
			if amount < 100 {
				readAt = now.Add(-time.Minute)
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','',10080,$3,$4,$5,'harness')`, admin.TenantID, ids["claude"], amount, reset, readAt); err != nil {
				return err
			}
			out, err := resolveBoardWork(ctx, tx, admin, query, now, nil)
			if err != nil {
				return err
			}
			if amount < 100 {
				if out.Profile == nil || out.Profile.ID != opus.ID {
					t.Fatalf("under-limit Claude must be eligible: %+v", out)
				}
			} else {
				if out.Profile == nil || out.Profile.ID != sol.ID {
					t.Fatalf("only exhausted account's routes must be skipped: %+v", out)
				}
				reasons := []string{}
				for _, step := range out.Ladder {
					if step.ProfileID == opus.ID {
						reasons = append(reasons, step.SkipReasons...)
					}
				}
				text := joinReasons(reasons)
				if !strings.Contains(text, "Claude account reported a vendor limit until "+reset.UTC().Format(time.RFC3339)) || strings.Contains(text, ";") {
					t.Fatalf("one human sentence with reset required: %q", text)
				}
			}
		}
		return nil
	})
}
