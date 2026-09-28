// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestSessionMessageMigrationPreservesUnboundHistory(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenants []string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "0898_session_messages.sql" {
			return nil
		}
		for i := range 2 {
			var tenantID string
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Message migration') RETURNING id::text`, fmt.Sprintf("migration-%d", i)).Scan(&tenantID); err != nil {
				return err
			}
			tenants = append(tenants, tenantID)
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
				var sender, recipient, project, message string
				var event int64
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Sender') RETURNING id::text`, tenantID).Scan(&sender); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Old worker') RETURNING id::text`, tenantID).Scan(&recipient); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'MSG-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, tenantID).Scan(&project); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'inbox.sent','{}') RETURNING id`, tenantID, sender).Scan(&event); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO inbox_messages(tenant_id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key) VALUES($1,$2,$3,$4,'Historical body','old') RETURNING id::text`, tenantID, sender, recipient, event).Scan(&message); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,sender_address,recipient_address,body,key_digest,request_digest,thread_id,hop,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level) VALUES($1,$2,$3,$4,$5,'paimos:historical-sender','codex:old-worker','Historical body','key','request','thread',1,$2,$6,false,false,'simple')`, tenantID, message, project, sender, recipient, event)
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tenantID := range tenants {
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			for _, table := range []string{"inbox_messages", "inbox_compat_messages"} {
				var n int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE recipient_session_id IS NULL AND sender_session_id IS NULL AND sender_label IS NULL AND body='Historical body'`).Scan(&n); err != nil {
					return err
				}
				if n != 1 {
					return fmt.Errorf("history changed or tenant leaked: %s count %d", table, n)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
