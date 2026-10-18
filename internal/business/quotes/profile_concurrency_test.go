// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestProfileBundleRefreshAndSelectionLockOrder(t *testing.T) {
	f := newQuoteFixture(t, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "profile-lock-order")
	settings := `{"expected_revision":0,"numbering_time_zone":"Europe/Vienna","default_currency":"EUR","sender":{"company":"Example Sender","street":"Example Street 1","postal_code":"0000","city":"Example City","country":"AT","email":"sender@example.test"},"defaults":{"intro":"","blocks":[],"accept_text":"","vat_note":""},"layout":{},"smtp_confirmation_enabled":false}`
	if status, body := f.call("admin", "PATCH", "/api/quotes/settings", settings); status != 200 {
		t.Fatalf("settings %d %v", status, body)
	}
	definition := syntheticProfile()
	raw, _ := json.Marshal(profileWrite{Name: "Concurrent profile", Definition: definition})
	status, profile := f.call("admin", "POST", "/api/quote-profiles", string(raw))
	if status != 201 {
		t.Fatalf("profile %d %v", status, profile)
	}
	profileID := profile["id"].(string)
	status, quote := f.call("admin", "POST", "/api/quotes", fmt.Sprintf(`{"title":"Concurrent","customer_org_node_id":%q,"profile_id":%q}`, f.ids["org"], profileID))
	if status != 201 {
		t.Fatalf("quote %d %v", status, quote)
	}
	id := quote["quote_node_id"].(string)
	status, draft := f.call("admin", "GET", "/api/quotes/"+id+"/draft", "")
	if status != 200 {
		t.Fatalf("draft %d %v", status, draft)
	}
	revision := draft["draft_revision"].(json.Number).String()
	definition.Cover["top_mm"] = "12"
	raw, _ = json.Marshal(profileWrite{Name: "Concurrent profile", Definition: definition})
	pool, barrier, ctx := dbtest.BarrierPool(t, f.database.App, func(sql string) bool {
		return strings.Contains(sql, "FOR UPDATE OF p") && strings.Contains(sql, "quote_document_profiles p")
	})
	first := make(chan error, 1)
	filesDir := t.TempDir()
	go func() {
		_, err := ApplyProfileBundleRefreshingDrafts(ctx, pool, f.tenantID, f.ids["admin"], filesDir, "", ProfileBundle{Profile: raw, Files: map[string][]byte{}}, false, true)
		first <- err
	}()
	pid := barrier.Wait(t, ctx)
	second := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		status, _ := f.call("admin", "PUT", "/api/quotes/"+id+"/profile", fmt.Sprintf(`{"expected_draft_revision":%s,"profile_id":%q}`, revision, profileID))
		second <- status
		close(done)
	}()
	if dbtest.BlockedOrDone(t, ctx, f.database.Admin, pid, done) == "" {
		t.Fatal("selection did not overlap refresh")
	}
	// Selection must be waiting on the profile without holding the draft's quote.
	_, lockErr := f.database.Admin.Exec(ctx, `SELECT quote_node_id FROM business_quotes WHERE quote_node_id=$1 FOR UPDATE NOWAIT`, id)
	barrier.Release()
	if lockErr != nil {
		t.Errorf("selection took quote before profile: %v", lockErr)
	}
	if err := dbtest.Await(t, ctx, first); err != nil {
		t.Fatal(err)
	}
	if status := dbtest.Await(t, ctx, second); status != 409 {
		t.Fatalf("selection should detect refreshed draft, got %d", status)
	}
}
