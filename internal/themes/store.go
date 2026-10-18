// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Store struct{ Pool *pgxpool.Pool }

// Theme writes share the existing authority fence: tree -> tenant -> record
// rows -> event counter last. Pairing is not involved. SHARE fences membership
// and role revocation while remaining compatible with tenant FK key-share locks.
func (s Store) transaction(ctx context.Context, p tenant.Principal, write bool, fn func(context.Context, pgx.Tx) error) error {
	if !validUUID(p.ID) || !validUUID(p.TenantID) || (p.Kind != tenant.Person && p.Kind != tenant.Agent) {
		return authz.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 15*time.Second)
	defer cancel()
	return db.InTenant(ctx, s.Pool, p.TenantID, func(tx pgx.Tx) error {
		if write {
			if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		return fn(ctx, tx)
	})
}

func selfPermission(ctx context.Context, tx pgx.Tx, p tenant.Principal, action string) error {
	err := authz.RequireTx(ctx, tx, p, "profile."+action, authz.Scope{AnyProject: true})
	if err == nil || !errors.Is(err, authz.ErrForbidden) {
		return err
	}
	return authz.RequireTx(ctx, tx, p, "profile.portal_"+action, authz.Scope{AnyProject: true})
}
func canonical(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1 AND id=$2 AND status='active'`, p.TenantID, p.ID).Scan(&id)
	return id, err
}
func writable(ctx context.Context, tx pgx.Tx, p tenant.Principal, scope string, owner *string) error {
	// settings.manage is person-only in the permission registry. Personal
	// themes/choices belong to the person, never to an agent's key creator.
	if p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	if scope != "personal" {
		return authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{})
	}
	if err := selfPermission(ctx, tx, p, "write"); err != nil {
		return err
	}
	id, err := canonical(ctx, tx, p)
	if err != nil {
		return err
	}
	if owner == nil {
		return authz.ErrForbidden
	}
	var owns bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1 AND id=$2
		AND kind='person' AND status='active' AND coalesce(linked_to,id)=$3::uuid)`, p.TenantID, *owner, id).Scan(&owns); err != nil {
		return err
	}
	if !owns {
		return authz.ErrForbidden
	}
	return nil
}

const columns = `id::text,tenant_id::text,name,scope,owner_principal_id::text,config,revision,created_at,updated_at,deleted_at`

