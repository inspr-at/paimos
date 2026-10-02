// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ReadinessResource is an opaque, tenant-scoped resource membership. It carries
// no vendor credential or local path. B reconciles memberships; C reports only
// against current memberships, and cannot assert a shared resource identity.
type ReadinessResource struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	BindingRevision int64  `json:"binding_revision"`
}

type ReadinessFactWrite struct {
	ResourceID   string     `json:"resource_id"`
	WindowKey    string     `json:"window_key"`
	Source       string     `json:"source"`
	ObservedAt   time.Time  `json:"observed_at"`
	ResetsAt     *time.Time `json:"resets_at"`
	ReadingAt    *time.Time `json:"reading_at"`
	UsedPercent  *float64   `json:"used_percent"`
	CreditState  string     `json:"credit_state"`
	Remaining    *float64   `json:"remaining"`
	StopKind     string     `json:"stop_kind"`
	DenialReason string     `json:"denial_reason"`
}

type ReadinessFact struct {
	legacyReading *capacity.Reading // Original legacy bucket/duration; never serialized.
	ReadinessFactWrite
	ReadingError       string     `json:"reading_error,omitempty"`
	ReadingAgeSeconds  *int64     `json:"reading_age_seconds"`
	FailureCount       int        `json:"failure_count"`
	CheckNextAttemptAt *time.Time `json:"check_next_attempt_at"`
	BackoffStep        int        `json:"backoff_step"`
	NextAttemptAt      *time.Time `json:"next_attempt_at"`
	WaitID             *string    `json:"wait_id"`
	EarlyRecoveryUsed  bool       `json:"early_recovery_used"`
}

type ReadinessReport struct {
	CheckID         string               `json:"check_id,omitempty"`
	BindingRevision *int64               `json:"binding_revision"`
	Result          string               `json:"result"`
	Facts           []ReadinessFactWrite `json:"facts,omitempty"`
}

func checkResultOK(s string) bool {
	switch s {
	case "success", "unsupported", "timeout", "protocol", "launch_failed", "identity_mismatch", "authentication_failed":
		return true
	}
	return false
}

func (v ReadinessFactWrite) validate(now time.Time) error {
	if !uuidRE.MatchString(v.ResourceID) || v.WindowKey == "check" || len(v.WindowKey) > 128 || !accountKeyRE.MatchString(v.WindowKey) || looksLikeCredential(v.WindowKey) {
		return fail(400, "invalid readiness resource/window")
	}
	if v.Source != "agentd" && v.Source != "harness" && v.Source != "provider" {
		return fail(400, "invalid readiness source")
	}
	if v.ObservedAt.IsZero() || v.ObservedAt.After(now.Add(time.Minute)) || v.ObservedAt.Before(now.Add(-24*time.Hour)) {
		return fail(400, "invalid readiness observation time")
	}
	if v.ReadingAt != nil && (v.ReadingAt.After(v.ObservedAt) || v.ReadingAt.IsZero()) {
		return fail(400, "invalid readiness reading time")
	}
	if v.UsedPercent != nil && (v.ReadingAt == nil || math.IsNaN(*v.UsedPercent) || *v.UsedPercent < 0 || *v.UsedPercent > 100) {
		return fail(400, "invalid readiness usage")
	}
	if v.Remaining != nil && (v.ReadingAt == nil || math.IsNaN(*v.Remaining) || math.IsInf(*v.Remaining, 0) || *v.Remaining < 0) {
		return fail(400, "invalid readiness credit")
	}
	if v.CreditState != "unknown" && v.CreditState != "available" && v.CreditState != "exhausted" {
		return fail(400, "invalid readiness credit state")
	}
	switch v.StopKind {
	case "", "none", "unnamed":
	case "named_reset":
		if v.ResetsAt == nil {
			return fail(400, "named reset requires reset time")
		}
	case "money_402":
		if v.ResetsAt != nil || v.Source != "provider" {
			return fail(400, "money stop requires provider-confirmed 402 without reset")
		}
	default:
		return fail(400, "invalid readiness stop")
	}
	switch v.DenialReason {
	case "", "vendor_denied", "quota_exhausted", "key_cap_exhausted", "money_exhausted":
	default:
		return fail(400, "invalid readiness denial")
	}
	return nil
}

