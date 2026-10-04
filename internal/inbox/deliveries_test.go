// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestMessagingAtomicRollbackAndProjectBoundary(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	var baseline int
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.Admin, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION p54_reject_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rollback'; END $$; CREATE TRIGGER p54_reject BEFORE INSERT ON inbox_message_deliveries FOR EACH ROW EXECUTE FUNCTION p54_reject_delivery()`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	in := compatInput("codex:worker", "rollback")
	in.ExpectsReply = true
	if _, err := m.commitMessage(t.Context(), w.sender, project, in); err == nil {
		t.Fatal("failed outbox did not abort")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.Admin, w.sender.TenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"inbox_compat_messages", "inbox_messages", "inbox_reply_obligations", "inbox_message_deliveries", "events"} {
			var n int
			if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			want := 0
			if table == "events" {
				want = baseline
			}
			if n != want {
				return fmt.Errorf("partial write in %s", table)
			}
		}
		_, err := tx.Exec(t.Context(), `DROP TRIGGER p54_reject ON inbox_message_deliveries; DROP FUNCTION p54_reject_delivery()`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := mustCompatSend(t, m, w.sender, project, in)
	var otherProject string
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1::uuid,id,'MSG-2','Other' FROM node_kinds WHERE slug='project' RETURNING id::text`, w.sender.TenantID).Scan(&otherProject)
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := compatInput(w.sender.ID, "wrong-project")
	reply.ReplyTo = &msg.ID
	if _, err := m.commitMessage(t.Context(), w.agent, otherProject, reply); !errors.Is(err, errNotFound) {
		t.Fatal("cross-project reply accepted", err)
	}
	status, data := do(t, srv, w.agent.ID, "GET", "/api/projects/"+otherProject+"/messages/listen", "", nil)
	if status != 200 || len(mustJSON[compatPage](t, data).Items) != 0 {
		t.Fatal("project inbox leaked")
	}
	// An administrator in another tenant still sees no project.
	foreign := insertPrincipal(t, w.db, w.outsider.TenantID, tenant.Agent, "foreign-admin", []string{"admin"})
	if _, err := m.storeTarget(t.Context(), foreign, project, targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "fixture", Role: "primary", MaximumLevel: "simple"}); !errors.Is(err, errNotFound) {
		t.Fatal("foreign project target accepted", err)
	}
}

func TestMessagingPaginationHeldFilterAndEvents(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	for i := range 12 {
		in := compatInput("codex:worker", fmt.Sprint(i))
		if i == 3 {
			in.ActionRequest = true
		}
		mustCompatSend(t, m, w.sender, project, in)
	}
	path := "/api/projects/" + project + "/messages/listen?to=codex:worker"
	status, data := do(t, srv, w.agent.ID, "GET", path, "", nil)
	if status != 200 {
		t.Fatalf("page %d", status)
	}
	first := mustJSON[compatPage](t, data)
	if len(first.Items) != 10 {
		t.Fatalf("first page count %d", len(first.Items))
	}
	status, data = do(t, srv, w.agent.ID, "GET", path+fmt.Sprintf("&after=%d", first.NextAfter), "", nil)
	if status != 200 {
		t.Fatalf("next page %d", status)
	}
	second := mustJSON[compatPage](t, data)
	if len(second.Items) != 1 || second.Items[0].SentEventID <= first.NextAfter {
		t.Fatal("cursor lost/duplicated rows")
	}
	for _, suffix := range []string{"&after=-1", "&limit=11", "&limit=0"} {
		status, _ := do(t, srv, w.agent.ID, "GET", path+suffix, "", nil)
		if status != 400 {
			t.Fatal("invalid pagination accepted")
		}
	}
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		var leaked bool
		if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE after::text LIKE '%fixture message body%')`).Scan(&leaked); err != nil {
			return err
		}
		if leaked {
			return errors.New("message body leaked into tenant events")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
