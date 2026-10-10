// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func queueSnapshotOverflow(part string) error {
	return workorders.Fail(http.StatusConflict, "queue "+part+" snapshot exceeds bound")
}

const (
	maxWaitRuns     = 4096
	maxWaitAccounts = 4096
	maxWaitWindows  = 16384
)

// waitSnapshot belongs only to one advisory batch and its tenant transaction.
// It is never passed into reservation or claim code, or retained across calls.
type waitSnapshot struct {
	accounts    []Account
	used        map[string]int
	quotaUsed   map[string]int
	shared      map[string][]Window
	sharedCount int
	now         time.Time
	loaded      bool
}

// A confirmed pool has the same ledger through every door. Keep that bounded
// ledger only for this advisory batch; admission derives fresh window copies.
func (s *waitSnapshot) windows(ctx context.Context, tx pgx.Tx, a Account, own []Window, now time.Time) ([]Window, error) {
	if a.QuotaPoolFingerprint == "" {
		return own, nil
	}
	key := a.Harness + ":" + a.QuotaPoolFingerprint
	if windows, ok := s.shared[key]; ok {
		return windows, nil
	}
	remaining := maxWaitWindows - s.sharedCount
	if remaining <= 0 {
		return nil, queueSnapshotOverflow("quota window")
	}
	windows, err := sharedQuotaWindowsLimit(ctx, tx, a, own, now, maxWaitAccounts, remaining)
	if err != nil {
		return nil, err
	}
	if s.shared == nil {
		s.shared = map[string][]Window{}
	}
	s.shared[key] = windows
	s.sharedCount += len(windows)
	return windows, nil
}

func (s *waitSnapshot) load(ctx context.Context, tx pgx.Tx) error {
	var err error
	s.accounts, err = queryAccountsLimit(ctx, tx, false, maxWaitAccounts)
	if err != nil {
		return err
	}
	// Count every active door, including retired siblings still holding a
	// confirmed pool's slots. The two existing occupancy views share one scan.
	rows, err := tx.Query(ctx, `SELECT r.account_id::text,a.harness,a.quota_pool_fingerprint,count(*)
 FROM agent_runs r JOIN agent_accounts a ON a.tenant_id=r.tenant_id AND a.id=r.account_id
 WHERE r.account_id IS NOT NULL AND r.status IN ('queued','starting','running','waiting')
 GROUP BY r.account_id,a.harness,a.quota_pool_fingerprint LIMIT $1`, maxWaitAccounts+1)
	if err != nil {
		return err
	}
	s.used, s.quotaUsed = map[string]int{}, map[string]int{}
	for rows.Next() {
		if len(s.used) == maxWaitAccounts {
			rows.Close()
			return queueSnapshotOverflow("occupancy")
		}
		var id, harness, pool string
		var count int
		if err = rows.Scan(&id, &harness, &pool, &count); err != nil {
			rows.Close()
			return err
		}
		s.used[id] = count
		if pool != "" {
			s.quotaUsed[harness+":"+pool] += count
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	s.now, err = dbNow(ctx, tx)
	s.loaded = err == nil
	return err
}

// WaitForRuns projects a bounded set of runs using common account, occupancy
// and quota facts once. The returned advice grants no launch authority: callers
// must still reserve and claim through the existing fenced mutation paths.
// Run-specific grants, pins, residency, policies and readiness are rechecked.
func WaitForRuns(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*CapacityWait, error) {
	if len(ids) > maxWaitRuns {
		return nil, queueSnapshotOverflow("run")
	}
	out := make(map[string]*CapacityWait, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	for _, id := range ids {
		if !uuidRE.MatchString(id) {
			return nil, workorders.Fail(http.StatusBadRequest, "invalid run id")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := tx.Query(ctx, `SELECT id::text,agent_principal_id::text,model_profile_id::text,account_id::text,
 COALESCE(requested_account_id,retry_account_id)::text,status,purpose,capacity_override
 FROM agent_runs WHERE id=ANY($1::uuid[]) LIMIT $2`, ids, maxWaitRuns)
	if err != nil {
		return nil, err
	}
	runs := make(map[string]runRow, len(ids))
	for rows.Next() {
		var run runRow
		if err = rows.Scan(&run.ID, &run.AgentID, &run.ProfileID, &run.AccountID, &run.RequestedAccountID, &run.Status, &run.Purpose, &run.CapacityOverride); err != nil {
			rows.Close()
			return nil, err
		}
		runs[run.ID] = run
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Fail the whole batch on a missing or wrong-tenant identity. Never make a
	// partial result look like a successful capacity projection.
	for _, id := range ids {
		if _, ok := runs[strings.ToLower(id)]; !ok {
			return nil, pgx.ErrNoRows
		}
	}
	snapshot := &waitSnapshot{}
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		run := runs[strings.ToLower(id)]
		if run.Status == "queued" && run.Purpose == "managed" {
			out[id], err = waitForRunSnapshot(ctx, tx, run, snapshot)
			if err != nil {
				return nil, err
			}
		} else {
			out[id] = nil
		}
	}
	return out, nil
}
