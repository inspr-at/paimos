// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// boardColumnOf reads one column of the ranked board as a layer shows it.
func boardColumnOf(t *testing.T, p tenant.Principal, layer, column string) boardColumn {
	t.Helper()
	doc := boardDecode[boardDocument](t, boardCall(t, p, "GET", "/api/model-preferences/board?layer="+layer, nil, ""), 200)
	for _, col := range doc.Columns {
		if col.Column == column {
			return col
		}
	}
	t.Fatalf("missing board column %s", column)
	return boardColumn{}
}

// Risk: a reset deletes an order's ending and fallback levels, and the page's
// Undo cannot write them back because the board reports only rank and
// exclusions (AEON-1161).
func TestModelsV2BoardStoredOrderCarriesEndingAndLevels(t *testing.T) {
	admin, member := boardFixture(t)
	rank := []string{"openai:sol", "anthropic:opus", "anthropic:fable"}
	efforts := map[string]string{"openai:sol": "high", "anthropic:opus": "xhigh", "anthropic:fable": "high"}
	saved := boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/backend/first?for=me", map[string]any{"rank": rank, "efforts": efforts, "end": "wait", "revision": 0}, member.ID), 200)
	if mine := boardColumnOf(t, member, "mine", "backend"); !slices.Equal(mine.Stored.Rank, rank) || mine.Stored.End != "wait" || !reflect.DeepEqual(mine.Stored.Efforts, efforts) {
		t.Fatalf("personal order reported without its ending or levels: %+v", mine.Stored)
	}
	// A column that follows another order has no ending or levels of its own.
	if follows := boardColumnOf(t, member, "mine", "frontend"); follows.Stored.End != "" || follows.Stored.Efforts != nil {
		t.Fatalf("a followed order's values were reported as the column's own: %+v", follows.Stored)
	}
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", map[string]any{"rank": []string{"anthropic:opus"}, "efforts": map[string]string{"anthropic:opus": "high"}, "end": "default", "revision": 0}, ""), 200)
	if shared := boardColumnOf(t, admin, "default", "backend"); shared.Stored.End != "default" || !reflect.DeepEqual(shared.Stored.Efforts, map[string]string{"anthropic:opus": "high"}) {
		t.Fatalf("workspace order reported with another ending or levels: %+v", shared.Stored)
	}
	boardDecode[boardWriteResult](t, boardCall(t, member, "DELETE", "/api/model-preferences/orders/backend/first?for=me&revision="+strconv.FormatInt(saved.Revision, 10), nil, member.ID), 200)
	if reset := boardColumnOf(t, member, "mine", "backend"); reset.Stored.End != "" || reset.Stored.Efforts != nil {
		t.Fatalf("a reset order still reports an ending or levels: %+v", reset.Stored)
	}
}