func scan(row pgx.Row) (Theme, error) {
	var t Theme
	var raw []byte
	err := row.Scan(&t.ID, &t.TenantID, &t.Name, &t.Scope, &t.OwnerPrincipalID, &raw, &t.Revision, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if err != nil {
		return t, err
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	if t.DeletedAt != nil {
		deleted := t.DeletedAt.UTC()
		t.DeletedAt = &deleted
	}
	err = json.Unmarshal(raw, &t.Values)
	return t, err
}
func read(ctx context.Context, tx pgx.Tx, tenantID, id string, deleted, lock bool) (Theme, error) {
	q := `SELECT ` + columns + ` FROM themes WHERE tenant_id=$1 AND id=$2`
	if !deleted {
		q += ` AND deleted_at IS NULL`
	}
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	return scan(tx.QueryRow(ctx, q, tenantID, id))
}
func save(ctx context.Context, tx pgx.Tx, t Theme) (Theme, error) {
	raw, err := json.Marshal(t.Values)
	if err != nil {
		return Theme{}, err
	}
	return scan(tx.QueryRow(ctx, `UPDATE themes SET name=$3,config=$4,revision=$5,updated_at=clock_timestamp(),deleted_at=$6
		WHERE tenant_id=$1 AND id=$2 RETURNING `+columns, t.TenantID, t.ID, t.Name, raw, t.Revision, t.DeletedAt))
}
func audience(id *string) json.RawMessage {
	if id == nil {
		return nil
	}
	raw, _ := json.Marshal(map[string]string{"audience_principal_id": *id})
	return raw
}
func themeChange(typ string, before, after *Theme) events.Change {
	t := after
	if t == nil {
		t = before
	}
	return events.Change{Type: typ, Before: before, After: after, Metadata: audience(t.OwnerPrincipalID)}
}

func (s Store) List(ctx context.Context, p tenant.Principal, after string, limit int) (Page, error) {
	out := Page{Items: []Theme{}}
	if limit < 1 || limit > 100 || (after != "" && !validUUID(after)) {
		return out, ErrInvalid
	}
	err := s.transaction(ctx, p, false, func(ctx context.Context, tx pgx.Tx) error {
		if err := selfPermission(ctx, tx, p, "read"); err != nil {
			return err
		}
		var cursor any
		if after != "" {
			cursor = after
		}
		rows, err := tx.Query(ctx, `SELECT `+columns+` FROM themes WHERE tenant_id=$1 AND deleted_at IS NULL AND ($2::uuid IS NULL OR id>$2) ORDER BY id LIMIT $3`, p.TenantID, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scan(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			id := out.Items[limit-1].ID
			out.NextCursor = &id
		}
		return nil
	})
	return out, err
}
func (s Store) Get(ctx context.Context, p tenant.Principal, id string) (Theme, error) {
	var out Theme
	if !validUUID(id) {
		return out, ErrInvalid
	}
	err := s.transaction(ctx, p, false, func(ctx context.Context, tx pgx.Tx) error {
		if err := selfPermission(ctx, tx, p, "read"); err != nil {
			return err
		}
		var err error
		out, err = read(ctx, tx, p.TenantID, id, false, false)
		return err
	})
	return out, err
}
func create(ctx context.Context, tx pgx.Tx, p tenant.Principal, in CreateInput) (Theme, error) {
	var owner *string
	if in.Scope == "personal" {
		id, err := canonical(ctx, tx, p)
		if err != nil {
			return Theme{}, err
		}
		owner = &id
	}
	if err := writable(ctx, tx, p, in.Scope, owner); err != nil {
		return Theme{}, err
	}
	v := Porcelain()
	if in.Values != nil {
		v = *in.Values
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return Theme{}, err
	}
	out, err := scan(tx.QueryRow(ctx, `INSERT INTO themes(tenant_id,name,scope,owner_principal_id,config) VALUES($1,$2,$3,$4,$5) RETURNING `+columns, p.TenantID, in.Name, in.Scope, owner, raw))
	if err != nil {
		return out, err
	}
	_, err = events.Append(ctx, tx, p, themeChange("theme.created", nil, &out))
	return out, err
}
func (s Store) Create(ctx context.Context, p tenant.Principal, in CreateInput) (Theme, error) {
	var out Theme
	if !validName(in.Name) || !validScope(in.Scope) || (in.Values != nil && in.Values.validate() != nil) {
		return out, ErrInvalid
	}
	err := s.transaction(ctx, p, true, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = create(ctx, tx, p, in)
		return err
	})
	return out, err
}
func (s Store) Duplicate(ctx context.Context, p tenant.Principal, id string, in DuplicateInput) (Theme, error) {
	var out Theme
	if !validUUID(id) || !validName(in.Name) || !validScope(in.Scope) || in.Revision < 1 {
		return out, ErrInvalid
	}
	err := s.transaction(ctx, p, true, func(ctx context.Context, tx pgx.Tx) error {
		if err := selfPermission(ctx, tx, p, "read"); err != nil {
			return err
		}
		source, err := read(ctx, tx, p.TenantID, id, false, true)
		if err != nil {
			return err
		}
		if source.Revision != in.Revision {
			return ErrConflict
		}
		out, err = create(ctx, tx, p, CreateInput{Name: in.Name, Scope: in.Scope, Values: &source.Values})
		return err
	})
	return out, err
}
func (s Store) Update(ctx context.Context, p tenant.Principal, id string, in UpdateInput) (Theme, error) {
	var out Theme
	if !validUUID(id) || in.Revision < 1 || (in.Name == nil && in.Values == nil) ||
		(in.Name != nil && !validName(*in.Name)) || (in.Values != nil && in.Values.validate() != nil) {
		return out, ErrInvalid
	}
	err := s.transaction(ctx, p, true, func(ctx context.Context, tx pgx.Tx) error {
		before, err := read(ctx, tx, p.TenantID, id, false, true)
		if err != nil {
			return err
		}
		if err := writable(ctx, tx, p, before.Scope, before.OwnerPrincipalID); err != nil {
			return err
		}
		if before.Revision != in.Revision {
			return ErrConflict
		}
		out = before
		if in.Name != nil {
			out.Name = *in.Name
		}
		if in.Values != nil {
			out.Values = *in.Values
		}
		if same(out, before) {
			return nil
		}
		out.Revision++
		out, err = save(ctx, tx, out)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, themeChange("theme.updated", &before, &out))
		return err
	})
	return out, err
}
func (s Store) Delete(ctx context.Context, p tenant.Principal, id string, revision int64) error {
	if !validUUID(id) || revision < 1 {
		return ErrInvalid
	}
	return s.transaction(ctx, p, true, func(ctx context.Context, tx pgx.Tx) error {
		before, err := read(ctx, tx, p.TenantID, id, false, true)
		if err != nil {
			return err
		}
		if err := writable(ctx, tx, p, before.Scope, before.OwnerPrincipalID); err != nil {
			return err
		}
		if before.Scope == "default" || before.Revision != revision {
			return ErrConflict
		}
		after := before
		after.Revision++
		// Database time is recorded in the reversible snapshot, not guessed by
		// the client. No appearance choices or user lists are rewritten here.
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&after.DeletedAt); err != nil {
			return err
		}
		after, err = save(ctx, tx, after)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, themeChange("theme.deleted", &before, &after))
		return err
	})
}

