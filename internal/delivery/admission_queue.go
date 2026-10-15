// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func stageAdmissionHeadTx(ctx context.Context, tx pgx.Tx, tid string, o Observation) error {
	if o.PR == nil {
		return nil
	}
	var rev int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM delivery_admission_bindings WHERE repository=$1 AND pull_request=$2`, o.Repository, *o.PR).Scan(&rev); err != nil {
		return err
	}
	project := ""
	if o.Project != nil {
		project = *o.Project
	}
	raw, _ := json.Marshal([]admissionMember{{PR: *o.PR, Head: o.Head, Revision: rev, Project: project}})
	_, err := tx.Exec(ctx, `INSERT INTO delivery_admission_outbox(tenant_id,repository,sha,context,kind,members,resolved) VALUES($1,$2,$3,$4,'head',$5,true)
 ON CONFLICT(tenant_id,repository,sha,context) DO UPDATE SET members=EXCLUDED.members,revision=delivery_admission_outbox.revision+1
 WHERE delivery_admission_outbox.kind='head' AND delivery_admission_outbox.members IS DISTINCT FROM EXCLUDED.members`, tid, o.Repository, o.Head, AdmissionContext, raw)
	if err != nil {
		return err
	}
	// A queue entry can reveal a synthetic SHA before its webhook arrives.
	// Its partial member list permits invalidation but can never grant success.
	if o.Queued && reviewgate.ValidSHA(o.QueueHead) {
		if _, err = tx.Exec(ctx, `INSERT INTO delivery_admission_outbox(tenant_id,repository,sha,context,kind,members) VALUES($1,$2,$3,$4,'group',$5)
 ON CONFLICT(tenant_id,repository,sha,context) DO UPDATE SET
 members=delivery_admission_outbox.members || EXCLUDED.members,revision=delivery_admission_outbox.revision+1
 WHERE NOT delivery_admission_outbox.resolved AND NOT delivery_admission_outbox.members @> EXCLUDED.members`, tid, o.Repository, o.QueueHead, AdmissionContext, raw); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_admission_queue(tenant_id,repository,pull_request,head_sha) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tid, o.Repository, *o.PR, o.Head)
	return err
}

type admissionQueueEntry struct {
	PR   int64
	Head string
}

func (m *Module) reconcileAdmissionQueue(ctx context.Context) error {
	if err := m.seedAdmission(db.AllProjects(ctx, "admission queue seed")); err != nil {
		return err
	}
	cursorPR := int64(0)
	cursorHead := ""
	var failures []error
	for {
		entries := []admissionQueueEntry{}
		err := db.InTenant(db.AllProjects(ctx, "admission queue inventory"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT pull_request,head_sha FROM delivery_admission_queue WHERE repository=$1 AND (enforcement NOT IN ('superseded','removed','merged_before_hold') OR (quarantined AND NOT notice_sent)) AND (pull_request,head_sha)>($2,$3) ORDER BY pull_request,head_sha LIMIT 100`, m.config.Repository, cursorPR, cursorHead)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var e admissionQueueEntry
				if err = rows.Scan(&e.PR, &e.Head); err != nil {
					return err
				}
				entries = append(entries, e)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err = m.admitQueueEntry(ctx, e); err != nil {
				failures = append(failures, err)
			}
		}
		if len(entries) < 100 {
			break
		}
		last := entries[len(entries)-1]
		cursorPR, cursorHead = last.PR, last.Head
	}
	return errors.Join(failures...)
}