// Risk: a migrated pin-to-bottom step stays forced last however the order is
// edited, or folding it through the workspace rules re-stamps the row's other
// rules, so "Never for …" names the folding admin instead of its author.
func TestModelsV2MigratedTailFoldsThroughRulesAndKeepsStamps(t *testing.T) {
	admin, _ := boardFixture(t)
	ada := addPrincipal(t, admin.TenantID, "person", "Ada", []string{"admin"})
	set := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	order := func(rank []string, revision int64) boardWriteResult {
		t.Helper()
		return boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first?for=default", map[string]any{"rank": rank, "revision": revision}, ""), 200)
	}
	saved := order([]string{"openai:sol", "anthropic:opus"}, 0)
	// What migration 1333 leaves for a legacy pin-to-bottom and Not-allowed rule:
	// the moved source rows and their copies, with author and time.
	inRegistry(t, admin, func(tx pgx.Tx) error {
		for _, rule := range []struct{ line, lock, kind, why string }{{"anthropic:fable", "bottom", "chain", "Last resort"}, {"openai:astra", "not", "never", "Too slow for backend"}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_rules(tenant_id,scope,column_key,line,lock,position,why,set_by,set_at,moved_to_models) VALUES($1,'workspace','backend',$2,$3,0,$4,$5,$6,true)`, admin.TenantID, rule.line, rule.lock, rule.why, ada.ID, set); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_row_constraints(tenant_id,scope,column_key,line,kind,position,why,set_by,set_at) VALUES($1,'workspace','backend',$2,$3,0,$4,$5,$6)`, admin.TenantID, rule.line, rule.kind, rule.why, ada.ID, set); err != nil {
				return err
			}
		}
		return nil
	})
	row := func() simpleRow {
		t.Helper()
		doc := boardDecode[simpleDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/simple?for=default", nil, ""), 200)
		for _, row := range doc.Exceptions {
			if row.Column == "backend" {
				return row
			}
		}
		t.Fatalf("missing backend row: %+v", doc.Exceptions)
		return simpleRow{}
	}
	chain := func(r simpleRow) []string {
		lines := []string{}
		for _, step := range r.Chain {
			lines = append(lines, step.Line)
		}
		return lines
	}
	rulesRevision := func() int64 {
		t.Helper()
		return boardDecode[rulesDocument](t, boardCall(t, admin, "GET", "/api/model-rules", nil, ""), 200).Revision
	}
	putRules := func(bottom []rulePin, revision int64) rulesDocument {
		t.Helper()
		return boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", "/api/model-rules/workspace/backend", map[string]any{"top": []rulePin{}, "bottom": bottom, "not": map[string]string{"openai:astra": "Too slow for backend"}, "revision": revision}, ""), 200)
	}
	// A rank edit alone cannot move the migrated step: a workspace rule keeps it last.
	saved = order([]string{"openai:sol", "anthropic:fable", "anthropic:opus"}, saved.Revision)
	if got := chain(row()); !slices.Equal(got, []string{"anthropic:opus", "anthropic:fable"}) {
		t.Fatalf("migrated step not kept last: %v", got)
	}
	// Taking it out: the page lifts its rule, then writes the order without it.
	lifted := putRules([]rulePin{}, rulesRevision())
	saved = order([]string{"openai:sol", "anthropic:opus"}, saved.Revision)
	current := row()
	if got := chain(current); !slices.Equal(got, []string{"anthropic:opus"}) {
		t.Fatalf("removed migrated step came back: %v", got)
	}
	// The row's Never rule was left as it was, so it still names who set it and when.
	if len(current.Never) != 1 || current.Never[0].Line != "openai:astra" || current.Never[0].By == nil || *current.Never[0].By != ada.ID || current.Never[0].At == nil || !current.Never[0].At.Equal(set) {
		t.Fatalf("an unchanged Never rule was re-stamped: %+v", current.Never)
	}
	// Undo sets the rule again and writes the order back; the step is last again.
	putRules([]rulePin{{Line: "anthropic:fable", Why: "Last resort"}}, lifted.Revision)
	saved = order([]string{"openai:sol", "anthropic:opus", "anthropic:fable"}, saved.Revision)
	if got := chain(row()); !slices.Equal(got, []string{"anthropic:opus", "anthropic:fable"}) {
		t.Fatalf("Undo did not restore the migrated step: %v", got)
	}
	// Moving it: the rule folds into the order, where the rank decides its place.
	putRules([]rulePin{}, rulesRevision())
	order([]string{"openai:sol", "anthropic:fable", "anthropic:opus"}, saved.Revision)
	if got := chain(row()); !slices.Equal(got, []string{"anthropic:fable", "anthropic:opus"}) {
		t.Fatalf("a moved migrated step was forced last again: %v", got)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var left int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_row_constraints WHERE column_key='backend' AND kind='chain'`).Scan(&left); err != nil {
			return err
		}
		if left != 0 {
			t.Fatalf("a folded step still constrains the row: %d", left)
		}
		return nil
	})
}

// Risk: the order and pause writers that replace the retired ladder editor let
// a concurrent writer or a revocation past their tenant fence: a stale
// conditional order overwrites a newer one, two pauses interleave, a demotion
// commits in the middle of a write, or a write commits after a demotion it
// waited for. Ports the ladder editor's conditional, unconditional and
// revocation fence test to the Models APIs, with barriers, not sleeps.
func TestModelsV2OrderAndPauseWritesFenceConcurrentWritersAndRevocation(t *testing.T) {
	const pausePath = "/api/models/lines/codex/gpt-6.1-sol/pause"
	const orderPath = "/api/model-preferences/orders/backend/first?for=default"
	pauseBody := func(reason string) string { return editorJSON(map[string]any{"until": nil, "reason": reason}) }
	pauseReason := func(t *testing.T, h *editorHTTP, p tenant.Principal) string {
		t.Helper()
		reason := ""
		for _, profile := range editorDecode[[]Profile](t, h.call(t, p, "GET", "/api/models", "", nil)) {
			if profile.Harness == "codex" && profile.Model == "gpt-6.1-sol" && profile.Pause != nil {
				reason = profile.Pause.Reason
			}
		}
		return reason
	}
	storedRank := func(t *testing.T, h *editorHTTP, p tenant.Principal) []string {
		t.Helper()
		for _, column := range editorDecode[boardDocument](t, h.call(t, p, "GET", "/api/model-preferences/board?layer=default", "", nil)).Columns {
			if column.Column == "backend" {
				return column.Stored.Rank
			}
		}
		t.Fatal("missing backend column")
		return nil
	}
	demote := func(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$2 AND key='member') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID, p.TenantID)
		return err
	}
	// The first pause write stops at its clock, after its tenant fence and authority check.
	heldPause := func(t *testing.T) (*editorHTTP, <-chan int, chan<- struct{}) {
		t.Helper()
		held, release := make(chan int, 1), make(chan struct{})
		var calls atomic.Int32
		m := &Module{pool: appPool, validationClock: func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
			if calls.Add(1) == 1 {
				var pid int
				if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					return time.Time{}, err
				}
				held <- pid
				select {
				case <-release:
				case <-ctx.Done():
					return time.Time{}, ctx.Err()
				}
			}
			return validationNow(ctx, tx)
		}}
		return newEditorHTTP(t, m), held, release
	}
	waitHeld := func(t *testing.T, held <-chan int) int {
		t.Helper()
		select {
		case pid := <-held:
			return pid
		case <-time.After(10 * time.Second):
			t.Fatal("writer did not reach its post-fence clock")
			return 0
		}
	}
	for _, mode := range []string{"conditional", "unconditional", "revoke", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := editorFixture(t)
			switch mode {
			case "conditional":
				// Orders carry the profile revision: the competing writer waits on the
				// first one's fence and then finds its revision stale.
				h := newEditorHTTP(t, &Module{pool: appPool})
				revision := editorDecode[boardDocument](t, h.call(t, p, "GET", "/api/model-preferences/board?layer=default", "", nil)).Revision
				body := func(rank ...string) string { return editorJSON(map[string]any{"rank": rank, "revision": revision}) }
				blocker, err := adminPool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				// The first writer reads the orders after its fence; holding the table stops it there.
				if _, err := blocker.Exec(t.Context(), `LOCK TABLE model_pref_orders IN ACCESS EXCLUSIVE MODE`); err != nil {
					t.Fatal(err)
				}
				var blockerPID, writerPID int
				if err := blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
					t.Fatal(err)
				}
				first := startEditorRequest(t, h, p, "PUT", orderPath, body("anthropic:opus", "openai:sol"), nil)
				waitEditorBlocked(t, blockerPID)
				if err := adminPool.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, blockerPID).Scan(&writerPID); err != nil {
					t.Fatal(err)
				}
				second := startEditorRequest(t, h, p, "PUT", orderPath, body("openai:sol"), nil)
				waitEditorBlocked(t, writerPID)
				if err := blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				editorDecode[boardWriteResult](t, finishEditorRequest(t, first))
				editorError(t, finishEditorRequest(t, second), 409, "stale_revision")
				if got := storedRank(t, h, p); !slices.Equal(got, []string{"anthropic:opus", "openai:sol"}) {
					t.Fatalf("stale competing writer overwrote the first: %v", got)
				}
			case "unconditional":
				// Pauses carry no revision: the second waits for the first, then applies after it.
				h, held, release := heldPause(t)
				first := startEditorRequest(t, h, p, "PUT", pausePath, pauseBody("First hold"), nil)
				pid := waitHeld(t, held)
				second := startEditorRequest(t, h, p, "PUT", pausePath, pauseBody("Second hold"), nil)
				waitEditorBlocked(t, pid)
				release <- struct{}{}
				if got := editorDecode[modelPauseResult](t, finishEditorRequest(t, first)); got.Pause == nil || got.Pause.Reason != "First hold" {
					t.Fatalf("first pause answered with another result: %+v", got)
				}
				editorDecode[modelPauseResult](t, finishEditorRequest(t, second))
				if got := pauseReason(t, h, p); got != "Second hold" {
					t.Fatalf("pauses did not apply in fence order: %q", got)
				}
			case "revoke":
				// A demotion waits behind a write that was authorized under the fence;
				// afterwards the demoted person's writes are refused.
				h, held, release := heldPause(t)
				first := startEditorRequest(t, h, p, "PUT", pausePath, pauseBody("Before demotion"), nil)
				pid := waitHeld(t, held)
				revoked := make(chan error, 1)
				go func() {
					revoked <- db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { return demote(t.Context(), tx, p) })
				}()
				waitEditorBlocked(t, pid)
				release <- struct{}{}
				editorDecode[modelPauseResult](t, finishEditorRequest(t, first))
				select {
				case err := <-revoked:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("demotion stayed blocked")
				}
				for _, w := range []struct{ path, body string }{{pausePath, pauseBody("After demotion")}, {orderPath, editorJSON(map[string]any{"rank": []string{"openai:sol"}, "revision": 0})}} {
					if got := h.call(t, p, "PUT", w.path, w.body, nil); got.Code != http.StatusForbidden {
						t.Fatalf("demoted person still writes %s: %d %s", w.path, got.Code, got.Body.String())
					}
				}
				if got := pauseReason(t, h, p); got != "Before demotion" {
					t.Fatalf("authorized pause lost or overwritten: %q", got)
				}
			case "revoked":
				// A write that waits for a demotion's fence checks authority after it,
				// inside its own transaction, and writes nothing.
				h := newEditorHTTP(t, &Module{pool: appPool})
				before := storedRank(t, h, p)
				events := eventCount(t, p, "model.preferences_changed")
				blocker, err := adminPool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if err := demote(t.Context(), blocker, p); err != nil {
					t.Fatal(err)
				}
				var blockerPID int
				if err := blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
					t.Fatal(err)
				}
				write := startEditorRequest(t, h, p, "PUT", orderPath, editorJSON(map[string]any{"rank": []string{"anthropic:opus"}, "revision": 0}), nil)
				waitEditorBlocked(t, blockerPID)
				if err := blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if w := finishEditorRequest(t, write); w.Code != http.StatusForbidden {
					t.Fatalf("a write that waited for the demotion was not refused: %d %s", w.Code, w.Body.String())
				}
				if !slices.Equal(storedRank(t, h, p), before) || eventCount(t, p, "model.preferences_changed") != events {
					t.Fatal("a refused write changed the order or wrote an event")
				}
			}
		})
	}
}