// ReadinessResources is bounded and includes only the current binding.
func ReadinessResources(ctx context.Context, tx pgx.Tx, a Account) ([]ReadinessResource, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.kind,m.binding_revision FROM account_readiness_memberships m JOIN account_readiness_resources r ON r.tenant_id=m.tenant_id AND r.id=m.resource_id WHERE m.account_id=$1 AND m.binding_revision=$2 ORDER BY r.id LIMIT 17`, a.ID, a.LinkRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReadinessResource{}
	for rows.Next() {
		var v ReadinessResource
		if err := rows.Scan(&v.ID, &v.Kind, &v.BindingRevision); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if len(out) > 16 {
		return nil, fail(503, "too many readiness resources")
	}
	return out, rows.Err()
}

func loadReadinessFacts(ctx context.Context, tx pgx.Tx, a Account, now time.Time) ([]ReadinessFact, error) {
	rows, err := tx.Query(ctx, `SELECT f.resource_id::text,f.window_key,f.source,f.observed_at,f.resets_at,f.reading_at,f.used_percent,f.credit_state,f.remaining,f.stop_kind,f.denial_reason,f.reading_error,f.failure_count,f.check_next_attempt_at,f.backoff_step,f.next_attempt_at,f.wait_id::text,f.early_recovery_used FROM account_readiness_facts f JOIN account_readiness_memberships m ON m.tenant_id=f.tenant_id AND m.resource_id=f.resource_id WHERE m.account_id=$1 AND m.binding_revision=$2 AND (f.window_key<>'check' OR (f.reported_by_account_id=$1 AND f.binding_revision=$2)) ORDER BY f.resource_id,f.window_key LIMIT 33`, a.ID, a.LinkRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReadinessFact{}
	for rows.Next() {
		var v ReadinessFact
		if err := rows.Scan(&v.ResourceID, &v.WindowKey, &v.Source, &v.ObservedAt, &v.ResetsAt, &v.ReadingAt, &v.UsedPercent, &v.CreditState, &v.Remaining, &v.StopKind, &v.DenialReason, &v.ReadingError, &v.FailureCount, &v.CheckNextAttemptAt, &v.BackoffStep, &v.NextAttemptAt, &v.WaitID, &v.EarlyRecoveryUsed); err != nil {
			return nil, err
		}
		if v.ReadingAt != nil {
			age := max(int64(0), int64(now.Sub(*v.ReadingAt)/time.Second))
			v.ReadingAgeSeconds = &age
		}
		out = append(out, v)
	}
	if len(out) > 32 {
		return nil, fail(503, "too many readiness facts")
	}
	return out, rows.Err()
}

// storeReadinessFact retains hard stops across failed/null readings. A 402 can
// only be cleared by package B's evidenced successful recovery inference. An
// ordinary check, null-cap response or unrelated bucket cannot clear it.
func storeReadinessFact(ctx context.Context, tx pgx.Tx, a Account, v ReadinessFactWrite, now time.Time) error {
	if v.StopKind == "" {
		v.StopKind = "none"
	}
	if v.StopKind == "none" && (v.CreditState == "exhausted" || v.UsedPercent != nil && *v.UsedPercent >= 100 || v.Remaining != nil && *v.Remaining == 0) {
		if v.ResetsAt != nil {
			v.StopKind = "named_reset"
		} else {
			v.StopKind = "unnamed"
		}
	}
	var waitID *string
	var next *time.Time
	if v.StopKind == "unnamed" || v.StopKind == "money_402" {
		var id string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return err
		}
		waitID = &id
		t := now.Add(time.Hour)
		next = &t
	}
	// Only a newer, measured, same-window room observation can clear a quota
	// stop, and the named reset must refer to that resource's window. Absent
	// measurements never create replenishment evidence or move the retained
	// stop's observation time past a subsequently delivered room reading.
	room := v.ReadingAt != nil && v.CreditState != "exhausted" && (v.UsedPercent != nil && *v.UsedPercent < 100 || v.Remaining != nil && *v.Remaining > 0)
	_, err := tx.Exec(ctx, `INSERT INTO account_readiness_facts(tenant_id,resource_id,window_key,reported_by_account_id,binding_revision,source,observed_at,resets_at,reading_at,used_percent,credit_state,remaining,stop_kind,denial_reason,wait_id,next_attempt_at)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
        ON CONFLICT(tenant_id,resource_id,window_key) DO UPDATE SET
        reported_by_account_id=EXCLUDED.reported_by_account_id,binding_revision=EXCLUDED.binding_revision,source=EXCLUDED.source,
        observed_at=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (account_readiness_facts.stop_kind<>'none' AND EXCLUDED.stop_kind='none' AND NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at)) THEN account_readiness_facts.observed_at ELSE EXCLUDED.observed_at END,
        reading_at=COALESCE(EXCLUDED.reading_at,account_readiness_facts.reading_at),used_percent=CASE WHEN EXCLUDED.reading_at IS NULL THEN account_readiness_facts.used_percent ELSE EXCLUDED.used_percent END,
        remaining=CASE WHEN EXCLUDED.reading_at IS NULL THEN account_readiness_facts.remaining ELSE EXCLUDED.remaining END,credit_state=CASE WHEN EXCLUDED.reading_at IS NULL AND account_readiness_facts.credit_state='exhausted' THEN account_readiness_facts.credit_state ELSE EXCLUDED.credit_state END,
        resets_at=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at) AND EXCLUDED.stop_kind='none' AND account_readiness_facts.stop_kind<>'none') THEN account_readiness_facts.resets_at ELSE EXCLUDED.resets_at END,
        stop_kind=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at) AND EXCLUDED.stop_kind='none') THEN account_readiness_facts.stop_kind ELSE EXCLUDED.stop_kind END,
        denial_reason=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at) AND EXCLUDED.stop_kind='none') THEN account_readiness_facts.denial_reason ELSE EXCLUDED.denial_reason END,
        wait_id=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (account_readiness_facts.stop_kind='unnamed' AND (EXCLUDED.stop_kind='unnamed' OR (EXCLUDED.stop_kind='none' AND NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at)))) THEN account_readiness_facts.wait_id ELSE EXCLUDED.wait_id END,
        next_attempt_at=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (account_readiness_facts.stop_kind='unnamed' AND (EXCLUDED.stop_kind='unnamed' OR (EXCLUDED.stop_kind='none' AND NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at)))) THEN account_readiness_facts.next_attempt_at ELSE EXCLUDED.next_attempt_at END,
        backoff_step=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (account_readiness_facts.stop_kind='unnamed' AND (EXCLUDED.stop_kind='unnamed' OR (EXCLUDED.stop_kind='none' AND NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at)))) THEN account_readiness_facts.backoff_step ELSE 0 END,
        early_recovery_used=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (account_readiness_facts.stop_kind='unnamed' AND (EXCLUDED.stop_kind='unnamed' OR (EXCLUDED.stop_kind='none' AND NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at)))) THEN account_readiness_facts.early_recovery_used ELSE false END,
        recovery_run_id=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (account_readiness_facts.stop_kind='unnamed' AND (EXCLUDED.stop_kind='unnamed' OR (EXCLUDED.stop_kind='none' AND NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at)))) THEN account_readiness_facts.recovery_run_id ELSE NULL END,
        recovery_check_id=CASE WHEN account_readiness_facts.stop_kind='money_402' OR (account_readiness_facts.stop_kind='unnamed' AND (EXCLUDED.stop_kind='unnamed' OR (EXCLUDED.stop_kind='none' AND NOT ($17 AND EXCLUDED.reading_at>account_readiness_facts.observed_at)))) THEN account_readiness_facts.recovery_check_id ELSE NULL END
        WHERE EXCLUDED.observed_at>account_readiness_facts.observed_at`, aTenant(ctx), v.ResourceID, v.WindowKey, a.ID, a.LinkRevision, v.Source, v.ObservedAt, v.ResetsAt, v.ReadingAt, v.UsedPercent, v.CreditState, v.Remaining, v.StopKind, v.DenialReason, waitID, next, room)
	return err
}

func aTenant(ctx context.Context) string {
	// The report handler always supplies its authenticated tenant context.
	// No resource identifier is permitted to supply a tenant.
	p, _ := tenant.PrincipalFrom(ctx)
	return p.TenantID
}

// Legacy Pi probes also contribute durable key facts. Otherwise a later null
// cap replaces the account's credit snapshot and silently erases known stops.
func storeLegacyKeyFact(ctx context.Context, tx pgx.Tx, a Account, now time.Time) error {
	resource, err := localReadinessResource(ctx, tx, a)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM account_readiness_resources WHERE id=$1 FOR NO KEY UPDATE`, resource); err != nil {
		return err
	}
	c := a.OpenRouterCredits
	fact := ReadinessFactWrite{ResourceID: resource, WindowKey: "key_cap", Source: "provider", ObservedAt: c.ObservedAt, ReadingAt: &c.ObservedAt, Remaining: c.KeyRemaining(), CreditState: "unknown", StopKind: "none"}
	if fact.Remaining != nil && *fact.Remaining == 0 {
		fact.CreditState = "exhausted"
		fact.DenialReason = "key_cap_exhausted"
	}
	return storeReadinessFact(ctx, tx, a, fact, now)
}