// The final mutation transaction holds tenant -> tree across the queue POST.
// This serializes access/policy changes with enqueue; after a hold commits its
// removal remains pending until a fresh GitHub read confirms it. Attempt intent
// is committed BEFORE the POST, so a crash cannot silently reset the retry cap.
func (m *Module) admitQueueEntry(ctx context.Context, e admissionQueueEntry) error {
	conn, release, err := db.Acquire(ctx, m.pool)
	if err != nil {
		return err
	}
	defer release()
	key := fmt.Sprintf("aeon-admission-queue:%s:%d", m.config.Repository, e.PR)
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil || !locked {
		return err
	}
	defer unlock(conn, key)
	ctx = db.AllProjects(db.WithConnection(ctx, m.pool, conn), "configured admission queue authority")
	live, err := m.github.Pull(ctx, e.PR)
	if err != nil {
		return err
	}
	// Historical-head rows must never dequeue or enqueue a different live head.
	if live.Head != e.Head {
		return db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE delivery_admission_queue SET enforcement='superseded' WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head)
			return err
		})
	}
	action := ""
	err = db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		allowed, item, err := m.queueAdmissionTx(ctx, tx, e, live)
		if err != nil {
			return err
		}
		var attempts, retries int
		var consumed string
		var removed, quarantined bool
		if err = tx.QueryRow(ctx, `SELECT attempts,infra_retries,consumed_group,policy_removed,quarantined FROM delivery_admission_queue WHERE repository=$1 AND pull_request=$2 AND head_sha=$3 FOR UPDATE`, m.config.Repository, e.PR, e.Head).Scan(&attempts, &retries, &consumed, &removed, &quarantined); err != nil {
			return err
		}
		var infraCount int
		var lastGroup string
		var codeFailed bool
		err = tx.QueryRow(ctx, `SELECT count(DISTINCT group_sha) FILTER(WHERE kind='infra' AND group_sha IS NOT NULL),
 coalesce((array_agg(group_sha ORDER BY at DESC,check_run_id DESC) FILTER(WHERE kind='infra' AND group_sha IS NOT NULL))[1],''),
 coalesce(bool_or(kind='required_failure'),false) FROM delivery_queue_failures WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head).Scan(&infraCount, &lastGroup, &codeFailed)
		if err != nil {
			return err
		}
		quarantined = quarantined || codeFailed || infraCount > 1
		if !allowed || quarantined {
			action = "dequeue"
			_, err = tx.Exec(ctx, `UPDATE delivery_admission_queue SET policy_removed=NOT $4,quarantined=$4,enforcement='pending' WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head, quarantined)
			return err
		}
		if live.Queued {
			_, err = tx.Exec(ctx, `UPDATE delivery_admission_queue SET enforcement='queued' WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head)
			return err
		}
		retry := attempts > 0 && !removed
		if retry && (lastGroup == "" || lastGroup == consumed || retries >= 1) {
			// Unknown/manual ejection is never a verified infrastructure retry.
			_, err = tx.Exec(ctx, `UPDATE delivery_admission_queue SET quarantined=true,enforcement='pending' WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head)
			action = "dequeue"
			return err
		}
		if item == nil {
			return errRead
		}
		action = "enqueue"
		_, err = tx.Exec(ctx, `UPDATE delivery_admission_queue SET attempts=attempts+1,infra_retries=infra_retries+$4,consumed_group=$5,policy_removed=false,enforcement='pending' WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head, boolInt(retry), lastGroup)
		return err
	})
	if err != nil {
		return err
	}
	if action != "" {
		err = db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTree(ctx, tx, m.config.TenantID); err != nil {
				return err
			}
			allowed, _, err := m.queueAdmissionTx(ctx, tx, e, live)
			if err != nil {
				return err
			}
			if action == "enqueue" {
				var quarantined bool
				if err = tx.QueryRow(ctx, `SELECT quarantined FROM delivery_admission_queue WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head).Scan(&quarantined); err != nil {
					return err
				}
				if !allowed || quarantined {
					return nil
				}
				return m.admissionGitHub.Enqueue(ctx, e.PR, e.Head)
			}
			// A lifted hold may resume this head in the next pass. Removal of the old
			// group is still completed; policy removal never quarantines approved code.
			if !live.Queued {
				return nil
			}
			return m.admissionGitHub.Dequeue(ctx, e.PR)
		})
		if err != nil {
			return err
		}
	}
	confirmed, err := m.github.Pull(ctx, e.PR)
	if err != nil {
		return err
	}
	err = db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		result := "pending"
		if confirmed.Head != e.Head {
			result = "superseded"
		} else if action == "dequeue" {
			if confirmed.Merged {
				result = "merged_before_hold"
			} else if !confirmed.Queued {
				result = "removed"
			}
		} else if confirmed.Queued {
			result = "queued"
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_admission_queue SET enforcement=$4 WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head, result)
		return err
	})
	if err != nil {
		return err
	}
	return m.admissionNotice(ctx, e)
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (m *Module) queueAdmissionTx(ctx context.Context, tx pgx.Tx, e admissionQueueEntry, live Pull) (bool, *Item, error) {
	item, err := load(ctx, tx, stableID(m.config.TenantID, m.config.Repository, subject(&e.PR, nil)))
	if err != nil || item == nil {
		return false, item, err
	}
	if item.Head != e.Head || !live.Open || live.Merged || live.Target != item.Observation.Target || live.AdmissionDiff == nil || live.AdmissionDiff.MergeBase != item.Observation.Base {
		return false, item, nil
	}
	// A network read may be older than a webhook that committed while it was
	// in flight. Both the current stored decision and live read must admit;
	// never overwrite a newly red stored CI fact with an older green response.
	stored, err := AdmissionTx(ctx, tx, *item)
	var problem *apiError
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) {
		return false, item, nil
	}
	if err != nil || !stored.Decision.Admissible {
		return false, item, err
	}
	current := *item
	current.Observation.Checks = live.Checks
	current.Observation.AdmissionDiff = live.AdmissionDiff
	snap, err := AdmissionTx(ctx, tx, current)
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) {
		return false, item, nil
	}
	return err == nil && snap.Decision.Admissible && !item.Observation.QueueFailure, item, err
}

