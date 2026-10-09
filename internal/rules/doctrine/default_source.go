// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// EnsureDefaultSource is the sole non-person source registration path. Its
// authority comes from explicit host policy, never a request principal. It
// preserves tenant-registered sources and emits source_added only on insertion.
func (m *Module) EnsureDefaultSource(ctx context.Context) error {
	if m == nil || m.pool == nil || !m.defaultSource || !workorders.UUID(m.app.TenantID) || strings.ToLower(m.app.TenantID) != m.app.TenantID {
		return nil
	}
	tid, repository := m.app.TenantID, m.repositories.Private()
	ref := "github-app"
	if m.credentials.MirrorDir != "" {
		ref = "host-mirror"
	}
	var existing Source
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		var err error
		existing, err = scanSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM doctrine_sources WHERE repository=$1`, repository))
		return err
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// A person's source keeps its existing credential, paths and pin.
	if err == nil && (existing.CredentialRef != ref || existing.Visibility != "private") {
		return nil
	}
	if err := m.credentials.authorize(ref, tid, repository); err != nil {
		return err
	}
	var pin Commit
	if err != nil {
		pin, err = m.pin(ctx, tid, SourceInput{Repository: repository, Visibility: "private", Ref: "main", CredentialRef: ref})
		if err != nil {
			return err
		}
	}
	var actor tenant.Principal
	var source Source
	// Provision System before the source transaction: Ensure may append its own
	// principal event, and the event counter must be the final lock in that tx.
	err = db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		var err error
		actor, err = systemactor.Ensure(ctx, tx, tid)
		return err
	})
	if err != nil {
		return err
	}
	err = m.defaultSourceTx(ctx, tid, ref, func(tx pgx.Tx) error {
		var err error
		source, err = scanSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM doctrine_sources WHERE repository=$1 FOR UPDATE`, repository))
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM doctrine_sources`).Scan(&count); err != nil {
			return err
		}
		if count >= MaxSources {
			return fail(409, "conflict", "a workspace indexes at most 8 doctrine repositories")
		}
		if !shaPattern.MatchString(pin.SHA) {
			return gitFail("the host default source could not be pinned")
		}
		source, err = insertSource(ctx, tx, tid, Source{Repository: repository, Visibility: "private", Ref: "main", Commit: pin.SHA, CommittedAt: pin.CommittedAt, Paths: DefaultPaths, CredentialRef: ref})
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, actor, events.Change{Type: "doctrine.source_added", After: source.audit()})
		return err
	})
	if err != nil {
		return err
	}
	if source.CredentialRef != ref || source.Visibility != "private" || source.IndexedAt != nil && source.IndexError == "" && m.credentials.authorizeSource(source, tid) == nil {
		return nil
	}
	return m.indexWith(ctx, actor, source.ID, func(fn func(pgx.Tx) error) error { return m.defaultSourceTx(ctx, tid, ref, fn) })
}

func (m *Module) defaultSourceTx(ctx context.Context, tid, ref string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if !m.defaultSource || tid != m.app.TenantID {
			return ErrCredential
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','3s',true), set_config('statement_timeout','10s',true)`); err != nil {
			return err
		}
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, tid).Scan(&locked); err != nil {
			return err
		}
		if err := m.credentials.authorize(ref, tid, m.repositories.Private()); err != nil {
			return err
		}
		return fn(tx)
	})
}
