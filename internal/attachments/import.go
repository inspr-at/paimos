// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AttachStagedTx publishes one staged file onto a node inside the caller's
// transaction (AEON-1441: the START transcript on its new project). It runs
// the upload path's attachments.write check in the node's current project,
// takes the blob locks and writes the row. The attachment.added change comes
// back for the caller to append last. The caller holds the tree fence, stages
// the bytes before the transaction and closes staged afterwards.
func AttachStagedTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID, name string, staged *Staged) (Attachment, events.Change, error) {
	if err := requireNodeWrite(ctx, tx, p, nodeID, "attachments.write"); err != nil {
		return Attachment{}, events.Change{}, err
	}
	if err := Publish(ctx, tx, OwnerAttachment, staged); err != nil {
		return Attachment{}, events.Change{}, err
	}
	var pos int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(ceil(max(position)),0)::bigint FROM attachments WHERE tenant_id=$1 AND node_id=$2`, p.TenantID, nodeID).Scan(&pos); err != nil {
		return Attachment{}, events.Change{}, err
	}
	a, err := scan(tx.QueryRow(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,width,height,caption,position,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'',$9,$10) RETURNING `+columns,
		p.TenantID, nodeID, staged.SHA256, cleanName(name), staged.ContentType, staged.Size, optionalDimension(staged.Width), optionalDimension(staged.Height), pos+1, p.ID))
	if err != nil {
		return Attachment{}, events.Change{}, err
	}
	return a, events.Change{NodeID: &nodeID, Type: "attachment.added", After: a}, nil
}
