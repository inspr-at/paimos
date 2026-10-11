// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: saved Default exclusions turn every inheriting kind into an exception,
// while filtering them could also hide an owned row or a row-specific rule.
func TestModelsV2InheritedDefaultExclusionsAreNotExceptions(t *testing.T) {
	for _, tc := range []struct {
		name, writeTarget, readTarget string
	}{
		{"workspace", "default", "default"},
		{"inherited_workspace", "default", "me"},
		{"personal", "me", "me"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin, member := boardFixture(t)
			writer, person := admin, ""
			excluded := "anthropic:opus"
			if tc.writeTarget == "me" {
				// Keep different workspace and personal exclusions so the read
				// must compare with the Default for the requested scope.
				boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"openai:sol"}, "not": []string{excluded}, "revision": 0}, ""), 200)
				writer, person, excluded = member, member.ID, "anthropic:sonnet"
			}
			body := map[string]any{"rank": []string{"openai:sol"}, "not": []string{}, "efforts": map[string]string{"openai:sol": "xhigh"}, "end": "wait", "revision": 0}
			write := func(column string) boardWriteResult {
				t.Helper()
				return boardDecode[boardWriteResult](t, boardCall(t, writer, "PUT", "/api/model-preferences/orders/"+column+"/first?for="+tc.writeTarget, body, person), 200)
			}
			read := func() simpleDocument {
				t.Helper()
				return boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple?for="+tc.readTarget, nil, ""), 200)
			}
			saved := write("other")
			baseline := read()
			if baseline.All.Line == nil || *baseline.All.Line != "openai:sol" || len(baseline.All.Never) != 0 || len(baseline.Exceptions) != 0 {
				t.Fatalf("unexpected Default without exclusions: %+v", baseline)
			}
			body["not"], body["revision"] = []string{excluded}, saved.Revision
			saved = write("other")
			assertInherited := func() {
				t.Helper()
				doc := read()
				if len(doc.All.Never) != 1 || doc.All.Never[0].Line != excluded || doc.All.Never[0].Why != "Saved exclusion" {
					t.Fatalf("Default exclusion lost or from the wrong scope: %+v", doc.All.Never)
				}
				if len(doc.Exceptions) != 0 {
					t.Fatalf("kinds inheriting Default became exceptions: %+v", doc.Exceptions)
				}
			}
			assertInherited()
			// Current page writes omit the legacy not field and preserve it.
			delete(body, "not")
			body["revision"] = saved.Revision
			saved = write("other")
			assertInherited()
			boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first/never/openai:astra", map[string]any{"why": "Backend exclusion", "revision": 0}, ""), 200)
			doc := read()
			if len(doc.Exceptions) != 1 || doc.Exceptions[0].Column != "backend" || !sameSimplePick(doc.Exceptions[0].simplePick, doc.All.simplePick) || !slices.ContainsFunc(doc.Exceptions[0].Never, func(n simpleNever) bool { return n.Line == "openai:astra" && n.Why == "Backend exclusion" }) || !slices.ContainsFunc(doc.Exceptions[0].Never, func(n simpleNever) bool { return n.Line == excluded }) {
				t.Fatalf("row-specific exclusion or inherited Default lost: %+v", doc.Exceptions)
			}
			// An explicitly owned row stays visible even when its effective
			// pick, chain and exclusions exactly match Default.
			body["not"], body["revision"] = []string{excluded}, saved.Revision
			write("frontend")
			doc = read()
			if len(doc.Exceptions) != 2 || !slices.ContainsFunc(doc.Exceptions, func(row simpleRow) bool {
				return row.Column == "frontend" && row.Mine == (tc.writeTarget == "me") && sameSimplePick(row.simplePick, doc.All.simplePick) && reflect.DeepEqual(row.Chain, doc.All.Chain) && reflect.DeepEqual(row.Never, doc.All.Never)
			}) {
				t.Fatalf("explicit row matching Default was hidden: %+v", doc.Exceptions)
			}
		})
	}
}

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
	for _, bad := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"rank": rank, "efforts": map[string]string{"openai:sol": "invented"}, "end": "wait", "revision": revision}, "effort_not_registered"},
		{map[string]any{"rank": rank, "efforts": map[string]string{"openai:luna": "high"}, "end": "wait", "revision": revision}, "invalid_effort"},
		{map[string]any{"rank": rank, "end": "row", "revision": revision}, "invalid_end"},
	} {
		boardError(t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", bad.body, ""), 422, bad.code)
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
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"openai:sol"}, "efforts": map[string]string{"openai:sol": "xhigh"}, "end": "wait", "revision": 0}, ""), 200)
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
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend'`).Scan(&kind); err != nil {
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

