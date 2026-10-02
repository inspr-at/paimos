// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/questions"
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
	// Capture a different, unclaimed source before revocation. An existing
	// claim must not make this authorization assertion pass by accident.
	f.ask(t, f.project, "paused", []string{f.ticket})
	readerPage := f.page(t, f.reader, 100, nil)
	var unclaimed Item
	for _, candidate := range readerPage.Items {
		if candidate.ID != item.ID {
			unclaimed = candidate
		}
	}
	if unclaimed.ID == "" {
		t.Fatal("missing fresh held source")
	}
	f.exec(t, `DELETE FROM role_bindings WHERE principal_id=$1`, f.reader.ID)
	f.reader.Roles = []string{"owner"}
	if won, err := f.claim(t, f.reader, unclaimed); err != nil || won {
		t.Fatalf("revoked recipient claimed push: %t %v", won, err)
	}
	err := db.InTenant(db.AllProjects(t.Context(), "desk retry test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		items, err := NoticesTx(t.Context(), tx, f.person, 10)
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].ID != unclaimed.ID {
			t.Fatal("claims starved or repeated a notice")
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

func TestHeldRequestCanonicalizationDoesNotRepeatNotice(t *testing.T) {
	f := setup(t)
	// Authenticate the native inbox entry using an ephemeral test-only key.
	secret := rand.Text()
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(f.agent.TenantID, "-", "") + strings.Repeat("ab", 8)
	f.exec(t, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'held-fixture',$3,$4,$5)`, f.agent.TenantID, f.agent.ID, prefix, hex.EncodeToString(sum[:]), []string{"inbox.send"})
	body, _ := json.Marshal(map[string]any{"to": f.person.ID, "body": "Private held request", "idempotency_key": "held-source", "is_action_request": true, "delivery_level": "simple"})
	r := httptest.NewRequest("POST", "/api/projects/"+f.project+"/messages", bytes.NewReader(body)).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer aeon_"+prefix+"_"+secret)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("held source %d: %s", w.Code, w.Body.String())
	}
	var held struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &held); err != nil {
		t.Fatal(err)
	}
	before := f.page(t, f.person, 100, nil)
	if len(before.Items) != 1 || before.Items[0].ID != held.ID || !before.Items[0].Held {
		t.Fatal("missing native held request")
	}
	if won, err := f.claim(t, f.person, before.Items[0]); err != nil || !won {
		t.Fatal("initial held notice missing", err)
	}
	q := f.askInput(t, f.project, questions.Input{RequestID: held.ID, Question: "Canonical held question", SourceRequestID: held.ID, Meanwhile: "parked", Options: []questions.Option{{ID: "yes", Title: "Yes", Answer: "Proceed"}}})
	after := f.page(t, f.person, 100, nil)
	if after.Counts.Open != 1 || after.Counts.Held != 1 || len(after.Items) != 1 || after.Items[0].ID != q.ID {
		t.Fatal("source and canonical question counted twice or lost held state")
	}
	if won, err := f.claim(t, f.person, after.Items[0]); err != nil || won {
		t.Fatal("canonicalizing a notified held request repeated its push", err)
	}
	if won, err := f.claim(t, f.reader, after.Items[0]); err != nil || !won {
		t.Fatal("canonicalization suppressed a fresh authorized recipient", err)
	}
	// A real answer and grace edit keep the work closed for every recipient.
	answered := f.decide(t, q, "Proceed")
	f.decide(t, answered, "Corrected answer")
	if got := f.page(t, f.person, 100, nil); got.Counts.Open != 0 {
		t.Fatal("answer resurrected linked held request")
	}
	if won, err := f.claim(t, f.person, after.Items[0]); err != nil || won {
		t.Fatal("decision/grace emitted notice", err)
	}
}

func TestApprovalExpiryWarningAndProjectReferenceAreNotHeld(t *testing.T) {
	f := setup(t)
	f.approval(t, f.project, time.Now().Add(time.Hour))
	near := f.approval(t, "", time.Now().Add(NearExpiry/2))
	f.approval(t, "", time.Now().Add(-time.Hour))
	var workOrder, run string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Waiting work',$2 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='work_order' RETURNING id::text`, f.person.TenantID, f.project).Scan(&workOrder); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'blocked')`, f.person.TenantID, workOrder, f.person.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status) VALUES($1,$2,$3,'waiting') RETURNING id::text`, f.person.TenantID, workOrder, f.agent.ID).Scan(&run)
	})
	if err != nil {
		t.Fatal(err)
	}
	linked := f.approval(t, "", time.Now().Add(time.Hour), run)
	page := f.page(t, f.person, 100, nil)
	if page.Counts.Open != 3 || page.Counts.Held != 1 {
		t.Fatal("a project reference or expired approval was marked held")
	}
	var scheduled []Item
	err = db.InTenant(db.AllProjects(t.Context(), "desk expiry test"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		var err error
		scheduled, err = NoticesTx(t.Context(), tx, f.person, 100)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 2 || scheduled[0].ID != linked || scheduled[1].ID != near {
		t.Fatal("near-expiry policy admitted ordinary or expired approvals")
	}
	f.exec(t, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'denied')`, f.person.TenantID, near, f.person.ID)
	if won, err := f.claim(t, f.person, scheduled[1]); err != nil || won {
		t.Fatal("decided approval claimed expiry notification", err)
	}
	f.exec(t, `UPDATE work_orders SET status='done' WHERE node_id=$1`, workOrder)
	if won, err := f.claim(t, f.person, scheduled[0]); err != nil || won {
		t.Fatal("finished work still claimed a held approval", err)
	}
}
