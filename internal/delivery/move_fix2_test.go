// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Pause the real lifecycle writer at its tree fence and prove that the move
// waits there. The requested revision is the committed freeze revision for
// reranks, so a CAS refusal cannot masquerade as enforcement of frozen scope.
func TestFrozenPlacementAndUndoRecheckAfterFence(t *testing.T) {
	for _, action := range []string{"remove", "rerank", "undo_remove", "undo_add", "undo_rerank"} {
		t.Run(action, func(t *testing.T) {
			f := newStoreFixture(t)
			subject := f.item(t, "ticket", "TK-1", "open", f.release, "V")
			f.item(t, "ticket", "TK-2", "open", f.release, "X")
			var event events.Event
			freezeID := f.release
			if strings.HasPrefix(action, "undo_") {
				request := PlacementRequest{ItemID: subject, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1}
				if action == "undo_add" {
					freezeID = f.next // Undo would remove work from this frozen source.
				} else if action == "undo_rerank" {
					request.ReleaseID = f.release
				}
				if _, err := f.store.PlaceWithRevision(t.Context(), f.person, f.project, []PlacementRequest{request}); err != nil {
					t.Fatal(err)
				}
				event = f.lastEvent(t, "ships_in.changed")
			}
			before := f.placed(t, subject)
			beforeEvents := f.scalar(t, `SELECT count(*) FROM events`)
			freezing := f.releaseRow(t, freezeID)
			otherID := f.next
			if freezeID == f.next {
				otherID = f.release
			}
			other := f.releaseRow(t, otherID)
			pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(query string) bool { return strings.Contains(query, "pg_advisory_xact_lock") })
			store := NewStore(pool).WithClock(func() time.Time { return f.clock })
			frozen := make(chan error, 1)
			go func() {
				_, err := store.Transition(ctx, f.person, TransitionRequest{ProjectID: f.project, ReleaseID: freezeID, ExpectedRevision: freezing.Revision, Action: "freeze"})
				frozen <- err
			}()
			pid := barrier.Wait(t, ctx)
			type outcome struct {
				result PlacementResult
				err    error
				undo   *httptest.ResponseRecorder
			}
			moved, done := make(chan outcome, 1), make(chan struct{})
			go func() {
				defer close(done)
				if strings.HasPrefix(action, "undo_") {
					mux := http.NewServeMux()
					events.New(f.d.App, events.WithUndoHandlers(f.store.UndoHandlers())).Mount(mux)
					r := httptest.NewRequest("POST", fmt.Sprintf("/api/events/%d/undo", event.ID), nil).WithContext(tenant.WithPrincipal(ctx, f.person))
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, r)
					moved <- outcome{undo: w}
					return
				}
				request := PlacementRequest{ItemID: subject, ExpectedProjectID: f.project, ExpectedRevision: before.Revision}
				if action == "rerank" {
					request.ReleaseID = f.release
					request.ExpectedReleaseRevision = freezing.Revision + 1
				}
				result, err := f.store.PlaceWithRevision(ctx, f.person, f.project, []PlacementRequest{request})
				moved <- outcome{result: result, err: err}
			}()
			if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock != "advisory" {
				t.Fatalf("move bypassed freeze fence: %q", lock)
			}
			barrier.Release()
			if err := dbtest.Await(t, ctx, frozen); err != nil {
				t.Fatal(err)
			}
			out := dbtest.Await(t, ctx, moved)
			if out.undo != nil {
				if out.undo.Code != http.StatusConflict || !strings.Contains(out.undo.Body.String(), "frozen") {
					t.Errorf("Undo returned a receipt or wrong refusal: %d %s", out.undo.Code, out.undo.Body.String())
				}
			} else if !errors.Is(out.err, ErrFrozen) || out.result.UndoEventID != nil || len(out.result.Items) != 0 {
				t.Errorf("wrong frozen refusal or successful receipt: %+v %v", out.result, out.err)
			}
			if !reflect.DeepEqual(before, f.placed(t, subject)) || !reflect.DeepEqual(other, f.releaseRow(t, otherID)) {
				t.Error("refused move changed placement or other release")
			}
			afterFreeze := f.releaseRow(t, freezeID)
			if afterFreeze.State != "frozen" || afterFreeze.Revision != freezing.Revision+1 || f.scalar(t, `SELECT count(*) FROM events`) != beforeEvents+1 {
				t.Error("move changed the frozen release or appended an event beyond freeze")
			}
		})
	}
}

func TestReleaseAnchorRechecksTerminalStateAfterFence(t *testing.T) {
	for _, action := range []string{"close", "abandon"} {
		for _, side := range []string{"before", "after"} {
			t.Run(action+"/"+side, func(t *testing.T) {
				f := newStoreFixture(t)
				anchor := f.addRelease(t, f.project, "REL-3", "internal", "planned", "F")
				if action == "close" {
					f.item(t, "ticket", "TK-1", "done", anchor.ID, "V")
					anchor = f.freeze(t, anchor.ID)
				}
				subject := f.addRelease(t, f.project, "REL-4", "internal", "planned", "H")
				beforeEvents := f.scalar(t, `SELECT count(*) FROM events`)
				pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(query string) bool { return strings.Contains(query, "pg_advisory_xact_lock") })
				store := NewStore(pool).WithClock(func() time.Time { return f.clock })
				closed := make(chan error, 1)
				go func() {
					_, err := store.Transition(ctx, f.person, TransitionRequest{ProjectID: f.project, ReleaseID: anchor.ID, ExpectedRevision: anchor.Revision, Action: action})
					closed <- err
				}()
				pid := barrier.Wait(t, ctx)
				type outcome struct {
					release Release
					err     error
				}
				reranked, done := make(chan outcome, 1), make(chan struct{})
				go func() {
					defer close(done)
					slot := Slot{BeforeID: anchor.ID}
					if side == "after" {
						slot = Slot{AfterID: anchor.ID}
					}
					r, err := f.store.Rerank(ctx, f.person, ReleaseEdit{ProjectID: f.project, ReleaseID: subject.ID, ExpectedRevision: subject.Revision, Slot: slot})
					reranked <- outcome{r, err}
				}()
				if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock != "advisory" {
					t.Fatalf("rank bypassed terminal writer: %q", lock)
				}
				barrier.Release()
				if err := dbtest.Await(t, ctx, closed); err != nil {
					t.Fatal(err)
				}
				out := dbtest.Await(t, ctx, reranked)
				if !errors.Is(out.err, ErrNotFound) || out.release.UndoEventID != nil {
					t.Errorf("terminal direct anchor accepted: %+v %v", out.release, out.err)
				}
				if !reflect.DeepEqual(subject, f.releaseRow(t, subject.ID)) || f.scalar(t, `SELECT count(*) FROM events`) != beforeEvents+1 {
					t.Error("terminal-anchor refusal changed rank/revision or appended a rank event")
				}
			})
		}
	}
}
