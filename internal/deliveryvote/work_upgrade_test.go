// SPDX-License-Identifier: AGPL-3.0-only
package deliveryvote

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func oldVoteDatabase(t *testing.T) *dbtest.DB {
	t.Helper()
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	stop := errors.New("before work")
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name == "1215_one_work_kind.sql" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("old schema: %v", err)
	}
	return d
}

func TestMigratedWorkRetainsDeliveryRatingsAndVisibility(t *testing.T) {
	d := oldVoteDatabase(t)
	f := newFixWithDB(t, d)
	project, parent, leaf, hiddenProject, hiddenLeaf := id(), id(), id(), id(), id()
	f.node(t, project, "project", "VT1-1", "Visible", "")
	f.node(t, parent, "epic", "VT1-2", "Retained parent", project)
	f.node(t, leaf, "task", "VT1-3", "Retained leaf", parent)
	f.node(t, hiddenProject, "project", "VT1-4", "Hidden", "")
	f.node(t, hiddenLeaf, "ticket", "VT1-5", "Hidden leaf", hiddenProject)
	f.bindGuest(t, project)
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	stopped := past.Add(time.Minute)
	parentSession, leafSession, hiddenSession := id(), id(), id()
	for _, s := range []struct{ id, project, node string }{{parentSession, project, parent}, {leafSession, project, leaf}, {hiddenSession, hiddenProject, leaf}} {
		f.session(t, s.id, s.project, s.node, "gpt-test", "desk", past, &stopped)
		f.put(t, f.person, s.id, `{"score":4,"tags":["quality"],"comment":"retained evidence"}`)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		node  string
		count int
	}{{leaf, 1}, {parent, 2}} {
		t.Run(tc.node, func(t *testing.T) {
			r := f.list(t, f.guest, tc.node)
			if len(r.Sessions) != tc.count {
				t.Fatalf("retained ratings: %+v", r)
			}
			for _, s := range r.Sessions {
				if s.SessionID == hiddenSession || s.Votes != 1 || s.Average == nil || *s.Average != "4.00" {
					t.Fatalf("retained visible rating: %+v", s)
				}
			}
		})
	}
	for _, target := range []string{hiddenLeaf, project} {
		if w := f.call(f.guest, http.MethodGet, "/api/nodes/"+target+"/delivery-ratings", ""); w.Code != http.StatusNotFound {
			t.Fatalf("hidden/non-work target: %d %s", w.Code, w.Body.String())
		}
	}
	if w := f.call(f.foreign, http.MethodGet, "/api/nodes/"+leaf+"/delivery-ratings", ""); w.Code != http.StatusNotFound {
		t.Fatalf("foreign tenant: %d %s", w.Code, w.Body.String())
	}
}