func completeReadinessReport(ctx context.Context, tx pgx.Tx, a Account, generation string, in ReadinessReport, now time.Time) error {
	if in.BindingRevision == nil || *in.BindingRevision != a.LinkRevision {
		return &httpError{status: 409, code: "stale_binding", msg: "readiness binding changed"}
	}
	if !checkResultOK(in.Result) || len(in.Facts) > 32 || in.Result != "success" && len(in.Facts) > 0 {
		return fail(400, "invalid readiness result")
	}
	if in.CheckID != "" {
		if !uuidRE.MatchString(in.CheckID) {
			return fail(400, "invalid check id")
		}
		c, err := scanCheck(tx.QueryRow(ctx, `SELECT `+checkColumns+` FROM account_readiness_checks WHERE id=$1 AND account_id=$2 FOR NO KEY UPDATE`, in.CheckID, a.ID))
		if isNoRows(err) {
			return fail(404, "check not found")
		}
		if err != nil {
			return err
		}
		if c.BindingRevision != a.LinkRevision || c.State == "invalidated" || c.State == "pending" && !now.Before(c.ExpiresAt) || c.DaemonGeneration != nil && *c.DaemonGeneration != generation {
			return &httpError{status: 409, code: "stale_binding", msg: "check binding changed"}
		}
		if c.State == "completed" {
			if c.Result != nil && *c.Result == in.Result {
				return nil
			}
			return fail(409, "check result already recorded")
		}
	}
	resources, err := ReadinessResources(ctx, tx, a)
	if err != nil {
		return err
	}
	members := map[string]bool{}
	for _, r := range resources {
		members[r.ID] = true
	}
	// Validate everything before acquiring fact locks in resource/window order.
	seen := map[string]bool{}
	for _, v := range in.Facts {
		if err := v.validate(now); err != nil {
			return err
		}
		if !members[v.ResourceID] {
			return fail(403, "current readiness membership required")
		}
		key := v.ResourceID + "/" + v.WindowKey
		if seen[key] {
			return fail(400, "duplicate readiness window")
		}
		seen[key] = true
	}
	sort.Slice(in.Facts, func(i, j int) bool {
		a, b := in.Facts[i], in.Facts[j]
		if a.ResourceID != b.ResourceID {
			return a.ResourceID < b.ResourceID
		}
		return a.WindowKey < b.WindowKey
	})
	// The pairing lock already serializes same-tenant reports. Explicit resource
	// row locks provide the contract for B's future reserve/claim integration.
	for _, resource := range resources {
		if _, err := tx.Exec(ctx, `SELECT id FROM account_readiness_resources WHERE id=$1 FOR NO KEY UPDATE`, resource.ID); err != nil {
			return err
		}
	}
	for _, v := range in.Facts {
		if err := storeReadinessFact(ctx, tx, a, v, now); err != nil {
			return err
		}
	}
	readingError := in.Result
	if readingError == "success" {
		readingError = ""
	}
	local, err := localReadinessResource(ctx, tx, a)
	if err != nil {
		return err
	}
	// Capture failures belong to this account's local resource, never a shared
	// balance or another account's local resource that B has pooled with it.
	for _, resource := range resources {
		if resource.ID != local {
			continue
		}
		_, err := tx.Exec(ctx, `INSERT INTO account_readiness_facts(tenant_id,resource_id,window_key,reported_by_account_id,binding_revision,source,observed_at,reading_error,failure_count,check_next_attempt_at)
            VALUES($1,$2,'check',$3,$4,'agentd',$5,$6,CASE WHEN $6='' THEN 0 ELSE 1 END,CASE WHEN $6='' OR $6='unsupported' THEN NULL ELSE $5::timestamptz+interval '1 minute' END)
            ON CONFLICT(tenant_id,resource_id,window_key) DO UPDATE SET observed_at=EXCLUDED.observed_at,reading_error=EXCLUDED.reading_error,reported_by_account_id=EXCLUDED.reported_by_account_id,binding_revision=EXCLUDED.binding_revision,
            failure_count=CASE WHEN $6='' THEN 0 WHEN account_readiness_facts.binding_revision<>EXCLUDED.binding_revision THEN 1 ELSE least(account_readiness_facts.failure_count+1,1000000) END,
            check_next_attempt_at=CASE WHEN $6='' OR $6='unsupported' THEN NULL ELSE $5::timestamptz+CASE account_readiness_facts.failure_count WHEN 0 THEN interval '1 minute' WHEN 1 THEN interval '2 minutes' WHEN 2 THEN interval '4 minutes' ELSE interval '30 minutes' END END`, aTenant(ctx), resource.ID, a.ID, a.LinkRevision, now, readingError)
		if err != nil {
			return err
		}
	}
	if in.CheckID != "" {
		_, err = tx.Exec(ctx, `UPDATE account_readiness_checks SET state='completed',result=$2,completed_at=$3 WHERE id=$1 AND state='pending'`, in.CheckID, in.Result, now)
	}
	return err
}