func selection(ctx context.Context, tx pgx.Tx, tenantID, principalID string, lock bool) (Selection, error) {
	out := Selection{PrincipalID: principalID, Unsaved: true}
	// Preserve physical ownership across links. A canonical saved choice (even
	// explicit default) wins; otherwise choose the lowest saved alias UUID. An
	// undone absence retains its CAS row but never takes precedence over saved
	// choices. LIMIT keeps every identity on the same bounded winning row.
	q := `SELECT principal_id::text,theme_id::text,revision,generation,
		coalesce(unsaved_revision=revision,false) FROM theme_selections
		WHERE tenant_id=$1 AND principal_id IN (SELECT id FROM principals
			WHERE tenant_id=$1 AND kind='person' AND status='active' AND coalesce(linked_to,id)=$2::uuid)
		ORDER BY coalesce(unsaved_revision=revision,false),
		(principal_id=$2::uuid) DESC,principal_id LIMIT 1`
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	err := tx.QueryRow(ctx, q, tenantID, principalID).Scan(&out.PrincipalID, &out.ThemeID, &out.Revision, &out.generation, &out.Unsaved)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	return out, err
}
func saveSelection(ctx context.Context, tx pgx.Tx, tenantID string, s Selection) error {
	var unsavedRevision *int64
	if s.Unsaved {
		unsavedRevision = &s.Revision
	}
	_, err := tx.Exec(ctx, `INSERT INTO theme_selections(tenant_id,principal_id,theme_id,revision,unsaved_revision) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(tenant_id,principal_id) DO UPDATE SET theme_id=EXCLUDED.theme_id,revision=EXCLUDED.revision,
		unsaved_revision=EXCLUDED.unsaved_revision`, tenantID, s.PrincipalID, s.ThemeID, s.Revision, unsavedRevision)
	return err
}
func active(ctx context.Context, tx pgx.Tx, p tenant.Principal) (Active, error) {
	var out Active
	def, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM themes WHERE tenant_id=$1 AND scope='default'`, p.TenantID))
	if err != nil {
		return out, err
	}
	out.Theme, out.DefaultThemeID = def, def.ID
	if p.Kind != tenant.Person {
		return out, nil
	}
	id, err := canonical(ctx, tx, p)
	if err != nil {
		return out, err
	}
	s, err := selection(ctx, tx, p.TenantID, id, false)
	if err != nil {
		return out, err
	}
	out.SelectedThemeID, out.Revision = s.ThemeID, s.generation
	if s.ThemeID == nil {
		return out, nil
	}
	t, err := read(ctx, tx, p.TenantID, *s.ThemeID, true, false)
	if errors.Is(err, pgx.ErrNoRows) {
		// Unlink can make a previously selected personal theme private. Keep its
		// CAS revision, but never expose the inaccessible ID/name or fail loading.
		out.SelectedThemeID = nil
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if t.DeletedAt != nil {
		out.FallbackNotice = &FallbackNotice{t.ID, t.Name}
	} else {
		out.Theme = t
	}
	return out, nil
}
func (s Store) Active(ctx context.Context, p tenant.Principal) (Active, error) {
	var out Active
	err := s.transaction(ctx, p, false, func(ctx context.Context, tx pgx.Tx) error {
		if err := selfPermission(ctx, tx, p, "read"); err != nil {
			return err
		}
		var err error
		out, err = active(ctx, tx, p)
		return err
	})
	return out, err
}
func (s Store) Select(ctx context.Context, p tenant.Principal, in SelectionInput) (Active, error) {
	var out Active
	if in.Revision < 0 || in.Revision > 9007199254740991 || (in.ThemeID != nil && !validUUID(*in.ThemeID)) {
		return out, ErrInvalid
	}
	err := s.transaction(ctx, p, true, func(ctx context.Context, tx pgx.Tx) error {
		if p.Kind != tenant.Person {
			return authz.ErrForbidden
		}
		if err := selfPermission(ctx, tx, p, "write"); err != nil {
			return err
		}
		id, err := canonical(ctx, tx, p)
		if err != nil {
			return err
		}
		if in.ThemeID != nil {
			target, err := read(ctx, tx, p.TenantID, *in.ThemeID, false, true)
			if err != nil {
				return err
			}
			// Snapshot the canonical UUID spelling returned by Postgres so an
			// uppercase request cannot cause a false CAS/undo conflict later.
			in.ThemeID = &target.ID
		}
		before, err := selection(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if before.generation != in.Revision {
			return ErrConflict
		}
		after := before
		after.ThemeID = in.ThemeID
		after.Unsaved = false
		// A missing row or undone absence is not an explicit default. Persist it
		// so it can take precedence over an alias's preference after linking.
		if before.Revision > 0 && same(before, after) {
			out, err = active(ctx, tx, p)
			return err
		}
		after.Revision++
		if err := saveSelection(ctx, tx, p.TenantID, after); err != nil {
			return err
		}
		out, err = active(ctx, tx, p)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "theme.selected", Before: before, After: after, Metadata: audience(&after.PrincipalID)})
		return err
	})
	return out, err
}
