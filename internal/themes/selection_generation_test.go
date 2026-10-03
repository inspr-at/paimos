// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

func activeHTTP(t *testing.T, h http.Handler, p tenant.Principal) Active {
	t.Helper()
	w := call(t, h, p, "GET", "/api/me/theme", "")
	expect(t, w, 200)
	var a Active
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSelectionCASRejectsChangedWinningIdentity(t *testing.T) {
	for _, topology := range []string{"link", "unlink"} {
		t.Run(topology, func(t *testing.T) {
			f := setup(t)
			a := mustTheme(t, f.s, f.owner, "Alias choice", "workspace")
			b := mustTheme(t, f.s, f.owner, "Canonical choice", "workspace")
			for _, c := range []struct {
				person tenant.Principal
				theme  Theme
			}{{f.member, a}, {f.other, b}} {
				if _, err := f.s.Select(t.Context(), c.person, SelectionInput{ThemeID: &c.theme.ID}); err != nil {
					t.Fatal(err)
				}
				var saved Selection
				if err := json.Unmarshal(lastEvent(t, f, "theme.selected").After, &saved); err != nil || saved.Revision != 1 {
					t.Fatalf("fixture must have equal physical revisions: %+v %v", saved, err)
				}
			}
			mux := http.NewServeMux()
			New(f.d.App).Mount(mux)
			if topology == "unlink" {
				linkPeople(t, f, f.member, f.other)
			}
			stale := activeHTTP(t, mux, f.member)
			wantBefore, wantAfter := a.ID, b.ID
			if topology == "link" {
				linkPeople(t, f, f.member, f.other)
			} else {
				wantBefore, wantAfter = b.ID, a.ID
				unlinkPerson(t, f, f.member)
			}
			current := activeHTTP(t, mux, f.member)
			if stale.Theme.ID != wantBefore || current.Theme.ID != wantAfter {
				t.Fatalf("winning identity did not switch: %+v -> %+v", stale, current)
			}
			beforeEvents := eventCount(t, f)
			expect(t, call(t, mux, f.member, "PUT", "/api/me/theme", fmt.Sprintf(`{"theme_id":null,"revision":%d}`, stale.Revision)), 409)
			if got := activeHTTP(t, mux, f.member); !same(got, current) || eventCount(t, f) != beforeEvents {
				t.Fatalf("stale choice changed state or audit: %+v -> %+v", current, got)
			}
			// A client can recover using the generation returned by its new read.
			expect(t, call(t, mux, f.member, "PUT", "/api/me/theme", fmt.Sprintf(`{"theme_id":null,"revision":%d}`, current.Revision)), 200)
			got := activeHTTP(t, mux, f.member)
			if got.Theme.Scope != "default" || got.SelectedThemeID != nil || got.Revision == current.Revision || eventCount(t, f) != beforeEvents+1 {
				t.Fatalf("fresh choice was not committed exactly once: %+v", got)
			}
		})
	}
}

func TestInitialExplicitDefaultPersistsAndWinsAfterLink(t *testing.T) {
	f := setup(t)
	mux := http.NewServeMux()
	New(f.d.App).Mount(mux)
	initial := activeHTTP(t, mux, f.other)
	if initial.Revision != 0 || initial.Theme.Scope != "default" {
		t.Fatalf("unexpected initial choice: %+v", initial)
	}
	beforeEvents := eventCount(t, f)
	expect(t, call(t, mux, f.other, "PUT", "/api/me/theme", `{"theme_id":null,"revision":0}`), 200)
	explicit := activeHTTP(t, mux, f.other)
	if explicit.Revision == 0 || explicit.SelectedThemeID != nil || explicit.Theme.ID != initial.Theme.ID || eventCount(t, f) != beforeEvents+1 {
		t.Fatalf("first explicit default was not saved and audited: %+v", explicit)
	}
	var saved Selection
	if err := json.Unmarshal(lastEvent(t, f, "theme.selected").After, &saved); err != nil || saved.PrincipalID != f.other.ID || saved.ThemeID != nil || saved.Revision != 1 {
		t.Fatalf("explicit default audit: %+v %v", saved, err)
	}
	// Repeating the explicit choice is a true no-op, including its generation.
	expect(t, call(t, mux, f.other, "PUT", "/api/me/theme", fmt.Sprintf(`{"theme_id":null,"revision":%d}`, explicit.Revision)), 200)
	if got := activeHTTP(t, mux, f.other); !same(got, explicit) || eventCount(t, f) != beforeEvents+1 {
		t.Fatalf("repeated default changed state or audit: %+v", got)
	}
	alias := mustTheme(t, f.s, f.member, "Alias preference", "personal")
	choice, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &alias.ID})
	if err != nil {
		t.Fatal(err)
	}
	linkPeople(t, f, f.member, f.other)
	for _, p := range []tenant.Principal{f.member, f.other} {
		if got := activeHTTP(t, mux, p); !same(got, explicit) {
			t.Fatalf("explicit canonical default lost after link: %+v", got)
		}
	}
	unlinkPerson(t, f, f.member)
	if got := activeHTTP(t, mux, f.member); !same(got, choice) {
		t.Fatalf("unlink lost the alias's original preference: %+v", got)
	}
	if got := activeHTTP(t, mux, f.other); !same(got, explicit) {
		t.Fatalf("unlink lost explicit canonical default: %+v", got)
	}
}