// Risk: native thinking and ranked order edits erase one another, an empty
// v2 map retains a hidden legacy override, or reset still reports "yours".
func TestModelsV2OrderNativeEffortCompatibility(t *testing.T) {
	admin, member := boardFixture(t)
	path := "/api/model-preferences/orders/backend/first?for=default"
	rank := []string{"openai:sol", "anthropic:opus"}
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", path, map[string]any{"rank": rank, "efforts": map[string]string{"openai:sol": "xhigh", "anthropic:opus": "xhigh"}, "end": "default", "revision": 0}, ""), 200)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first/thinking?for=default", map[string]any{"effort": "high", "revision": 1}, ""), 200)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", path, map[string]any{"rank": rank, "revision": 2}, ""), 200)
	readOrder := func(person *string) modelprefs.BoardOrder {
		t.Helper()
		var result modelprefs.BoardOrder
		inRegistry(t, admin, func(tx pgx.Tx) error {
			s, err := modelprefs.LoadBoard(t.Context(), tx, person, "")
			if err != nil {
				return err
			}
			profile := s.Workspace.ID
			if person != nil && s.Person != nil {
				profile = s.Person.ID
			}
			for _, o := range s.Orders {
				if o.ProfileID == profile && o.Column == "backend" && o.Situation == "first" {
					result = o
					return nil
				}
			}
			t.Fatal("saved order missing")
			return nil
		})
		return result
	}
	if o := readOrder(nil); o.Efforts["openai:sol"] != "high" || o.Efforts["anthropic:opus"] != "xhigh" || o.End != "default" {
		t.Fatalf("rewrite lost native settings: %+v", o)
	}
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", path, map[string]any{"rank": rank, "efforts": map[string]string{}, "revision": 3}, ""), 200)
	if o := readOrder(nil); len(o.Efforts) != 0 || o.Effort != nil || o.EffortLevel != nil {
		t.Fatalf("automatic native level kept a legacy override: %+v", o)
	}
	for field, code := range map[string]string{"efforts": "invalid_effort", "end": "invalid_end"} {
		boardError(t, boardCall(t, admin, "PUT", path, map[string]any{"rank": rank, "revision": 4, field: nil}, ""), 422, code)
	}
	boardError(t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/small?for=default", map[string]any{"rank": rank, "end": "default", "revision": 4}, ""), 422, "invalid_end")
	personal := "/api/model-preferences/orders/backend/first?for=me"
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", personal, map[string]any{"rank": rank, "end": "wait", "revision": 0}, member.ID), 200)
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/backend/first/thinking?for=me", map[string]any{"thinking": "deep", "effort": "high", "revision": 1}, member.ID), 200)
	boardDecode[boardWriteResult](t, boardCall(t, member, "DELETE", personal+"&revision=2", nil, member.ID), 200)
	if o := readOrder(&member.ID); o.Thinking == nil || *o.Thinking != "deep" || o.Rank != nil || o.Effort != nil || o.End != "" || len(o.Efforts) != 0 {
		t.Fatalf("rank reset altered thinking or retained a chain: %+v", o)
	}
	doc := boardDecode[simpleDocument](t, boardCall(t, member, "GET", "/api/model-preferences/simple", nil, ""), 200)
	for _, row := range doc.Exceptions {
		if row.Column == "backend" && row.Mine {
			t.Fatal("thinking-only row still reports a personal order")
		}
	}
}