// ensureLocalReadinessResource upgrades an existing account during a fenced
// write. New accounts are seeded by migration 1135's insert trigger. Shared
// identities and outstanding hold reconciliation remain package B's job.
func ensureLocalReadinessResource(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Account) error {
	kind := "subscription_quota"
	if a.Harness == "pi" {
		kind = "key_cap"
	}
	sum := sha256.Sum256([]byte("account:" + a.ID + ":" + kind))
	var resource string
	err := tx.QueryRow(ctx, `INSERT INTO account_readiness_resources(tenant_id,kind,identity_kind,identity_key) VALUES($1,$2,'account',$3)
        ON CONFLICT(tenant_id,kind,identity_kind,identity_key) DO UPDATE SET identity_key=EXCLUDED.identity_key RETURNING id::text`, p.TenantID, kind, hex.EncodeToString(sum[:])).Scan(&resource)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_readiness_memberships(tenant_id,account_id,resource_id,binding_revision) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,account_id,resource_id) DO UPDATE SET binding_revision=EXCLUDED.binding_revision`, p.TenantID, a.ID, resource, a.LinkRevision)
	return err
}

// The same canonical local membership is used for legacy observations and
// per-account capture status; read paths never invent resource identifiers.
func localReadinessResource(ctx context.Context, tx pgx.Tx, a Account) (string, error) {
	kind := "subscription_quota"
	if a.Harness == "pi" {
		kind = "key_cap"
	}
	sum := sha256.Sum256([]byte("account:" + a.ID + ":" + kind))
	var id string
	err := tx.QueryRow(ctx, `SELECT r.id::text FROM account_readiness_memberships m JOIN account_readiness_resources r ON r.tenant_id=m.tenant_id AND r.id=m.resource_id WHERE m.account_id=$1 AND m.binding_revision=$2 AND r.identity_kind='account' AND r.kind=$3 AND r.identity_key=$4`, a.ID, a.LinkRevision, kind, hex.EncodeToString(sum[:])).Scan(&id)
	return id, err
}
