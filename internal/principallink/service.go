// SPDX-License-Identifier: AGPL-3.0-only
package principallink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/principallink/apply"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type Person struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Email    *string `json:"email"`
	LinkedTo *string `json:"linked_to"`
}
type Result struct {
	Person  Person `json:"person"`
	Changed bool   `json:"changed"`
}

// Resolve returns a canonical principal within the caller's db.InTenant
// transaction. It preserves agents and does not change the source's classic
// role labels; authorization resolves the canonical person's role binding.
func Resolve(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, string, error) {
	var canonical, name string
	err := tx.QueryRow(ctx, `SELECT coalesce(t.id,p.id)::text,coalesce(t.name,p.name)
 FROM principals p LEFT JOIN principals t ON t.tenant_id=p.tenant_id AND t.id=p.linked_to
 WHERE p.tenant_id=$1 AND p.id=$2 FOR SHARE OF p`, tenantID, id).Scan(&canonical, &name)
	return canonical, name, err
}

func (s *Service) Link(ctx context.Context, slug, from, to string) (Result, error) {
	if strings.TrimSpace(to) == "" {
		return Result{}, errors.New("to is required")
	}
	return s.change(ctx, slug, from, to)
}
func (s *Service) Unlink(ctx context.Context, slug, from string) (Result, error) {
	return s.change(ctx, slug, from, "")
}

// LinkTx links or unlinks inside the caller's tenant transaction. from and to
// are principal ids or unique names. An empty to unlinks. actorID is stored on
// the event; eventLinked and eventUnlinked select the event type. Linking
// deletes the source's role bindings.
func LinkTx(ctx context.Context, tx pgx.Tx, tenantID, from, to, actorID, eventLinked, eventUnlinked string) (Result, error) {
	out, err := apply.Apply(ctx, tx, tenantID, from, to, actorID)
	if err != nil {
		return Result{}, err
	}
	result := Result{Person: Person{ID: out.Person.ID, Name: out.Person.Name, Email: out.Person.Email, LinkedTo: out.Person.LinkedTo}, Changed: out.Changed}
	if !out.Changed {
		return result, nil
	}
	typ := eventLinked
	if to == "" {
		typ = eventUnlinked
	}
	if err := appendRaw(ctx, tx, tenantID, actorID, typ, out.Before, out.After); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) change(ctx context.Context, slug, from, to string) (Result, error) {
	// Operator CLI, no principal: workspace rows only (ADR-003 P2).
	ctx = db.NoProjects(ctx, "principal link")
	var result Result
	if strings.TrimSpace(from) == "" {
		return result, errors.New("from is required")
	}
	tid, err := tenantbootstrap.ResolveSlug(ctx, s.pool, slug)
	if err != nil {
		return result, err
	}
	err = db.InTenant(ctx, s.pool, tid, func(tx pgx.Tx) error {
		actor, err := operator(ctx, tx, tid)
		if err != nil {
			return err
		}
		result, err = LinkTx(ctx, tx, tid, from, to, actor, "principal.linked", "principal.unlinked")
		return err
	})
	if err != nil {
		return Result{}, fmt.Errorf("principal link: %w", err)
	}
	return result, nil
}
func operator(ctx context.Context, tx pgx.Tx, tid string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE tenant_id=$1 AND kind='agent' AND name='Principal link operator' AND roles=ARRAY['operator']::text[] ORDER BY id LIMIT 1`, tid).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	var after json.RawMessage
	err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent','Principal link operator',ARRAY['operator']) RETURNING id::text,to_jsonb(principals)`, tid).Scan(&id, &after)
	if err != nil {
		return "", err
	}
	err = appendRaw(ctx, tx, tid, id, "principal.created", nil, after)
	return id, err
}

// appendRaw writes one event without importing internal/events. That import
// would cycle through authz, which calls LinkTx.
func appendRaw(ctx context.Context, tx pgx.Tx, tenantID, actorID, typ string, before, after json.RawMessage) error {
	if len(before) == 0 && len(after) == 0 {
		return errors.New("principal link event needs a snapshot")
	}
	var old, next any
	if len(before) > 0 {
		old = []byte(before)
	}
	if len(after) > 0 {
		next = []byte(after)
	}
	_, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,before,after,at)
		VALUES($1::uuid,$2::uuid,$3,$4::jsonb,$5::jsonb,clock_timestamp())`, tenantID, actorID, typ, old, next)
	return err
}

type Suggestion struct {
	From   Person `json:"from"`
	To     Person `json:"to"`
	Reason string `json:"reason"`
}

func (s *Service) Suggest(ctx context.Context, slug string) ([]Suggestion, error) {
	out := []Suggestion{}
	tid, err := tenantbootstrap.ResolveSlug(ctx, s.pool, slug)
	if err != nil {
		return nil, err
	}
	err = db.InTenant(ctx, s.pool, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH people AS (
   SELECT p.id,p.name,coalesce(nullif(p.email,''),i.email) email,i.issuer
   FROM principals p JOIN identities i ON i.id=p.identity_id
   WHERE p.tenant_id=$1 AND p.kind='person' AND p.linked_to IS NULL
  ) SELECT a.id::text,a.name,a.email,b.id::text,b.name,b.email,
  CASE WHEN lower(trim(a.email))=lower(trim(b.email)) THEN 'same_email' ELSE 'username_email_local_part' END
  FROM people a JOIN people b ON a.id<>b.id
  WHERE a.issuer='paimos-classic' AND b.issuer<>'paimos-classic'
   AND nullif(trim(b.email),'') IS NOT NULL AND strpos(b.email,'@')>1
   AND (lower(trim(a.email))=lower(trim(b.email)) OR lower(trim(a.name))=lower(split_part(trim(b.email),'@',1)))
  ORDER BY a.name,a.id,b.name,b.id`, tid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Suggestion
			if err := rows.Scan(&v.From.ID, &v.From.Name, &v.From.Email, &v.To.ID, &v.To.Name, &v.To.Email, &v.Reason); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
