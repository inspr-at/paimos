// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/jackc/pgx/v5"
)

// The catalog cursor is durable in the first visible job checkpoint. It is
// merely a paging cursor: a completed sweep starts again, and no absence from
// this application's restored database authorizes retirement of a recovery pin.
func (s *Service) reconcileCatalog(ctx context.Context) error {
	var anchor Authority
	var project, cursor string
	for _, a := range s.cfg.Authorities {
		err := s.with(ctx, a.Executor, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT project_node_id::text,coalesce(reconciliation_cursor,'') FROM delivery_adoption_jobs WHERE instance_id=$1 ORDER BY project_node_id LIMIT 1`, s.cfg.Instance).Scan(&project, &cursor)
		})
		if err == nil {
			anchor = a
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if project == "" {
		return nil
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	page, err := s.provider.List(call, s.cfg.Instance, cursor, 100)
	cancel()
	if err != nil {
		return capacityError("cleanup_blocked")
	}
	if len(page.Items) > 100 || len(page.Next) > 512 || page.Next != "" && page.Next == cursor {
		return errors.New("provider catalog violated keyset bounds")
	}
	unknown := false
	for _, v := range page.Items {
		id := v.Identity
		if id.Instance != s.cfg.Instance || id.Migration != Migration || !uuidRE.MatchString(id.Tenant) || !uuidRE.MatchString(id.Project) || !uuidRE.MatchString(id.Attempt) || v.Key != operationKey(id, v.Kind) {
			unknown = true
			continue
		}
		if v.State == "reclaimed" {
			continue
		}
		a, ok := s.authority(id.Tenant)
		if !ok {
			unknown = true
			continue
		}
		switch v.Kind {
		case "backup", "restore", "pin", "cancel", "cleanup":
		default:
			unknown = true
			continue
		}
		err = s.with(ctx, a.Executor, func(tx pgx.Tx) error {
			if err := mutationFence(ctx, tx, a.Executor); err != nil {
				return err
			}
			if err := authz.RequireTx(ctx, tx, a.Executor, "releases.read", authz.Scope{ProjectID: id.Project}); err != nil {
				return err
			}
			j, err := s.loadJob(ctx, tx, id.Tenant, id.Project, true)
			if err != nil {
				return err
			}
			for _, op := range j.Journal.Operations {
				if op.Key == v.Key {
					return nil
				}
			}
			var adopted bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=$1)`, id.Project).Scan(&adopted); err != nil {
				return err
			}
			// Protected backup catalog entries need not repeat the pin handle.
			// The successful job's exact chain and immutable pin are retained.
			if adopted && j.Pin != "" && v.Kind == "backup" && v.BackupRef == j.BackupRef && v.BackupDigest == j.BackupDigest {
				return nil
			}
			// This result may be a successfully pinned recovery chain whose
			// database checkpoint was rolled back. Preserve it independently.
			if v.Kind == "pin" || v.Protected {
				if adopted && v.Pin == j.Pin {
					return nil
				}
				_, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET cleanup_state='blocked',reason_code='recovery_unknown',reason_message=$2,resource_attempt_id=coalesce(resource_attempt_id,$3::uuid),reserved_backup_bytes=greatest(reserved_backup_bytes,$4),next_reconcile_at=$5,revision=revision+1,updated_at=$6 WHERE project_node_id=$1`, id.Project, "An external recovery pin lacks authoritative local history; retain it and reconcile the restore record.", id.Attempt, s.cfg.Quota.BundleBytes, s.now().Add(time.Hour), s.now())
				unknown = true
				return err
			}
			if v.State == "reclaimed" || v.Kind == "cancel" || v.Kind == "cleanup" {
				return nil
			}
			if j.ResourceAttempt != "" && j.ResourceAttempt != id.Attempt {
				unknown = true
				return nil
			}
			// Owned scratch orphan: preserve its immutable key before any
			// reconciliation effect. A future generation cannot allocate yet.
			op := Operation{Key: v.Key, Attempt: id.Attempt, Kind: v.Kind, Status: v.State, Handle: v.Handle, Identity: id, Deadline: s.now(), Bytes: s.cfg.Quota.BundleBytes, Slots: 1}
			j.Journal.Operations = append(j.Journal.Operations, op)
			raw, err := json.Marshal(j.Journal)
			if err != nil {
				return err
			}
			if err = decodeJournal(raw, &Journal{}); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET operation_journal=$2,resource_attempt_id=$3,reserved_backup_bytes=greatest(reserved_backup_bytes,$4),reserved_restore_slots=1,cleanup_state='pending',next_reconcile_at=$5,revision=revision+1,updated_at=$5 WHERE project_node_id=$1`, id.Project, raw, id.Attempt, s.cfg.Quota.BundleBytes, s.now())
			return err
		})
		if err != nil {
			unknown = true
		}
	}
	err = s.with(ctx, anchor.Executor, func(tx pgx.Tx) error {
		if err := mutationFence(ctx, tx, anchor.Executor); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, anchor.Executor, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET reconciliation_cursor=nullif($2,''),revision=revision+1,updated_at=$3 WHERE project_node_id=$1`, project, page.Next, s.now())
		return err
	})
	if err != nil {
		return err
	}
	if unknown {
		return capacityError("cleanup_blocked")
	}
	return nil
}
