// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ImportAttachments downloads the attachment records already captured in a
// classic snapshot. It only issues GET /api/attachments/{id} to the
// same configured HTTPSource. The node snapshot must already be imported.
// A rerun skips rows identified by (tenant, source instance, classic ID).
func ImportAttachments(ctx context.Context, pool *pgxpool.Pool, store attachments.Store, source *HTTPSource, snap Snapshot, tenantID, actorID string) (int, error) {
	if source == nil || snap.SourceID == "" || source.InstanceID() != snap.SourceID {
		return 0, errors.New("source identity mismatch")
	}
	p := tenant.Principal{TenantID: tenantID, ID: actorID}
	created := 0
	for issueID, detail := range snap.Details {
		for _, record := range detail.Attachments {
			attachmentID, ok := intField(record, "id")
			if !ok || attachmentID < 1 {
				return created, fmt.Errorf("issue %d attachment missing id", issueID)
			}
			name := path.Base(strings.ReplaceAll(stringField(record, "filename"), "\\", "/"))
			name = strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return -1
				}
				return r
			}, name)
			for len(name) > 255 {
				name = name[:len(name)-1]
				for !utf8.ValidString(name) {
					name = name[:len(name)-1]
				}
			}
			if name == "" || name == "." || name == "/" {
				return created, fmt.Errorf("attachment %d missing filename", attachmentID)
			}
			var nodeID string
			var exists bool
			err := db.InTenant(db.AllProjects(ctx, "classic importer"), pool, tenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE tenant_id=$1 AND fields->'classic'->>'source_id'=$2 AND fields->'classic'->>'id'=$3 AND deleted_at IS NULL`, tenantID, snap.SourceID, strconv.FormatInt(issueID, 10)).Scan(&nodeID); err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachments WHERE tenant_id=$1 AND source_id=$2 AND source_attachment_id=$3)`, tenantID, snap.SourceID, attachmentID).Scan(&exists)
			})
			if err != nil {
				return created, fmt.Errorf("resolve issue %d: %w", issueID, err)
			}
			if exists {
				continue
			}
			body, err := source.downloadAttachment(ctx, attachmentID)
			if isNotFound(err) {
				// The classic record exists but its file is gone (deleted or
				// purged); skip it like deleted issues and keep importing.
				slog.Warn("classic attachment file missing; skipped", "attachment", attachmentID, "issue", issueID)
				continue
			}
			if err != nil {
				return created, err
			}
			blob, putErr := store.Stage(ctx, tenantID, body)
			closeErr := body.Close()
			if errors.Is(putErr, attachments.ErrUnsupportedType) {
				slog.Warn("classic attachment type not accepted; skipped", "attachment", attachmentID, "issue", issueID, "error", putErr)
				continue
			}
			if putErr != nil {
				return created, fmt.Errorf("store attachment %d: %w", attachmentID, putErr)
			}
			if closeErr != nil {
				_ = blob.Close()
				return created, closeErr
			}
			inserted := false
			err = db.InTenant(db.AllProjects(ctx, "classic importer"), pool, tenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, tenantID+":"+snap.SourceID+":"+strconv.FormatInt(attachmentID, 10)); err != nil {
					return err
				}
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachments WHERE tenant_id=$1 AND source_id=$2 AND source_attachment_id=$3)`, tenantID, snap.SourceID, attachmentID).Scan(&exists); err != nil {
					return err
				}
				if exists {
					return nil
				}
				if err := attachments.Publish(ctx, tx, attachments.OwnerAttachment, blob); err != nil {
					return err
				}
				var position int64
				if err := tx.QueryRow(ctx, `SELECT coalesce(ceil(max(position)),0)::bigint+1 FROM attachments WHERE tenant_id=$1 AND node_id=$2`, tenantID, nodeID).Scan(&position); err != nil {
					return err
				}
				var a attachments.Attachment
				err := tx.QueryRow(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,width,height,caption,position,created_by,source_id,source_attachment_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'',$9,$10,$11,$12) RETURNING id::text,node_id::text,sha256,name,content_type,size,width,height,caption,position::text,created_by::text,created_at,updated_at,deleted_at`, tenantID, nodeID, blob.SHA256, name, blob.ContentType, blob.Size, optionalDimension(blob.Width), optionalDimension(blob.Height), position, actorID, snap.SourceID, attachmentID).Scan(&a.ID, &a.NodeID, &a.SHA256, &a.Name, &a.ContentType, &a.Size, &a.Width, &a.Height, &a.Caption, &a.Position, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
				if err != nil {
					return err
				}
				_, err = events.Append(ctx, tx, p, events.Change{NodeID: &nodeID, Type: "attachment.added", After: a})
				if err == nil {
					inserted = true
				}
				return err
			})
			_ = blob.Close()
			if err != nil {
				return created, fmt.Errorf("attach classic %d: %w", attachmentID, err)
			}
			if inserted {
				created++
			}
		}
	}
	return created, nil
}
func optionalDimension(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func (s *HTTPSource) downloadAttachment(ctx context.Context, id int64) (io.ReadCloser, error) {
	// Classic Paimos serves the file at GET /api/attachments/{id} (backend/main.go).
	path := "/attachments/" + strconv.FormatInt(id, 10)
	u := *s.base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api" + path
	u.RawPath = ""
	u.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid attachment download request")
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("attachment %d download failed", id)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, &sourceHTTPError{method: http.MethodGet, path: path, status: resp.StatusCode}
	}
	return resp.Body, nil
}
