// SPDX-License-Identifier: AGPL-3.0-only
package activity

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestInvitedClassicHistoryUsesNewPerson(t *testing.T) {
	f := setup(t)
	var alias, identity, signin string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='owner') WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='workspace'`, f.p.TenantID, f.p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,email) VALUES('paimos-classic','invited:7','classic@example.com') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,identity_id,name,email,roles) VALUES($1,'person',$2,'old-classic','classic@example.com',ARRAY['admin']) RETURNING id::text`, f.p.TenantID, identity).Scan(&alias); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,email) VALUES('https://id.example','new-person','classic@example.com') RETURNING id::text`).Scan(&signin)
	})
	f.event("import.user_created", time.Now(), nil, map[string]any{"principal": map[string]any{"id": alias}, "classic": map[string]any{"source_id": "invited", "username": "old-classic"}})
	f.event("import.comment", time.Now(), nil, map[string]any{"classic_ref": "invited:import.comment:1", "record": map[string]any{"id": "1", "author_id": json.Number("7"), "body": "earlier comment"}})
	f.event("import.comment", time.Now(), nil, map[string]any{"classic_ref": "invited:import.comment:2", "record": map[string]any{"id": "2", "author": "old-classic", "body": "earlier named comment"}})
	var person tenant.Principal
	f.tx(func(tx pgx.Tx) error {
		hash := sha256.Sum256([]byte("timeline-invite"))
		if _, err := tx.Exec(t.Context(), `INSERT INTO invites(tenant_id,email,workspace_role_id,token_hash,expires_at,created_by)
			SELECT $1,'classic@example.com',id,$2,now()+interval '7 days',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, f.p.TenantID, hash[:], f.p.ID); err != nil {
			return err
		}
		var err error
		person, err = authz.AcceptInvite(t.Context(), tx, f.p.TenantID, signin, "classic@example.com", "New Person", "timeline-invite")
		return err
	})
	page, err := (&module{pool: f.d.App}).read(dbtest.Seed(t.Context()), person, f.node, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("classic history: %+v", page.Items)
	}
	for _, item := range page.Items {
		if item.Author.ID == nil || *item.Author.ID != person.ID || item.Author.Name != "New Person" {
			t.Fatalf("classic author: %+v", item.Author)
		}
	}
}

func TestLinkedActivityAuthorsAndCommentWrites(t *testing.T) {
	f := setup(t)
	var alias string
	f.tx(func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject) VALUES('paimos-classic','source:7') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,identity_id) VALUES($1,'person','mba',$2) RETURNING id::text`, f.p.TenantID, identity).Scan(&alias)
	})
	f.event("import.user_created", time.Now(), nil, map[string]any{"principal": map[string]any{"id": alias}, "classic": map[string]any{"source_id": "source", "username": "mba"}})
	f.event("import.comment", time.Now(), nil, map[string]any{"classic_ref": "source:import.comment:1", "record": map[string]any{"id": "1", "author_id": json.Number("7"), "body": "import by id"}})
	f.event("import.comment", time.Now(), nil, map[string]any{"classic_ref": "source:import.comment:2", "record": map[string]any{"id": "2", "author": "mba", "body": "import by name"}})
	caller := f.p
	caller.ID = alias
	m := &module{pool: f.d.App}
	old, err := m.writeComment(dbtest.Seed(t.Context()), caller, f.node, 0, "old native", false)
	if err != nil {
		t.Fatal(err)
	}
	service := principallink.New(f.d.App)
	if _, err := service.Link(t.Context(), "activity", alias, f.p.ID); err != nil {
		t.Fatal(err)
	}
	page, err := m.read(dbtest.Seed(t.Context()), f.p, f.node, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("items %+v", page.Items)
	}
	for _, item := range page.Items {
		if item.Author.ID == nil || *item.Author.ID != f.p.ID || item.Author.Name != f.p.Name {
			t.Fatalf("unresolved author %+v", item)
		}
	}
	fresh, err := m.writeComment(dbtest.Seed(t.Context()), caller, f.node, 0, "new canonical", false)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Author.ID == nil || *fresh.Author.ID != f.p.ID {
		t.Fatalf("write author %+v", fresh)
	}
	f.tx(func(tx pgx.Tx) error {
		var actor string
		if err := tx.QueryRow(t.Context(), `SELECT actor_principal_id::text FROM events WHERE tenant_id=$1 AND id=$2`, f.p.TenantID, fresh.ID).Scan(&actor); err != nil {
			return err
		}
		if actor != f.p.ID {
			return fmt.Errorf("stored alias actor")
		}
		return nil
	})
	// Linking preserves ownership of a recent comment whose immutable event
	// still names the source principal.
	var oldID int64
	if _, err := fmt.Sscan(old.ID, &oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.writeComment(dbtest.Seed(t.Context()), f.p, f.node, oldID, "edited by canonical owner", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Unlink(t.Context(), "activity", alias); err != nil {
		t.Fatal(err)
	}
	page, err = m.read(dbtest.Seed(t.Context()), f.p, f.node, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		want := alias
		if item.ID == fresh.ID {
			want = f.p.ID
		}
		if item.Author.ID == nil || *item.Author.ID != want {
			t.Fatalf("unlink author %+v", item)
		}
	}
}