// Quarantine produces one durable held Desk request, without a network send.
// Missing authorized recipient/project keeps the notice pending for the sweep.
func (m *Module) admissionNotice(ctx context.Context, e admissionQueueEntry) error {
	var actor tenant.Principal
	err := db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		var err error
		actor, err = systemactor.Ensure(ctx, tx, m.config.TenantID)
		return err
	})
	if err != nil {
		return err
	}
	return db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		var needed bool
		if err := tx.QueryRow(ctx, `SELECT (quarantined OR enforcement='merged_before_hold') AND NOT notice_sent FROM delivery_admission_queue WHERE repository=$1 AND pull_request=$2 AND head_sha=$3 FOR UPDATE`, m.config.Repository, e.PR, e.Head).Scan(&needed); err != nil || !needed {
			return err
		}
		item, err := load(ctx, tx, stableID(m.config.TenantID, m.config.Repository, subject(&e.PR, nil)))
		if err != nil || item == nil || item.Project == nil {
			return err
		}
		var recipient string
		err = tx.QueryRow(ctx, `SELECT p.id::text FROM principals p JOIN role_bindings b ON b.principal_id=p.id JOIN roles r ON r.id=b.role_id WHERE p.kind='person' AND p.status='active' AND r.key IN ('owner','admin') AND (b.scope_type='workspace' OR b.scope_type='project' AND b.scope_id=$1::uuid) ORDER BY p.id LIMIT 1`, *item.Project).Scan(&recipient)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		person := tenant.Principal{ID: recipient, TenantID: m.config.TenantID, Kind: tenant.Person}
		for _, permission := range []string{"delivery.read", "inbox.manage"} {
			if err = authz.RequireTx(ctx, tx, person, permission, scope(item.Project)); errors.Is(err, authz.ErrForbidden) {
				return nil
			} else if err != nil {
				return err
			}
		}
		var result string
		if err = tx.QueryRow(ctx, `SELECT enforcement FROM delivery_admission_queue WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head).Scan(&result); err != nil {
			return err
		}
		body := fmt.Sprintf("Admission: PR #%d, head %s is quarantined after a required failure, repeated infrastructure failure or unconfirmed queue outcome. A new reviewed head is required. Queue enforcement: %s.", e.PR, e.Head, result)
		if result == "merged_before_hold" {
			body = fmt.Sprintf("Admission: PR #%d, head %s merged before the hold. The hold did not prevent this merge.", e.PR, e.Head)
		}
		var id string
		if err = tx.QueryRow(ctx, `INSERT INTO inbox_compat_messages(tenant_id,project_id,sender_principal_id,recipient_principal_id,sender_address,recipient_address,body,key_digest,request_digest,thread_id,hop,is_action_request,expects_reply,delivery_level) VALUES($1,$2,$3,$4,'paimos:System','paimos:person',$5,$6,$6,gen_random_uuid()::text,1,true,false,'simple') RETURNING id::text`, m.config.TenantID, item.Project, actor.ID, recipient, body, fmt.Sprintf("admission:%s:%d:%s", m.config.Repository, e.PR, e.Head)).Scan(&id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO inbox_message_deliveries(tenant_id,message_id,state,reason) VALUES($1,$2,'held','action_request')`, m.config.TenantID, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE delivery_admission_queue SET notice_sent=true WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, m.config.Repository, e.PR, e.Head); err != nil {
			return err
		}
		ev, err := events.Append(ctx, tx, actor, events.Change{NodeID: item.Project, Type: "delivery.admission.notice", After: map[string]string{"message_id": id, "body": body}})
		if err != nil {
			return err
		}
		// This row was inserted and fenced above, before the event counter.
		_, err = tx.Exec(ctx, `UPDATE inbox_compat_messages SET sent_event_id=$2 WHERE id=$1`, id, ev.ID)
		return err
	})
}

// Seed unchanged P2 observations as well as new webhooks. Without this bounded
// backfill enabling P3 would wait for a changed observation to create intent.
func (m *Module) seedAdmission(ctx context.Context) error {
	cursor := ""
	for {
		observations := []Observation{}
		last := ""
		err := db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT id::text,observation FROM delivery_items WHERE repository=$1 AND pull_request IS NOT NULL AND id::text>$2 AND observation->>'open'='true' ORDER BY id::text LIMIT 100`, m.config.Repository, cursor)
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw []byte
				var o Observation
				if err = rows.Scan(&last, &raw); err != nil {
					rows.Close()
					return err
				}
				if err = json.Unmarshal(raw, &o); err != nil {
					rows.Close()
					return err
				}
				observations = append(observations, o)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, o := range observations {
				if err = stageAdmissionHeadTx(ctx, tx, m.config.TenantID, o); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(observations) < 100 {
			return nil
		}
		cursor = last
	}
}
