// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type AttachmentDeltaReport struct {
	Created   int              `json:"created"`
	Updated   int              `json:"updated"`
	Conflicts []ImportConflict `json:"conflicts"`
}

// ImportAttachmentDelta refreshes existing imported bytes when classic changes,
// then copies newly discovered attachments. It compares the current row to its
// last importer event before updating, so Aeon-side edits remain untouched.
func ImportAttachmentDelta(ctx context.Context, pool *pgxpool.Pool, store attachments.Store, source *HTTPSource, snap Snapshot, tenantID, actorID string) (AttachmentDeltaReport, error) {
	report := AttachmentDeltaReport{Conflicts: []ImportConflict{}}
	if pool == nil || source == nil || snap.SourceID == "" || source.InstanceID() != snap.SourceID || tenantID == "" || actorID == "" {
		return report, errors.New("attachment delta requires matching source, tenant, actor and pool")
	}
	for issueID, detail := range snap.Details {
		for _, record := range detail.Attachments {
			classicID, ok := intField(record, "id")
			if !ok || classicID < 1 {
				return report, fmt.Errorf("issue %d attachment missing id", issueID)
			}
			var attachmentID, oldHash string
			err := db.InTenant(db.AllProjects(ctx, "classic importer"), pool, tenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT id::text,sha256 FROM attachments WHERE tenant_id=$1 AND source_id=$2 AND source_attachment_id=$3`, tenantID, snap.SourceID, classicID).Scan(&attachmentID, &oldHash)
			})
			if errors.Is(err, pgx.ErrNoRows) {
				continue // ImportAttachments creates this row below.
			}
			if err != nil {
				return report, err
			}
			body, err := source.downloadAttachment(ctx, classicID)
			if isNotFound(err) {
				report.Conflicts = append(report.Conflicts, ImportConflict{ClassicID: classicID, Key: "attachment:" + strconv.FormatInt(classicID, 10), Reason: "classic attachment bytes are missing"})
				continue
			}
			if err != nil {
				return report, err
			}
			blob, putErr := store.Stage(ctx, tenantID, body)
			closeErr := body.Close()
			if putErr != nil {
				return report, fmt.Errorf("store classic attachment %d: %w", classicID, putErr)
			}
			if closeErr != nil {
				_ = blob.Close()
				return report, closeErr
			}
			if blob.SHA256 == oldHash {
				_ = blob.Close()
				continue
			}
			updated := false
			conflict := false
			err = db.InTenant(db.AllProjects(ctx, "classic importer"), pool, tenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, tenantID+":"+snap.SourceID+":"+strconv.FormatInt(classicID, 10)); err != nil {
					return err
				}
				var before, baseline []byte
				if err := tx.QueryRow(ctx, `SELECT to_jsonb(a) FROM attachments a WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, attachmentID).Scan(&before); err != nil {
					return err
				}
				err := tx.QueryRow(ctx, `SELECT after FROM events WHERE tenant_id=$1 AND type IN ('attachment.added','attachment.updated') AND after->>'id'=$2 ORDER BY id DESC LIMIT 1`, tenantID, attachmentID).Scan(&baseline)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if divergedAttachment(before, baseline) {
					conflict = true
					return nil
				}
				var currentHash string
				if err := tx.QueryRow(ctx, `SELECT sha256 FROM attachments WHERE tenant_id=$1 AND id=$2`, tenantID, attachmentID).Scan(&currentHash); err != nil {
					return err
				}
				if currentHash == blob.SHA256 {
					return nil
				}
				if err := attachments.Publish(ctx, tx, attachments.OwnerAttachment, blob); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE attachments SET sha256=$3,content_type=$4,size=$5,width=$6,height=$7,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, tenantID, attachmentID, blob.SHA256, blob.ContentType, blob.Size, optionalDimension(blob.Width), optionalDimension(blob.Height)); err != nil {
					return err
				}
				var after []byte
				if err := tx.QueryRow(ctx, `SELECT to_jsonb(a) FROM attachments a WHERE tenant_id=$1 AND id=$2`, tenantID, attachmentID).Scan(&after); err != nil {
					return err
				}
				var nodeID string
				if err := tx.QueryRow(ctx, `SELECT node_id::text FROM attachments WHERE tenant_id=$1 AND id=$2`, tenantID, attachmentID).Scan(&nodeID); err != nil {
					return err
				}
				_, err = events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actorID}, events.Change{NodeID: &nodeID, Type: "attachment.updated", Before: json.RawMessage(before), After: json.RawMessage(after)})
				updated = err == nil
				return err
			})
			_ = blob.Close()
			if err != nil {
				return report, err
			}
			if conflict {
				report.Conflicts = append(report.Conflicts, ImportConflict{ClassicID: classicID, Key: "attachment:" + strconv.FormatInt(classicID, 10), Reason: "Aeon attachment changed since last import"})
			}
			if updated {
				report.Updated++
			}
		}
	}
	created, err := ImportAttachments(ctx, pool, store, source, snap, tenantID, actorID)
	report.Created = created
	return report, err
}

func divergedAttachment(current, imported []byte) bool {
	if len(imported) == 0 {
		return true
	}
	var have, baseline map[string]any
	if json.Unmarshal(current, &have) != nil || json.Unmarshal(imported, &baseline) != nil {
		return true
	}
	for _, field := range []string{"sha256", "name", "content_type", "size", "width", "height", "caption", "node_id", "deleted_at"} {
		if !reflect.DeepEqual(have[field], baseline[field]) {
			return true
		}
	}
	return false
}
