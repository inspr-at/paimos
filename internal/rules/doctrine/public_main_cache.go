// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const publicMainMaxAge = 5 * time.Minute

type publicMainSnapshot struct {
	Commit     string
	Tree       []Entry
	ObservedAt time.Time
}

func (m *Module) readPublicMain(ctx context.Context, tenantID string, s Source) *publicMainSnapshot {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	r, err := m.reader(tenantID, s.Repository, s.CredentialRef)
	if err != nil {
		return nil
	}
	observed := time.Now()
	main, err := r.Commit(ctx, s.Repository, "main")
	if err != nil {
		return nil
	}
	tree, err := r.Tree(ctx, s.Repository, main.SHA)
	if err != nil || len(tree) == 0 {
		return nil
	}
	return &publicMainSnapshot{Commit: main.SHA, Tree: tree, ObservedAt: observed}
}

func storePublicMain(ctx context.Context, tx pgx.Tx, tenantID string, s Source, main *publicMainSnapshot) error {
	if main == nil {
		_, err := tx.Exec(ctx, `DELETE FROM doctrine_public_main_cache WHERE source_id=$1`, s.ID)
		return err
	}
	raw, err := json.Marshal(main.Tree)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO doctrine_public_main_cache(tenant_id,source_id,pin_commit,main_commit,observed_at,tree)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_id,source_id) DO UPDATE SET pin_commit=EXCLUDED.pin_commit, main_commit=EXCLUDED.main_commit, observed_at=EXCLUDED.observed_at, tree=EXCLUDED.tree`, tenantID, s.ID, s.Commit, main.Commit, main.ObservedAt, raw)
	return err
}

// checkPrivateQuotes first matches the private corpus with NO public exception.
// No hit needs no main cache and performs no guard-related network I/O. On a
// hit, only a recent independently resolved main tree can exempt cached blobs.
// Missing/stale/malformed caches refuse; this path never refreshes them.
func (m *Module) checkPrivateQuotes(ctx context.Context, actor tenant.Principal, s Source, files []File, guard *guardCorpus, texts ...string) (string, error) {
	var exemptMain string
	err := withGuardSlot(ctx, func() error {
		err := guardPrivateQuotes(guard, nil, texts...)
		if err == nil {
			return nil
		}
		var refusal *failure
		if !errors.As(err, &refusal) || refusal.Code != "private_doctrine" {
			return err
		}
		var cached publicMainSnapshot
		var raw []byte
		err = m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT c.main_commit,c.observed_at,c.tree FROM doctrine_public_main_cache c
				JOIN doctrine_sources s ON s.tenant_id=c.tenant_id AND s.id=c.source_id
				WHERE c.source_id=$1 AND c.pin_commit=$2 AND s.commit_sha=c.pin_commit AND s.index_error='' AND s.indexed_at IS NOT NULL`, s.ID, s.Commit).Scan(&cached.Commit, &cached.ObservedAt, &raw)
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		age := time.Since(cached.ObservedAt)
		if err != nil || age < 0 || age > publicMainMaxAge || !shaPattern.MatchString(cached.Commit) || json.Unmarshal(raw, &cached.Tree) != nil || len(cached.Tree) == 0 {
			return fail(422, "public_main_unavailable", "The public main cache is missing or stale. Reindex the public source before proposing text shared with private doctrine. Nothing was published.")
		}
		if err := guardPrivateQuotes(guard, mainMatchingFiles(files, cached.Tree), texts...); err != nil {
			return err
		}
		exemptMain = cached.Commit
		return nil
	})
	return exemptMain, err
}
