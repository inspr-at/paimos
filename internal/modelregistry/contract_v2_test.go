// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: the page saves a chain or native level that dispatch does not read,
// personal writes alter workspace policy, or failed writes emit success/audit.
func TestModelsV2PageContractWritesAndReads(t *testing.T) {
	admin, member := boardFixture(t)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	sol := profileBySlug(profiles, "codex-6-1-sol-xhigh")
	minimalAccount(t, admin, sol)
	write := func(column, situation string, revision int64, rank []string, efforts map[string]string, end string) boardWriteResult {
		t.Helper()
		return boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/"+column+"/"+situation+"?for=default", map[string]any{"rank": rank, "efforts": efforts, "end": end, "revision": revision}, ""), 200)
	}
	read := func(pickDefault bool) simpleDocument {
		t.Helper()
		path := "/api/model-preferences/simple"
		if pickDefault {
			path += "?for=default"
		}
		return boardDecode[simpleDocument](t, boardCall(t, member, "GET", path, nil, ""), 200)
	}
	findRow := func(doc simpleDocument, column string) simpleRow {
		t.Helper()
		for _, row := range doc.Exceptions {
			if row.Column == column {
				return row
			}
		}
		t.Fatalf("missing row %s: %+v", column, doc.Exceptions)
		return simpleRow{}
	}
	write("other", "first", 0, []string{"openai:sol"}, map[string]string{"openai:sol": "xhigh"}, "wait")
	rank := []string{"anthropic:opus", "openai:sol"}
	efforts := map[string]string{"anthropic:opus": "xhigh", "openai:sol": "high"}
	write("backend", "first", 1, rank, efforts, "default")
	doc := read(true)
	row := findRow(doc, "backend")
	if row.Line == nil || *row.Line != rank[0] || row.Effort == nil || *row.Effort != "xhigh" || row.End != "default" || len(row.Chain) != 1 || row.Chain[0].Line != rank[1] || row.Chain[0].Effort == nil || *row.Chain[0].Effort != "high" || row.Never == nil || doc.Situations == nil || doc.Quick.Trace == nil || doc.Outdated.Named == nil {
		t.Fatalf("incomplete v2 row/document: %+v %+v", row, doc)
	}
	// Both new situations persist in the additive situation table and appear
	// only in the workspace matrix. Existing fix/stuck remain supported.
	revision := int64(2)
	for _, situation := range []string{"small", "fix", "stuck", "review"} {
		end := "row"
		if situation == "review" {
			end = "auto"
		}
		result := write("backend", situation, revision, rank, map[string]string{"anthropic:opus": "xhigh", "openai:sol": "xhigh"}, end)
		revision = result.Revision
		cells := read(true).Situations
		cell := (*cells)["backend"][situation]
		if cell.Line == nil || *cell.Line != rank[0] || len(cell.Chain) != 1 || cell.End != end {
			t.Fatalf("situation %s lost: %+v", situation, cell)
		}
		boardDecode[boardWriteResult](t, boardCall(t, admin, "DELETE", "/api/model-preferences/orders/backend/"+situation+"?for=default&revision="+strconv.FormatInt(revision, 10), nil, ""), 200)
		revision++
		if _, present := (*read(true).Situations)["backend"][situation]; present {
			t.Fatalf("reset kept situation %s", situation)
		}
	}
	if read(false).Situations != nil {
		t.Fatal("personal read exposed the workspace matrix")
	}
	personal := map[string]any{"rank": []string{"openai:sol", "anthropic:opus"}, "efforts": map[string]string{"openai:sol": "high", "anthropic:opus": "xhigh"}, "end": "wait", "revision": 0}
	path := "/api/model-preferences/orders/backend/first?for=me"
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", path, personal, member.ID), 200)
	if mine := findRow(read(false), "backend"); !mine.Mine || mine.Line == nil || *mine.Line != "openai:sol" || mine.End != "wait" || read(true).Revision != revision {
		t.Fatalf("personal order changed the workspace: %+v", mine)
	}
	boardDecode[boardWriteResult](t, boardCall(t, member, "DELETE", path+"&revision=1", nil, member.ID), 200)
	if findRow(read(false), "backend").Mine {
		t.Fatal("reset retained personal ownership")
	}
	neverPath := "/api/model-preferences/orders/backend/first/never/openai:sol"
	boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", neverPath, map[string]any{"why": "Independent route required", "revision": 0}, ""), 200)
	row = findRow(read(false), "backend")
	if len(row.Never) != 1 || row.Never[0].By == nil || *row.Never[0].By != admin.ID || row.Never[0].At == nil || row.Never[0].Why != "Independent route required" || row.Never[0].Line != "openai:sol" {
		t.Fatalf("never attribution lost: %+v", row.Never)
	}
	beforeEvents := eventCount(t, admin, "model.rules_changed")
	boardError(t, boardCall(t, admin, "PUT", neverPath, map[string]any{"why": "stale", "revision": 0}, ""), 409, "stale_revision")
	if beforeEvents != eventCount(t, admin, "model.rules_changed") {
		t.Fatal("stale never write emitted an event")
	}
	boardDecode[map[string]any](t, boardCall(t, member, "PUT", neverPath, map[string]any{"why": "No authority", "revision": 1}, ""), 403)
	boardDecode[rulesDocument](t, boardCall(t, admin, "DELETE", neverPath+"?revision=1", nil, ""), 200)
	if len(findRow(read(false), "backend").Never) != 0 {
		t.Fatal("never reset retained the prohibition")
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_rules(tenant_id,scope,column_key,line,lock,why,set_by) VALUES($1,'workspace','backend','anthropic:opus','top','Fixed workspace choice',$2)`, admin.TenantID, admin.ID)
		return err
	})
	personal["revision"] = 2
	boardError(t, boardCall(t, member, "PUT", path, personal, member.ID), 423, "column_locked")
	boardError(t, boardCall(t, member, "DELETE", path+"&revision=2", nil, member.ID), 423, "column_locked")
	locked := findRow(read(false), "backend")
	if locked.Lock == nil || locked.Mine || locked.Lock.By == nil || *locked.Lock.By != admin.ID || locked.Line == nil || *locked.Line != "anthropic:opus" {
		t.Fatalf("workspace lock lost: %+v", locked)
	}
	for _, bad := range []map[string]any{
		{"rank": rank, "efforts": map[string]string{"openai:sol": "invented"}, "end": "wait", "revision": revision},
		{"rank": rank, "efforts": map[string]string{"openai:luna": "high"}, "end": "wait", "revision": revision},
		{"rank": rank, "end": "row", "revision": revision},
	} {
		boardDecode[map[string]any](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", bad, ""), 422)
		if read(true).Revision != revision {
			t.Fatal("failed order advanced its revision")
		}
	}
	boardError(t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/review?for=default", map[string]any{"rank": []string{"openai:luna"}, "end": "wait", "revision": revision}, ""), 422, "review_floor")
	// Read the persisted row, not only the serialized response.
	inRegistry(t, admin, func(tx pgx.Tx) error {
		state, err := modelprefs.LoadBoard(t.Context(), tx, nil, "")
		if err != nil {
			return err
		}
		found := false
		for _, order := range state.Orders {
			if order.Column == "backend" && order.Situation == "first" {
				found = true
				if !slices.Equal(order.Rank, rank) || !reflect.DeepEqual(order.Efforts, efforts) || order.End != "default" {
					t.Fatalf("persisted order lost: %+v", order)
				}
			}
		}
		if !found {
			t.Fatal("persisted order missing")
		}
		return nil
	})
}

// Risk: a pause leaves another native level running, expiry blocks forever,
// or a member/agent obtains workspace write authority.
func TestModelsV2PauseLifecycleAndAuthority(t *testing.T) {
	admin, member := boardFixture(t)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	sol := profileBySlug(profiles, "codex-6-1-sol-xhigh")
	minimalAccount(t, admin, sol)
	path := "/api/models/lines/codex/" + sol.Model + "/pause"
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	input := map[string]any{"until": until, "reason": "Planned maintenance"}
	boardDecode[map[string]any](t, boardCall(t, member, "PUT", path, input, ""), 403)
	agent := admin
	agent.Kind = tenant.Agent
	boardError(t, boardCall(t, agent, "PUT", path, input, ""), 403, "person_required")
	saved := boardDecode[modelPauseResult](t, boardCall(t, admin, "PUT", path, input, ""), 200)
	if saved.Pause == nil || saved.Pause.Until == nil || !saved.Pause.Until.Equal(until) || saved.Pause.By == nil || *saved.Pause.By != admin.ID {
		t.Fatalf("pause response incomplete: %+v", saved)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		for _, profile := range profiles {
			if profile.Harness != sol.Harness || profile.Model != sol.Model {
				continue
			}
			step, err := preferenceStep(t.Context(), tx, profile, "build")
			if err != nil {
				return err
			}
			role, _ := roleByName("build")
			if len(skipReasons(step, role, resolveQuery{Role: "build"}, until.Add(-time.Microsecond), nil)) == 0 || len(skipReasons(step, role, resolveQuery{Role: "build"}, until, nil)) != 0 {
				t.Fatalf("pause boundary differs by native level: %+v", step)
			}
		}
		return nil
	})
	input["until"] = nil
	boardDecode[modelPauseResult](t, boardCall(t, admin, "PUT", path, input, ""), 200)
	got := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	for _, profile := range got {
		if profile.Harness == sol.Harness && profile.Model == sol.Model && (profile.Pause == nil || profile.Pause.Until != nil || profile.Pause.Reason != input["reason"]) {
			t.Fatalf("catalog hid indefinite pause: %+v", profile)
		}
	}
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"openai:sol"}, "end": "wait", "revision": 0}, ""), 200)
	doc := boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
	if len(doc.Unavailable) != 1 || doc.Unavailable[0].Cause != "paused" || !doc.Unavailable[0].Waits || doc.Unavailable[0].Route == nil || *doc.Unavailable[0].Route != sol.Harness || doc.Unavailable[0].Until != nil {
		t.Fatalf("pause cause duplicated/lost: %+v", doc.Unavailable)
	}
	boardDecode[modelPauseResult](t, boardCall(t, admin, "DELETE", path, nil, ""), 200)
	doc = boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
	if len(doc.Unavailable) != 0 {
		t.Fatalf("resume did not restore availability: %+v", doc.Unavailable)
	}
	before := eventCount(t, admin, "model.pause_changed")
	boardError(t, boardCall(t, admin, "PUT", path, map[string]any{"until": "2000-01-01T00:00:00Z", "reason": "Past"}, ""), 422, "pause_until_must_be_future")
	if before != eventCount(t, admin, "model.pause_changed") {
		t.Fatal("invalid pause emitted an event")
	}
	raw, err := json.Marshal(doc)
	if err != nil || !json.Valid(raw) {
		t.Fatal("invalid simple document", err)
	}
}

// Risk: different profiles/native levels repeat a cause, or private generic
// evidence is turned into a daily cap/reset that the reader cannot see.
func TestModelsV2UnavailableDistinctCauses(t *testing.T) {
	line, harness := "openai:sol", "codex"
	c := boardCatalog{profiles: map[string][]Profile{line: {{ID: "a", Harness: harness}, {ID: "b", Harness: harness}}}}
	resolved := &WorkResolution{Resolution: Resolution{Ladder: []Candidate{
		{ProfileID: "a", SkipReasons: []string{"Codex account is at its daily cap until 2026-10-12T04:00:00Z", "No tools in PAIMOS", "paused: maintenance", "retired", "not allowed: eu", "no qualified account with available capacity"}},
		{ProfileID: "b", SkipReasons: []string{"Codex account is at its daily cap until 2026-10-12T04:00:00Z", "No tools in PAIMOS"}},
	}}}
	causes := simpleCausesFor("backend", line, simplePick{}, resolved, c)
	if len(causes) != 6 {
		t.Fatalf("want one row per cause: %+v", causes)
	}
	seen := map[string]bool{}
	for _, cause := range causes {
		if seen[cause.Cause] || !cause.Waits || cause.Route == nil || *cause.Route != harness {
			t.Fatalf("repeated or incomplete cause: %+v", cause)
		}
		seen[cause.Cause] = true
		if cause.Cause == "daily_cap" && (cause.Until == nil || cause.Until.Format(time.RFC3339) != "2026-10-12T04:00:00Z") {
			t.Fatal("named reset lost")
		}
	}
	cause, until := simpleCause("Codex account cannot take new work right now")
	if cause != "no_account" || until != nil {
		t.Fatal("private generic evidence became a metric")
	}
}

// Risk: the summary reports no uses despite live immutable pins, counts native
// levels as separate versions, or leaks another person's pin usage.
func TestModelsV2OutdatedCountsVisiblePins(t *testing.T) {
	admin, member := boardFixture(t)
	old := decode[Profile](t, &admin, "POST", "/api/models", `{"slug":"older-sol-contract","version":"1","harness":"codex","family":"openai","model":"gpt-5-sol","effort":"xhigh","tier":"frontier"}`, 201)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE slug='ticket'`).Scan(&kind); err != nil {
			return err
		}
		for _, scope := range []modelprefs.Scope{{Level: "default"}, {Level: "person", PersonID: &member.ID}} {
			saved, err := modelprefs.SaveScopeOnly(t.Context(), tx, admin, scope)
			if err != nil {
				return err
			}
			cells := map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: old.ID}}
			if scope.Level == "person" {
				cells["complex"] = modelprefs.Cell{Mode: "pinned", ProfileID: old.ID}
			}
			if err := modelprefs.PutRow(t.Context(), tx, admin, saved, kind, modelprefs.Row{Cells: cells}); err != nil {
				return err
			}
		}
		return nil
	})
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	for _, reader := range []struct {
		principal tenant.Principal
		uses      int
	}{{admin, 1}, {member, 3}} {
		doc := boardDecode[simpleDocument](t, boardCall(t, reader.principal, "GET", "/api/model-preferences/simple", nil, ""), 200)
		if doc.Outdated.Count != len(doc.Outdated.Named) || doc.Outdated.Uses != reader.uses {
			t.Fatalf("summary differs from visible pins: %+v", doc.Outdated)
		}
		found := false
		for _, name := range doc.Outdated.Named {
			if name.Line != old.ID {
				continue
			}
			found = true
			if !slices.ContainsFunc(profiles, func(p Profile) bool {
				line, version := modelsLine(p)
				return p.ID == name.Successor && line == "openai:sol" && CompareModelVersions(version, "5") > 0
			}) {
				t.Fatalf("successor is not a newer registered version: %+v", name)
			}
		}
		if !found {
			t.Fatal("older registered version missing")
		}
	}
}
