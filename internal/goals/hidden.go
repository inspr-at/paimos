// SPDX-License-Identifier: AGPL-3.0-only
package goals

// Hide for me (AEON-1614): a person hides a goal or release from their own
// Goals view. Nobody else's view changes; the rows live in hidden_goals, whose
// RLS admits only the row's own person. Only a signed-in person (no API key,
// no agent) may hide or unhide, and only a goal they can read. Repeating either
// call changes nothing. The Goals list leaves hidden goals out unless asked for
// hidden=include, marks them hidden, and counts them in hidden_count.

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type HiddenResult struct {
	GoalID  string `json:"goal_id"`
	Key     string `json:"key"`
	Hidden  bool   `json:"hidden"`
	Changed bool   `json:"changed"`
}

// hidingPerson is a person acting for themselves: no agent, no API key of any kind.
func hidingPerson(p tenant.Principal, r *http.Request) bool {
	return p.Kind == tenant.Person && p.KeyID == "" && p.AuthKeyID == "" && p.KeyCreatorID == "" && p.KeyPersonID == "" && r.Header.Get("Authorization") == ""
}

func (m *module) setHidden(hidden bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requestPrincipal(w, r)
		if !ok {
			return
		}
		if !hidingPerson(p, r) {
			respond(w, nil, fail(http.StatusForbidden, "only a person can hide or unhide goals in their own view"))
			return
		}
		identity := r.PathValue("goalId")
		if !nodeIdentity(identity) {
			respond(w, nil, invalid("goalId", "must be a goal key or UUID"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var out HiddenResult
		err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			var e error
			out, e = setHiddenTx(ctx, tx, p, identity, hidden)
			return e
		})
		respond(w, out, err)
	}
}

// setHiddenTx checks the goal and the person's read access inside the write.
// The shared tenant fences (as reads take them) conflict with access changes,
// so a revoked grant cannot slip between the check and the row.
func setHiddenTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, identity string, hidden bool) (HiddenResult, error) {
	var out HiddenResult
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR SHARE`, p.TenantID); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1::text,0))`, p.TenantID); err != nil {
		return out, err
	}
	if err := db.EnterPrincipal(ctx, tx, p.TenantID, p.ID, nil, nil); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
		return out, err
	}
	n, err := resolveNode(ctx, tx, identity, false)
	if err != nil {
		return out, err
	}
	if n.Deleted || !IsGoalKind(n.Slug) {
		return out, pgx.ErrNoRows
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: n.Project}); err != nil {
		return out, err
	}
	query := `INSERT INTO hidden_goals(tenant_id,principal_id,goal_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`
	if !hidden {
		query = `DELETE FROM hidden_goals WHERE tenant_id=$1 AND principal_id=$2 AND goal_id=$3`
	}
	tag, err := tx.Exec(ctx, query, p.TenantID, p.ID, n.ID)
	if err != nil {
		return out, err
	}
	return HiddenResult{GoalID: n.ID, Key: n.Key, Hidden: hidden, Changed: tag.RowsAffected() > 0}, nil
}

// hiddenAmong returns which of ids the calling person hid. ids is the bounded
// candidate set of one list read, so the lookup is bounded too.
func hiddenAmong(ctx context.Context, tx pgx.Tx, p tenant.Principal, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if p.Kind != tenant.Person || len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT goal_id::text FROM hidden_goals WHERE tenant_id=$1 AND principal_id=$2 AND goal_id=ANY($3::uuid[])`, p.TenantID, p.ID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
