// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type StatusList struct {
	Items      []Status       `json:"items"`
	Next       string         `json:"next_cursor,omitempty"`
	Counts     map[string]int `json:"counts"`
	Scanned    int            `json:"scanned"`
	Incomplete bool           `json:"incomplete"`
	AtLeast    bool           `json:"at_least"`
}

// List is authenticated and instance/tenant local. Both paging and the
// aggregate scan are bounded; counts never imply completeness beyond 1,000
// visible projects. Refused/deleted projects stay in the operator inventory.
func (s *Service) List(ctx context.Context, p tenant.Principal, state, cursor string, limit int, includeDeleted bool) (StatusList, error) {
	out := StatusList{Items: []Status{}, Counts: map[string]int{}}
	switch state {
	case "", "pending", "checking", "backing_up", "applying", "adopted", "refused", "retry_wait":
	default:
		return out, errors.New("invalid adoption state")
	}
	if limit < 1 || limit > 50 || cursor != "" && !uuidRE.MatchString(cursor) {
		return out, errors.New("invalid adoption page")
	}
	if cursor == "" {
		cursor = "00000000-0000-0000-0000-000000000000"
	}
	err := s.snapshot(ctx, p, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		if includeDeleted {
			var person bool
			if err := tx.QueryRow(ctx, `SELECT kind='person' AND status='active' FROM principals WHERE id=$1`, p.ID).Scan(&person); err != nil {
				return err
			}
			if !person || p.Kind != tenant.Person {
				return authz.ErrForbidden
			}
			if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
				return err
			}
		}
		ids := []string{}
		rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project' AND ($1 OR n.deleted_at IS NULL) ORDER BY n.id LIMIT 1001`, includeDeleted)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		out.Incomplete = len(ids) > 1000
		out.AtLeast = out.Incomplete
		for _, id := range ids[:min(len(ids), 1000)] {
			if err = authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: id}); err != nil {
				if errors.Is(err, authz.ErrForbidden) {
					continue
				}
				return err
			}
			var status string
			if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT state FROM delivery_adoption_jobs WHERE project_node_id=$1 AND instance_id=$2),'pending')`, id, s.cfg.Instance).Scan(&status); err != nil {
				return err
			}
			out.Counts[status]++
			out.Scanned++
		}
		// A page scans at most 200 visible candidates. A cursor is advanced even
		// when state/permission filters yield a short page, avoiding hidden scans.
		ids = nil
		rows, err = tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id LEFT JOIN delivery_adoption_jobs j ON j.tenant_id=n.tenant_id AND j.project_node_id=n.id AND j.instance_id=$4 WHERE k.slug='project' AND ($1 OR n.deleted_at IS NULL) AND n.id>$2::uuid AND ($3='' OR coalesce(j.state,'pending')=$3) ORDER BY n.id LIMIT 201`, includeDeleted, cursor, state, s.cfg.Instance)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for i, id := range ids[:min(len(ids), 200)] {
			if err = authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: id}); err != nil {
				if errors.Is(err, authz.ErrForbidden) {
					if i == 199 && len(ids) > 200 {
						out.Next = id
					}
					continue
				}
				return err
			}
			v, err := scanStatus(tx.QueryRow(ctx, `SELECT `+statusColumns+` FROM delivery_adoption_jobs j LEFT JOIN project_delivery d USING(tenant_id,project_node_id) WHERE j.project_node_id=$1 AND j.instance_id=$2`, id, s.cfg.Instance))
			if errors.Is(err, pgx.ErrNoRows) {
				v = Status{Project: id, Mode: "journey", State: "pending", Incomplete: true, Cleanup: "none", ReasonCode: "rollout_dependency", Reason: safeReason("rollout_dependency")}
				err = nil
			}
			if err != nil {
				return err
			}
			out.Items = append(out.Items, v)
			if len(out.Items) == limit {
				if i+1 < len(ids) {
					out.Next = id
				}
				break
			}
			if i == 199 && len(ids) > 200 {
				out.Next = id
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = delivery.ErrNotFound
	}
	return out, err
}
