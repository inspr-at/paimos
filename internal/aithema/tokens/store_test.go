// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestPostgresPersistenceIsolationAndReplicaRotation(t *testing.T) {
	database := dbtest.Open(t)
	var owner, other string
	for slug, target := range map[string]*string{"signing-owner": &owner, "other-owner": &other} {
		if err := database.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Issuer: "https://host.example", Audience: "host.example", Clock: func() time.Time { return time.Unix(fixtureNow, 0) }}
	store := &PostgresStore{Pool: database.App, TenantID: owner}
	master := bytes.Repeat([]byte{7}, 32)
	keys, err := New(t.Context(), store, master, cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := activeKey(t, keys)
	minted, err := keys.MintSession(t.Context(), fixtureClaims(t, Session))
	if err != nil {
		t.Fatal(err)
	}
	replica, err := New(t.Context(), &PostgresStore{Pool: database.App, TenantID: owner}, master, cfg)
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := keys.JWKS(t.Context())
	loaded, _ := replica.JWKS(t.Context())
	if initial.Keys[0].ID != loaded.Keys[0].ID {
		t.Fatal("restart lost keys")
	}
	var encrypted []byte
	if err := db.InTenant(t.Context(), database.App, owner, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT encrypted_state FROM aithema_signing_keys WHERE tenant_id=$1`, owner).Scan(&encrypted)
	}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, old.Seed) || bytes.Contains(encrypted, []byte(`"active"`)) {
		t.Fatal("plaintext seed stored in Postgres")
	}
	if _, err := New(t.Context(), store, bytes.Repeat([]byte{8}, 32), cfg); err == nil {
		t.Fatal("wrong host secret accepted")
	}
	if err := db.InTenant(t.Context(), database.App, other, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM aithema_signing_keys`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("cross tenant key state visible")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(t.Context(), database.App, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO aithema_signing_keys(tenant_id,encrypted_state) VALUES($1,$2)`, owner, encrypted)
		return err
	}); err == nil {
		t.Fatal("RLS allowed cross tenant key write")
	}
	var wg sync.WaitGroup
	for _, signer := range []*KeySet{keys, replica} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				if err := signer.Rotate(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	rotated, err := replica.JWKS(t.Context())
	if err != nil || len(rotated.Keys) != 8 {
		t.Fatalf("replica rotation lost keys: count %d err %v", len(rotated.Keys), err)
	}
	if _, err := replica.VerifySession(t.Context(), minted); err != nil {
		t.Fatal("pre-rotation token failed across replica")
	}
	if err := db.InTenant(t.Context(), database.App, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO aithema_signing_keys(tenant_id,encrypted_state) VALUES($1,$2)`, other, encrypted)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.Context(), &PostgresStore{Pool: database.App, TenantID: other}, master, cfg); err == nil {
		t.Fatal("copied ciphertext accepted under different owner")
	}
	// No tenant setting exposes no signing rows, including pooled connections
	// that previously entered a tenant transaction.
	var count int
	if err := database.App.QueryRow(t.Context(), `SELECT count(*) FROM aithema_signing_keys`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unscoped signing rows: count %d err %v", count, err)
	}
}
