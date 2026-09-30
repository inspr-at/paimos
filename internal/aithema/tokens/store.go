// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"bytes"
	"context"
	"sync"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store serializes callbacks across all signers sharing a key set. The bytes
// are always encrypted; callbacks commit atomically or leave storage unchanged.
// Binding is the tenant owner used as AES-GCM associated data, not a JWT tid.
type Store interface {
	Binding() string
	Update(context.Context, func([]byte) ([]byte, error)) error
}

// MemoryStore is for tests and ephemeral hosts. It still stores ciphertext.
// A persistent host uses PostgresStore so rotation survives restart and replicas.
type MemoryStore struct {
	TenantID   string
	mu         sync.Mutex
	ciphertext []byte
}

func (s *MemoryStore) Binding() string { return s.TenantID }
func (s *MemoryStore) Update(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	next, err := fn(bytes.Clone(s.ciphertext))
	if err == nil {
		s.ciphertext = bytes.Clone(next)
	}
	return err
}

type PostgresStore struct {
	Pool     *pgxpool.Pool
	TenantID string
}

func (s *PostgresStore) Binding() string { return s.TenantID }
func (s *PostgresStore) Update(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	if s.Pool == nil {
		return ErrUnavailable
	}
	return db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) error {
		// The conflict lock serializes the first insert as well as later rotations.
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_signing_keys(tenant_id, encrypted_state) VALUES($1, '\x'::bytea) ON CONFLICT(tenant_id) DO NOTHING`, s.TenantID); err != nil {
			return ErrUnavailable
		}
		var old []byte
		if err := tx.QueryRow(ctx, `SELECT encrypted_state FROM aithema_signing_keys WHERE tenant_id=$1 FOR UPDATE`, s.TenantID).Scan(&old); err != nil {
			return ErrUnavailable
		}
		next, err := fn(old)
		if err != nil {
			return err
		}
		if bytes.Equal(old, next) {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE aithema_signing_keys SET encrypted_state=$2 WHERE tenant_id=$1`, s.TenantID, next)
		if err != nil {
			return ErrUnavailable
		}
		return nil
	})
}
