// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"bytes"
	"context"
	"slices"

	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// preserveChatOwnership validates and extends lasting native-context ownership
// during creation and alias replay, including registrations never chat-bound.
// The caller has resolved the final owner and holds tenant -> hierarchy ->
// session locks. Run before event writes; conflicts roll back the registration,
// alias, row version and claims together.
func preserveChatOwnership(ctx context.Context, tx pgx.Tx, s Session) error {
	// A shared agent or a changed registration owner cannot see historical
	// person-owned rows normally. Open only this verified session's context,
	// including the newly stored alias, and restore it before other work.
	var previous string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.chat_session_id',true),'')`).Scan(&previous); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.chat_session_id',$1,true)`, s.ID); err != nil {
		return err
	}
	type ownership struct{ role, person, project string }
	// The immutable registration snapshot survives handover. Either native
	// reference may also carry ownership from an earlier registration. Three
	// unique-key lookups bound this read, including unbound replacements.
	rows, err := tx.Query(ctx, `SELECT role_id::text,owner_person_id::text,project_id::text
 FROM chat_session_contexts WHERE session_id=$1
 UNION ALL
 SELECT role_id::text,owner_person_id::text,project_id::text
 FROM chat_native_contexts WHERE harness=$2 AND ref_digest IN ($3,$4)`, s.ID, s.Harness, s.refDigest, s.vendorRefDigest)
	if err != nil {
		return err
	}
	history, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ownership, error) {
		var owner ownership
		err := row.Scan(&owner.role, &owner.person, &owner.project)
		return owner, err
	})
	if err != nil {
		return err
	}
	if len(history) > 0 {
		owner := history[0]
		if s.ownerID == nil || owner.person != *s.ownerID || owner.project != s.ProjectID {
			return workorders.Fail(409, "chat binding unavailable")
		}
		for _, prior := range history[1:] {
			if prior != owner {
				return workorders.Fail(409, "chat binding unavailable")
			}
		}
		refs := [][]byte{s.refDigest}
		if len(s.vendorRefDigest) != 0 && !bytes.Equal(s.refDigest, s.vendorRefDigest) {
			refs = append(refs, s.vendorRefDigest)
		}
		slices.SortFunc(refs, bytes.Compare)
		for _, ref := range refs {
			// No UPDATE: historical ownership is immutable even when its owner
			// is RLS-hidden. The tenant fence serializes claims across projects.
			if _, err = tx.Exec(ctx, `INSERT INTO chat_native_contexts(tenant_id,harness,ref_digest,role_id,owner_person_id,project_id)
 SELECT tenant_id,harness,$2,$3,$4,$5 FROM harness_sessions WHERE id=$1
 ON CONFLICT (tenant_id,harness,ref_digest) DO NOTHING`, s.ID, ref, owner.role, owner.person, owner.project); err != nil {
				return err
			}
			var same bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_native_contexts WHERE harness=$1 AND ref_digest=$2
 AND role_id=$3 AND owner_person_id=$4 AND project_id=$5)`, s.Harness, ref, owner.role, owner.person, owner.project).Scan(&same); err != nil {
				return err
			}
			if !same {
				return workorders.Fail(409, "chat binding unavailable")
			}
		}
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.chat_session_id',$1,true)`, previous)
	return err
}
