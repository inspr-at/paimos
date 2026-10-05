// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"testing"

	"github.com/inspr-at/paimos/internal/releasehistory/codename"
)

func TestReleaseSheetStatusUsesExplicitProductBinding(t *testing.T) {
	f := newStoreFixture(t)
	for _, tc := range []struct {
		name, tenant, project string
		product               bool
	}{
		{"unbound", "", "", false},
		{"bound", f.tenant, f.project, true},
		{"different project", f.tenant, f.legacy, false},
		{"different tenant", "00000000-0000-4000-8000-000000000099", f.project, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := f.store.WithProductProject(tc.tenant, tc.project).Status(t.Context(), f.person, f.project)
			if err != nil {
				t.Fatal(err)
			}
			if status.ProductProject != tc.product {
				t.Fatalf("product_project=%v; want %v", status.ProductProject, tc.product)
			}
			view, err := f.store.WithProductProject(tc.tenant, tc.project).GetRelease(t.Context(), f.person, f.project, f.release)
			if err != nil {
				t.Fatal(err)
			}
			if tc.product && view.DisplayName != codename.Codename(view.Sequence) {
				t.Fatalf("bound name=%q", view.DisplayName)
			}
			if !tc.product && view.DisplayName != "" {
				t.Fatal("product marketing name escaped its explicit binding")
			}
		})
	}
}
