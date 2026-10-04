// SPDX-License-Identifier: AGPL-3.0-only
package ticketwork

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func oldWorkDatabase(t *testing.T) *dbtest.DB {
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

func TestMigratedWorkSessionsAndCostsRespectVisibility(t *testing.T) {
	d := oldWorkDatabase(t)
	f := newWorkFixtureWithDB(t, d)
	parent, leaf, hiddenProject, hiddenLeaf := uid(), uid(), uid(), uid()
	f.node(t, f.project, "project", "TW1-1", "Visible", "")
	f.node(t, parent, "epic", "TW1-2", "Retained parent", f.project)
	f.node(t, leaf, "task", "TW1-3", "Retained leaf", parent)
	f.node(t, hiddenProject, "project", "TW1-4", "Hidden", "")
	f.node(t, hiddenLeaf, "ticket", "TW1-5", "Hidden leaf", hiddenProject)
	f.bindGuest(t, f.project)
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	parentSession, leafSession, hiddenSession := uid(), uid(), uid()
	for _, s := range []struct{ id, project, node, cost string }{
		{parentSession, f.project, parent, "1.000000000000"},
		{leafSession, f.project, leaf, "2.000000000000"},
		{hiddenSession, hiddenProject, leaf, "99.000000000000"},
	} {
		f.session(t, s.id, s.project, s.node, "codex", "gpt-test", "high", past, past.Add(time.Minute), past, "stopped")
		f.usage(t, f.person, s.id, "gpt-test", "100", "20", "0", s.cost, false, "1", "api", "")
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		node, cost  string
		count       int
		descendants bool
	}{
		{leaf, "2.000000000000", 1, false}, {parent, "3.000000000000", 2, true},
	} {
		t.Run(tc.node, func(t *testing.T) {
			r := f.get(t, f.guest, tc.node, "")
			if r.Kind != "work" || r.IncludesDescendants != tc.descendants || r.Totals.SessionCount != tc.count || str(r.Totals.EstimatedCostUSD) != tc.cost {
				t.Fatalf("retained report: %+v", r)
			}
			for _, s := range r.Sessions {
				if s.ID == hiddenSession {
					t.Fatal("hidden session leaked")
				}
			}
		})
	}
	for _, target := range []string{hiddenLeaf, f.project} {
		if got := f.status(t, f.guest, target, ""); got != http.StatusNotFound {
			t.Fatalf("hidden/non-work target status %d", got)
		}
	}
	if got := f.status(t, f.foreign, leaf, ""); got != http.StatusNotFound {
		t.Fatalf("foreign tenant status %d", got)
	}
}
