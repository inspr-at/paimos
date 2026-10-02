// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func (f *fixture) claim(t *testing.T, p tenant.Principal, item Item) (bool, error) {
	t.Helper()
	claimed := false
	err := db.InTenant(db.AllProjects(t.Context(), "desk claim test"), f.d.App, p.TenantID, func(tx pgx.Tx) error {
		var err error
		claimed, err = ClaimTx(t.Context(), tx, p, item)
		return err
	})
	return claimed, err
}

func TestConcurrentClaimsRetryRecipientAndRevocation(t *testing.T) {
	f := setup(t)
	q := f.ask(t, f.project, "parked", []string{f.ticket})
	item := f.page(t, f.person, 100, nil).Items[0]
	if item.ID != q.ID || !item.Held {
		t.Fatal("fixture is not held")
	}
	start := make(chan struct{})
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; won, err := f.claim(t, f.person, item); results <- won; errs <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for won := range results {
		if won {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("replicas claimed %d pushes", winners)
	}
	if won, err := f.claim(t, f.person, item); err != nil || won {
		t.Fatalf("retry repeated push: %t %v", won, err)
	}
	if won, err := f.claim(t, f.reader, item); err != nil || !won {
		t.Fatalf("claim was not per recipient: %t %v", won, err)
	}
	// Capture another reader's still-current source, then revoke its binding
	// before the claim. The stale projection and role hint grant no authority.
	f.exec(t, `DELETE FROM role_bindings WHERE principal_id=$1`, f.reader.ID)
	f.reader.Roles = []string{"owner"}
	if won, err := f.claim(t, f.reader, item); err != nil || won {
		t.Fatalf("revoked recipient claimed push: %t %v", won, err)
	}
	err := db.InTenant(db.AllProjects(t.Context(), "desk retry test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		items, err := NoticesTx(t.Context(), tx, f.person, 10)
		if err != nil {
			return err
		}
		if len(items) != 0 {
			t.Fatal("claimed notice still scheduled")
		}
		return FinishTx(t.Context(), tx, f.person, item, "failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if won, err := f.claim(t, f.person, item); err != nil || won {
		t.Fatal("ambiguous failure retried a push", err)
	}
	var state string
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT state FROM desk_notification_claims WHERE item_id=$1 AND recipient_id=$2`, item.ID, f.person.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("failed transport falsely reported success", state, err)
	}
}

func TestNotificationPointersAndNormalBriefingItem(t *testing.T) {
	f := setup(t)
	q := f.ask(t, f.project, "carries_on", []string{f.ticket})
	page := f.page(t, f.person, 100, nil)
	if page.Counts.Open != 1 || page.Items[0].ID != q.ID || page.Items[0].Source != "/api/questions/"+q.ID {
		t.Fatal("ordinary question absent from briefing source")
	}
	err := db.InTenant(db.AllProjects(t.Context(), "desk normal-item test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		items, err := NoticesTx(t.Context(), tx, f.person, 100)
		if len(items) != 0 {
			t.Fatal("normal briefing item generated a push")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	item.Href = "/private-question-text"
	item.Title = "private-question-text"
	b, err := Payload(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-question-text") || strings.Contains(string(b), q.Input.Question) {
		t.Fatal("push retained private question text")
	}
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["url"] != "/agents?needs=q:"+q.ID || fields["item_id"] != q.ID {
		t.Fatalf("incorrect pointer: %s", b)
	}
	item.Kind = "doctrine"
	if _, err = Payload(item); err == nil {
		t.Fatal("doctrine toast also produced a push")
	}
}