// Risk: same-line route fallback hides its pause cause or the route that runs, or Quick jobs reports
// the normal row's strong model instead of the resolver's fast-tier choice.
func TestModelsV2SameLineFallbackAndQuickJobs(t *testing.T) {
	admin, _ := boardFixture(t)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	sol := profileBySlug(profiles, "codex-6-1-sol-xhigh")
	minimalAccount(t, admin, sol)
	luna := profileBySlug(profiles, "codex-luna-medium")
	minimalAccount(t, admin, luna)
	cursor := decode[Profile](t, &admin, "POST", "/api/models", `{"slug":"cursor-sol-contract","version":"1","harness":"cursor","family":"openai","model":"gpt-6.1-sol","effort":"xhigh","tier":"strong"}`, 201)
	minimalAccount(t, admin, cursor)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"openai:sol"}, "efforts": map[string]string{"openai:sol": "xhigh"}, "end": "wait", "revision": 0}, ""), 200)
	boardDecode[modelPauseResult](t, boardCall(t, admin, "PUT", "/api/models/lines/codex/"+sol.Model+"/pause", map[string]any{"until": nil, "reason": "Use the sibling route"}, ""), 200)
	doc := boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
	if len(doc.Unavailable) != 1 || doc.Unavailable[0].Cause != "paused" || doc.Unavailable[0].Waits || doc.Unavailable[0].RunsInstead == nil || *doc.Unavailable[0].RunsInstead != "openai:sol" || doc.Unavailable[0].Route == nil || *doc.Unavailable[0].Route != "codex" {
		t.Fatalf("same-line sibling route hid the pause: %+v", doc.Unavailable)
	}
	// The page says "runs through Cursor instead" only from the route that actually runs.
	if via := doc.Unavailable[0].RunsVia; via == nil || *via != "cursor" {
		t.Fatalf("same-line fallback did not name the route that runs: %+v", doc.Unavailable[0])
	}
	if doc.Quick.Line == nil || *doc.Quick.Line != "openai:luna" || doc.Quick.Effort == nil || *doc.Quick.Effort != "medium" || doc.Quick.Harness == nil || *doc.Quick.Harness != "codex" || !slices.ContainsFunc(doc.Quick.Trace, func(trace simpleTrace) bool { return trace.Selected && trace.Line == "openai:luna" }) {
		t.Fatalf("Quick jobs differs from dispatch: %+v", doc.Quick)
	}
}

// Risk: a known residency rejection is called a missing account, or an absent
// account is falsely described as a residency rejection.
func TestModelsV2ResidencyCauseMatchesEvidence(t *testing.T) {
	admin, _ := boardFixture(t)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	sol := profileBySlug(profiles, "codex-6-1-sol-xhigh")
	account := minimalAccount(t, admin, sol)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"openai:sol"}, "efforts": map[string]string{"openai:sol": "xhigh"}, "end": "wait", "revision": 0}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_pref_profiles SET residency='eu' WHERE scope='workspace'`)
		return err
	})
	read := func(cause string) {
		t.Helper()
		doc := boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
		if len(doc.Unavailable) != 1 || doc.Unavailable[0].Cause != cause || !doc.Unavailable[0].Waits || doc.Unavailable[0].Until != nil || doc.Unavailable[0].RunsVia != nil {
			t.Fatalf("cause differs from qualification: %+v", doc.Unavailable)
		}
	}
	read("residency")
	luna := profileBySlug(profiles, "codex-luna-medium")
	inRegistry(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET allowed_model_profile_ids=ARRAY[$2::uuid] WHERE id=$1`, account, luna.ID)
		return err
	})
	read("no_account")
}

// Risk: canonical page line ids or a sibling harness's native effort are
// rejected by writes that still validate only the legacy harness grouping.
func TestModelsV2CrossHarnessNativeWrites(t *testing.T) {
	admin, _ := boardFixture(t)
	decode[Profile](t, &admin, "POST", "/api/models", `{"slug":"cursor-sol-ultra-contract","version":"1","harness":"cursor","family":"openai","model":"gpt-6.1-sol","effort":"ultra","tier":"strong"}`, 201)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", map[string]any{"rank": []string{"openai:sol"}, "efforts": map[string]string{"openai:sol": "ultra"}, "revision": 0}, ""), 200)
	decode[Profile](t, &admin, "POST", "/api/models", `{"slug":"cursor-nova-contract","version":"1","harness":"cursor","family":"openai","model":"gpt-7-nova","effort":"xhigh","tier":"strong"}`, 201)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/frontend/first?for=default", map[string]any{"rank": []string{"openai:nova"}, "efforts": map[string]string{"openai:nova": "xhigh"}, "revision": 1}, ""), 200)
	boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first/never/openai:nova", map[string]any{"why": "Canonical identity", "revision": 0}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		s, err := modelprefs.LoadBoard(t.Context(), tx, nil, "")
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(s.Orders, func(o modelprefs.BoardOrder) bool { return o.Column == "backend" && o.Efforts["openai:sol"] == "ultra" }) || !slices.ContainsFunc(s.Orders, func(o modelprefs.BoardOrder) bool {
			return o.Column == "frontend" && slices.Equal(o.Rank, []string{"openai:nova"})
		}) || !slices.ContainsFunc(s.Rules, func(rule modelprefs.Rule) bool {
			return rule.Column == "backend" && rule.Line == "openai:nova" && rule.Lock == "not"
		}) {
			t.Fatal("canonical write lost its native effort, order or rule")
		}
		return nil
	})
}

