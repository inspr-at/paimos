// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

type capacityFactInput struct {
	ID       string     `json:"id"`
	Revision int64      `json:"revision"`
	Resource *string    `json:"resource"`
	Window   string     `json:"window"`
	Wait     *string    `json:"wait"`
	Bucket   string     `json:"bucket"`
	Start    *time.Time `json:"start"`
	End      *time.Time `json:"end"`
}
type capacityFactLedger struct {
	id, account string
	reserved    int64
}

func factLedgerKey(id, bucket string, end time.Time) string {
	return id + "/" + bucket + "/" + end.UTC().Format(time.RFC3339Nano)
}
func recoveryKey(id, resource, window, wait string) string {
	return id + "/" + resource + "/" + window + "/" + wait
}

func (s *capacityProjection) loadProjectionFacts(ctx context.Context, tx pgx.Tx, ids []string, now time.Time) error {
	load := func(q string, args []any, read func(pgx.Rows) error) error {
		return accountCapacityRows(ctx, tx, ids, q, args, read)
	}
	if err := load(`SELECT
 (SELECT r.id::text FROM account_readiness_memberships m JOIN account_readiness_resources r ON r.tenant_id=m.tenant_id AND r.id=m.resource_id
 WHERE m.account_id=a.id AND m.binding_revision=a.link_revision AND r.identity_kind='account'
 AND r.kind=CASE WHEN a.harness='pi' THEN 'key_cap' ELSE 'subscription_quota' END
 AND r.identity_key=encode(sha256(convert_to('account:'||a.id::text||':'||CASE WHEN a.harness='pi' THEN 'key_cap' ELSE 'subscription_quota' END,'UTF8')),'hex')),
 (SELECT r.id::text FROM account_readiness_resources r JOIN account_readiness_memberships m ON m.tenant_id=r.tenant_id AND m.resource_id=r.id
 WHERE a.harness<>'pi' AND a.quota_pool_fingerprint<>'' AND m.account_id=a.id AND m.binding_revision=a.link_revision
 AND r.identity_kind='person_confirmed' AND r.identity_key=encode(sha256(convert_to('quota:'||a.harness||':'||a.quota_pool_fingerprint,'UTF8')),'hex'))
 FROM agent_accounts a WHERE a.id=$1`, nil, func(row pgx.Rows) error {
		var id string
		var local, resource *string
		if err := row.Scan(&id, &local, &resource); err != nil {
			return err
		}
		if local != nil {
			s.local[id] = *local
			s.resources[id] = *local
		}
		if resource != nil {
			s.resources[id] = *resource
		}
		return nil
	}); err != nil {
		return err
	}
	inputs := make([]capacityFactInput, 0, len(ids))
	for _, id := range ids {
		var resource *string
		if r := s.resources[id]; r != "" {
			resource = &r
		}
		inputs = append(inputs, capacityFactInput{ID: id, Revision: s.accounts[id].LinkRevision, Resource: resource})
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	from := `jsonb_to_recordset($1::jsonb) batch(id uuid,revision bigint,resource uuid)`
	q := strings.NewReplacer("$1", "batch.id", "$2", "batch.revision").Replace(capacityFactsSQL)
	if err = capacityRows(ctx, tx, from, q, []any{raw}, func(row pgx.Rows) error {
		var id string
		var v ReadinessFact
		if err := row.Scan(&id, &v.ResourceID, &v.WindowKey, &v.Source, &v.ObservedAt, &v.ResetsAt, &v.ReadingAt, &v.UsedPercent, &v.CreditState, &v.Remaining, &v.StopKind, &v.DenialReason, &v.ReadingError, &v.FailureCount, &v.CheckNextAttemptAt, &v.BackoffStep, &v.NextAttemptAt, &v.WaitID, &v.EarlyRecoveryUsed, &v.recoveryRunID); err != nil {
			return err
		}
		if v.ReadingAt != nil {
			age := max(int64(0), int64(now.Sub(*v.ReadingAt)/time.Second))
			v.ReadingAgeSeconds = &age
		}
		s.facts[id] = append(s.facts[id], v)
		return nil
	}); err != nil {
		return err
	}
	if err = load(capacityLegacyReadingsSQL, nil, func(row pgx.Rows) error {
		var id string
		var v capacity.Reading
		if err := row.Scan(&id, &v.WindowKind, &v.Bucket, &v.WindowMinutes, &v.UsedPercent, &v.ResetsAt, &v.ReadAt, &v.Source, &v.OrdinaryUsageAllowed); err != nil {
			return err
		}
		s.legacy[id] = append(s.legacy[id], legacyCapacityFact(s.local[id], v, now))
		return nil
	}); err != nil {
		return err
	}
	if err = load(capacityNamedStopSQL, []any{now}, func(row pgx.Rows) error {
		var id string
		var reset *time.Time
		if err := row.Scan(&id, &reset); err != nil {
			return err
		}
		s.named[id] = reset
		return nil
	}); err != nil {
		return err
	}
	// Keep the original bucket-specific clearance test and persisted watermark.
	// An unrelated room reading must not clear a named vendor stop.
	cleared := strings.NewReplacer("$1", "batch.id", "$2", "observation.run_id::uuid", "$3", "observation.kind", "$4", "$2").Replace(capacityVendorClearedSQL)
	q = `SELECT observation.at,observation.reset,(` + cleared + `),EXISTS(SELECT 1 FROM account_readiness_facts WHERE resource_id=batch.resource AND window_key='vendor' AND observed_at>=observation.at)
 FROM (` + strings.ReplaceAll(capacityVendorSQL, "$1", "batch.id") + `) observation(at,reset,run_id,kind)`
	if err = capacityRows(ctx, tx, from, q, []any{raw, now}, func(row pgx.Rows) error {
		var id string
		var at time.Time
		var reset *time.Time
		var cleared, already bool
		if err := row.Scan(&id, &at, &reset, &cleared, &already); err != nil {
			return err
		}
		if already || reset != nil && cleared {
			return nil
		}
		f := &ReadinessFact{ReadinessFactWrite: ReadinessFactWrite{ResourceID: s.resources[id], WindowKey: "vendor", Source: "harness", ObservedAt: at, ResetsAt: reset, CreditState: "unknown", StopKind: "unnamed", DenialReason: "vendor_denied"}}
		if reset != nil {
			f.StopKind = "named_reset"
		} else {
			next := at.Add(time.Hour)
			f.NextAttemptAt = &next
		}
		s.vendor[id] = f
		return nil
	}); err != nil {
		return err
	}
	// Resolve fact ledgers and recovery checks in two set reads. Their consumers
	// retain the existing hard-stop/permit decisions and ordering.
	facts := []capacityFactInput{}
	for _, id := range ids {
		for _, f := range s.facts[id] {
			resource := f.ResourceID
			facts = append(facts, capacityFactInput{ID: id, Resource: &resource, Window: f.WindowKey, Wait: f.WaitID, Bucket: f.ResourceID + ":" + f.WindowKey, Start: f.ReadingAt, End: f.ResetsAt})
		}
	}
	raw, err = json.Marshal(facts)
	if err != nil {
		return err
	}
	from = `jsonb_to_recordset($1::jsonb) batch(id uuid,resource uuid,"window" text,wait uuid,bucket text,start timestamptz,"end" timestamptz)`
	s.factLedgers = map[string]capacityFactLedger{}
	q = `SELECT batch.bucket,batch."end",ledger.id::text,ledger.account_id::text,
 (SELECT COALESCE(sum(reserved),0)::bigint FROM account_allowance_windows WHERE capacity_kind='fact' AND capacity_bucket=batch.bucket AND starts_at<batch."end" AND ends_at>batch.start)
 FROM (SELECT 1) singleton LEFT JOIN LATERAL (SELECT id,account_id FROM account_allowance_windows WHERE capacity_kind='fact' AND capacity_bucket=batch.bucket AND ends_at=batch."end" ORDER BY id LIMIT 1) ledger ON true
 WHERE batch.start IS NOT NULL AND batch."end" IS NOT NULL`
	if err = capacityRows(ctx, tx, from, q, []any{raw}, func(row pgx.Rows) error {
		var id, bucket string
		var end time.Time
		var ledger, account *string
		var reserved int64
		if err := row.Scan(&id, &bucket, &end, &ledger, &account, &reserved); err != nil {
			return err
		}
		v := capacityFactLedger{account: id, reserved: reserved}
		if ledger != nil {
			v.id = *ledger
			v.account = *account
		}
		s.factLedgers[factLedgerKey(id, bucket, end)] = v
		return nil
	}); err != nil {
		return err
	}
	q = `SELECT batch.resource::text,batch."window",batch.wait::text,checks.id,busy.value
 FROM (SELECT 1) singleton
 LEFT JOIN LATERAL (` + strings.NewReplacer("$1", "batch.resource", "$2", `batch."window"`, "$3", "batch.wait", "$4", "$2").Replace(capacityEarlyCheckSQL) + `) checks(id) ON true
 CROSS JOIN LATERAL (` + strings.NewReplacer("$1", "batch.id", "$2", "batch.resource", "$3", "''", "$4", "false").Replace(capacityRecoveryBusySQL) + `) busy(value) WHERE batch.wait IS NOT NULL`
	return capacityRows(ctx, tx, from, q, []any{raw, now.Add(-CheckTTL)}, func(row pgx.Rows) error {
		var id, resource, window, wait string
		var check *string
		var busy bool
		if err := row.Scan(&id, &resource, &window, &wait, &check, &busy); err != nil {
			return err
		}
		key := recoveryKey(id, resource, window, wait)
		s.early[key] = check
		s.busy[key] = busy
		return nil
	})
}

func legacyCapacityFact(resource string, v capacity.Reading, now time.Time) ReadinessFact {
	at, reset, used := v.ReadAt, v.ResetsAt, v.UsedPercent
	age := max(int64(0), int64(now.Sub(at)/time.Second))
	f := ReadinessFact{ReadinessFactWrite: ReadinessFactWrite{ResourceID: resource, WindowKey: v.WindowKind + ":" + v.Bucket, Source: v.Source, ObservedAt: at, ReadingAt: &at, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown", StopKind: "none"}, ReadingAgeSeconds: &age}
	if used >= 100 || v.OrdinaryUsageAllowed != nil && !*v.OrdinaryUsageAllowed {
		f.StopKind = "named_reset"
		f.DenialReason = "vendor_denied"
		if used >= 100 {
			f.DenialReason = "quota_exhausted"
		}
	}
	f.legacyReading = &capacity.Reading{WindowKind: v.WindowKind, Bucket: v.Bucket, WindowMinutes: v.WindowMinutes, UsedPercent: used, ResetsAt: reset, ReadAt: at, Source: v.Source}
	return f
}
