// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Owner identifies the durable reference written in the same transaction as
// Publish or Put. Add each new consumer here, including its history/undo roots.
type Owner string

const (
	OwnerAttachment   Owner = "attachment"
	OwnerAvatar       Owner = "avatar"
	OwnerReceipt      Owner = "quote-receipt"
	OwnerQuoteProfile Owner = "quote-profile-asset"
)

type ownerSpec struct {
	owner Owner
	query string
	// Production functions allowed to publish this owner's blobs. The source
	// guard requires new writers to be inventoried here, even for existing owners.
	writers []string
}

// Immutable receipts and profile assets retain bytes for frozen quote versions.
// Soft-deleted attachments and both sides of history retain bytes for undo.
// SVG assets have no raster derivatives: image/* alone is not sufficient.
func blobOwners() []ownerSpec {
	return []ownerSpec{
		{OwnerAttachment, `SELECT sha256, content_type LIKE 'image/%' AS derivatives FROM attachments WHERE tenant_id=$1
		 UNION ALL SELECT s->>'sha256', coalesce(s->>'content_type','') LIKE 'image/%'
		 FROM events e CROSS JOIN LATERAL (VALUES(e.before),(e.after)) snapshot(s)
		 WHERE e.tenant_id=$1 AND e.type IN ('attachment.added','attachment.updated','attachment.removed')`,
			[]string{"internal/attachments/module.go:upload", "internal/importer/attachments.go:ImportAttachments", "internal/importer/attachment_delta.go:ImportAttachmentDelta"}},
		{OwnerAvatar, `WITH snapshots AS (
		 SELECT jsonb_build_object('avatar_original_hash',avatar_original_hash,'avatar_hashes',avatar_hashes) AS s FROM personal_profiles WHERE tenant_id=$1
		 UNION ALL SELECT s FROM events e CROSS JOIN LATERAL (VALUES(e.before),(e.after)) snapshot(s) WHERE e.tenant_id=$1 AND e.type='profile.updated'
		 ) SELECT s->>'avatar_original_hash' AS sha256, true AS derivatives FROM snapshots
		 UNION ALL SELECT h.value,true FROM snapshots CROSS JOIN LATERAL jsonb_each_text(CASE WHEN jsonb_typeof(s->'avatar_hashes')='object' THEN s->'avatar_hashes' ELSE '{}'::jsonb END) h`,
			[]string{"internal/profile/avatar.go:uploadAvatar", "internal/profile/import.go:Run"}},
		{OwnerReceipt, `SELECT file_sha256 AS sha256, false AS derivatives FROM quote_confirmation_receipts WHERE tenant_id=$1
		 UNION ALL SELECT receipt_sha256,false FROM quote_confirmation_jobs WHERE tenant_id=$1`,
			[]string{"internal/business/quotes/confirmation/module.go:ProcessNext"}},
		{OwnerQuoteProfile, `SELECT sha256,content_type='image/png' AS derivatives FROM quote_document_profile_assets WHERE tenant_id=$1`,
			[]string{"internal/business/quotes/profiles.go:putProfileAsset"}},
	}
}

func knownOwner(owner Owner) bool {
	for _, spec := range blobOwners() {
		if spec.owner == owner {
			return true
		}
	}
	return false
}

// inventoryQuery is shared by discovery, the final locked GC check and verify.
// hash="" selects the whole tenant; a digest selects just that lifetime.
func inventoryQuery() string {
	var queries []string
	for _, spec := range blobOwners() {
		queries = append(queries, "("+spec.query+")")
	}
	return `SELECT sha256,bool_or(derivatives) FROM (` + strings.Join(queries, " UNION ALL ") + `) owners
	 WHERE sha256 ~ '^[0-9a-f]{64}$' AND ($2::text='' OR sha256=$2) GROUP BY sha256`
}

func referencesTx(ctx context.Context, tx pgx.Tx, tenantID, hash string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, inventoryQuery(), tenantID, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]bool{}
	for rows.Next() {
		var digest string
		var derivatives bool
		if err := rows.Scan(&digest, &derivatives); err != nil {
			return nil, err
		}
		refs[digest] = derivatives
	}
	return refs, rows.Err()
}

// lockBlob must precede any hash-layout write or GC check/unlink. The owning
// transaction keeps it until its reference commits (or rolls back). The next
// statement sees commits made while waiting, so READ COMMITTED is required.
func lockBlob(ctx context.Context, tx pgx.Tx, tenantID, hash string) error {
	return LockBlobs(ctx, tx, tenantID, hash)
}

// LockBlobs reserves the complete set of blobs a transaction will publish,
// before its first file write or event append. Composite writers must call it
// once for all their inputs, including inputs in later nested operations.
// Existing reservations may be reused; new locks must follow tenant/hash order.
func LockBlobs(ctx context.Context, tx pgx.Tx, tenantID string, hashes ...string) error {
	if tx == nil || !validTenant(tenantID) {
		return errors.New("blob lifetime requires a tenant transaction and digest")
	}
	keys := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		if !validHash(hash) {
			return errors.New("blob lifetime requires a tenant transaction and digest")
		}
		keys = append(keys, strings.ToLower(tenantID)+":"+hash)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	if len(keys) == 0 {
		return nil
	}
	var currentTenant, isolation, reservations string
	var eventsLocked bool
	// Transaction-local settings survive RELEASE SAVEPOINT and roll back with
	// ROLLBACK TO SAVEPOINT, just like the advisory locks. No process-global
	// transaction cache can safely provide those semantics.
	// Appending an event writes event_counters (aeon_allocate_event_id), holding
	// RowExclusiveLock until commit. Inspect the actual lock so direct SQL event
	// inserts and nested calls are covered too, without instrumenting Append.
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id',true),current_setting('transaction_isolation'),
	 coalesce(current_setting('aeon.blob_reservations',true),''),
	 EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='relation'
	   AND relation='event_counters'::regclass AND mode='RowExclusiveLock' AND granted)`).Scan(&currentTenant, &isolation, &reservations, &eventsLocked); err != nil {
		return err
	}
	if !strings.EqualFold(currentTenant, tenantID) || isolation != "read committed" {
		return errors.New("blob lifetime requires the same tenant at read committed isolation")
	}
	var held []string
	if reservations != "" {
		held = strings.Split(reservations, ",")
	}
	var pending []string
	for _, key := range keys {
		if slices.Contains(held, key) {
			continue // Already held: do not acquire another lock after an event.
		}
		if eventsLocked {
			return errors.New("blob lock acquisition after event append")
		}
		if len(held) > 0 && key < held[len(held)-1] {
			return errors.New("blob lock acquisition out of tenant/hash order")
		}
		pending = append(pending, key)
	}
	for _, key := range pending {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,557))`, key); err != nil {
			return err
		}
	}
	if len(pending) == 0 {
		return nil
	}
	held = append(held, pending...)
	_, err := tx.Exec(ctx, `SELECT set_config('aeon.blob_reservations',$1,true)`, strings.Join(held, ","))
	return err
}