// Risk: pausing one Cursor native id leaves sibling levels running, or expands
// the pause into a different version/fast route.
func TestModelsV2CursorPauseCoversNativeIDs(t *testing.T) {
	admin, _ := boardFixture(t)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	profile := profileBySlug(profiles, "cursor-grok")
	if profile.ID == "" || profile.Model != "grok-4.7-high" {
		t.Fatal("fixture lacks the Cursor native high route")
	}
	path := "/api/models/lines/cursor/" + profile.Model + "/pause"
	boardDecode[modelPauseResult](t, boardCall(t, admin, "PUT", path, map[string]any{"until": nil, "reason": "Native route maintenance"}, ""), 200)
	check := func(paused bool) {
		t.Helper()
		got := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
		matched, separate := 0, 0
		for _, p := range got {
			line, version := modelsLine(p)
			if p.Harness != "cursor" || p.Family != "xai" || version != "4.7" {
				continue
			}
			if line == "xai:grok" {
				matched++
				if (p.Pause != nil) != paused {
					t.Fatalf("native level pause differs: %+v", p)
				}
			} else if line == "xai:grok-fast" {
				separate++
				if p.Pause != nil {
					t.Fatal("pause crossed into the fast route")
				}
			}
		}
		if matched < 2 || separate < 1 {
			t.Fatal("fixture lacks native levels or separate route")
		}
		inRegistry(t, admin, func(tx pgx.Tx) error {
			for _, p := range got {
				line, version := modelsLine(p)
				if p.Harness != "cursor" || line != "xai:grok" || version != "4.7" {
					continue
				}
				step, err := preferenceStep(t.Context(), tx, p, "build")
				if err != nil {
					return err
				}
				if (step.State != "available") != paused {
					t.Fatalf("dispatch differs from pause read: %+v", step)
				}
			}
			return nil
		})
	}
	check(true)
	boardDecode[modelPauseResult](t, boardCall(t, admin, "DELETE", path, nil, ""), 200)
	check(false)
}

// Risk: a pause, the successor of a ladder hold, commits for an admin whose
// grant was revoked while it waited on the tenant fence (code health rule 1;
// moved here from the retired ladder editor's revocation case).
func TestModelsV2PauseRechecksGrantRevokedUnderFence(t *testing.T) {
	admin, _ := boardFixture(t)
	sol := profileBySlug(decode[[]Profile](t, &admin, "GET", "/api/models", "", 200), "codex-6-1-sol-xhigh")
	before := eventCount(t, admin, "model.pause_changed")
	var clocked atomic.Int32
	h := newEditorHTTP(t, &Module{pool: appPool, validationClock: func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
		clocked.Add(1)
		return validationNow(ctx, tx)
	}})
	held, release, fenced := make(chan int, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		fenced <- db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
			if err := preferenceFence(t.Context(), tx, admin); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, admin.ID); err != nil {
				return err
			}
			var pid int
			if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			held <- pid
			<-release
			return nil
		})
	}()
	var pid int
	select {
	case pid = <-held:
	case err := <-fenced:
		t.Fatal("fence holder failed", err)
	case <-time.After(10 * time.Second):
		t.Fatal("fence holder did not take the tenant fence")
	}
	done := startEditorRequest(t, h, admin, "PUT", "/api/models/lines/codex/"+sol.Model+"/pause", `{"until":null,"reason":"Planned maintenance"}`, nil)
	waitEditorBlocked(t, pid)
	// The pause itself waits at the tenant fence, past every earlier check.
	var waiting string
	if err := adminPool.QueryRow(t.Context(), `SELECT query FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting != db.TenantFenceSQL {
		t.Fatalf("the pause did not wait at the tenant fence: %q", waiting)
	}
	close(release)
	if err := <-fenced; err != nil {
		t.Fatal(err)
	}
	w := finishEditorRequest(t, done)
	if w.Code != 403 || clocked.Load() != 0 {
		t.Fatalf("revoked pause answered %d after %d clock reads: %s", w.Code, clocked.Load(), w.Body.String())
	}
	if eventCount(t, admin, "model.pause_changed") != before {
		t.Fatal("revoked pause emitted an event")
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var stored int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_pauses`).Scan(&stored); err != nil {
			return err
		}
		if stored != 0 {
			t.Fatalf("revoked pause stored %d rows", stored)
		}
		return nil
	})
}
