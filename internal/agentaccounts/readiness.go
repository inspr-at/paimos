// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

const UsageUnknownReason = "usage_unknown_reserve_not_enforceable"

// AccountReadiness is advice, never a permit, reservation, or a prediction that
// work will finish. B must recheck model/project and execution gates at launch.
type AccountReadiness struct {
	AccountID       string          `json:"account_id"`
	State           string          `json:"state"`
	CanTry          bool            `json:"can_try"`
	ReasonCodes     []string        `json:"reason_codes"`
	DisplayReason   string          `json:"display_reason"`
	CheckedAt       *time.Time      `json:"checked_at"`
	NextAttemptAt   *time.Time      `json:"next_attempt_at"`
	MeasuredUsage   []ReadinessFact `json:"measured_usage"`
	CheckResult     string          `json:"check_result,omitempty"`
	DetailsRedacted bool            `json:"details_redacted"`
}

// ReadinessInput separates known blocking facts from measurement. Missing or
// stale usage adds an honest reason but never a start budget or serial limit.
type ReadinessInput struct {
	AccountID   string
	Now         time.Time
	CheckedAt   *time.Time
	HardReasons []string
	Facts       []ReadinessFact
}

var readinessReasonOrder = []string{"owner_required", "approval_required", "paused", "hold", "sign_in", "identity_mismatch", "launcher_unavailable", "offline", "slots_occupied", "models", "vendor_denied", "quota_exhausted", "key_cap_exhausted", "money_exhausted", "manual_limit", "schedule", "reserve"}

func ProjectReadiness(in ReadinessInput) AccountReadiness {
	out := AccountReadiness{AccountID: in.AccountID, State: "unknown", CanTry: true, ReasonCodes: []string{}, CheckedAt: in.CheckedAt}
	unknownMeasurement := false
	reasons := map[string]bool{}
	for _, r := range in.HardReasons {
		reasons[r] = true
	}
	var checkedAt *time.Time
	for _, f := range in.Facts {
		if f.WindowKey == "check" && (checkedAt == nil || f.ObservedAt.After(*checkedAt)) {
			t := f.ObservedAt
			checkedAt = &t
			out.CheckedAt = &t
			out.CheckResult = f.ReadingError
			if out.CheckResult == "" {
				out.CheckResult = "success"
			}
		}

		if f.ReadingError == "identity_mismatch" {
			reasons["identity_mismatch"] = true
		}
		if f.ReadingError == "authentication_failed" {
			reasons["sign_in"] = true
		}
		active := f.StopKind != "none" && f.StopKind != "" && (f.StopKind != "named_reset" || f.ResetsAt == nil || in.Now.Before(*f.ResetsAt))
		if active {
			reason := f.DenialReason
			if reason == "" {
				reason = "vendor_denied"
			}
			if f.StopKind == "money_402" {
				reason = "money_exhausted"
			}
			reasons[reason] = true
			retry := f.NextAttemptAt
			if f.StopKind == "named_reset" {
				retry = f.ResetsAt
			}
			if retry != nil && (out.NextAttemptAt == nil || retry.After(*out.NextAttemptAt)) {
				t := *retry
				out.NextAttemptAt = &t
			}
		}
		// A positive ordinary-key cap says nothing about the provider balance.
		if (f.WindowKey == "key_cap" || f.Remaining != nil) && f.CreditState == "unknown" {
			unknownMeasurement = true
		}
		fresh := f.ReadingAt != nil && !in.Now.Before(*f.ReadingAt) && in.Now.Sub(*f.ReadingAt) <= 10*time.Minute && (f.ResetsAt == nil || in.Now.Before(*f.ResetsAt))
		if f.WindowKey != "check" && (!fresh || f.UsedPercent == nil && f.Remaining == nil) {
			unknownMeasurement = true
		}
		if fresh && (f.UsedPercent != nil || f.Remaining != nil) {
			out.MeasuredUsage = append(out.MeasuredUsage, f)
		}
		// Hard exhausted values persist past measurement freshness, but their
		// own named reset is a resource-specific boundary.
		if f.ResetsAt == nil || in.Now.Before(*f.ResetsAt) {
			if f.UsedPercent != nil && *f.UsedPercent >= 100 {
				reasons["quota_exhausted"] = true
			}
			if f.CreditState == "exhausted" || f.Remaining != nil && *f.Remaining == 0 {
				if f.StopKind == "money_402" {
					reasons["money_exhausted"] = true
				} else {
					reasons["key_cap_exhausted"] = true
				}
			}
		}
		if f.CheckNextAttemptAt != nil && (out.NextAttemptAt == nil || f.CheckNextAttemptAt.After(*out.NextAttemptAt)) {
			t := *f.CheckNextAttemptAt
			out.NextAttemptAt = &t
		}
	}
	for _, reason := range readinessReasonOrder {
		if reasons[reason] {
			out.ReasonCodes = append(out.ReasonCodes, reason)
			out.CanTry = false
		}
	}
	if len(out.MeasuredUsage) == 0 || unknownMeasurement {
		out.ReasonCodes = append(out.ReasonCodes, UsageUnknownReason)
	} else {
		out.State = "ready"
	}
	if !out.CanTry {
		out.State = "blocked"
	}
	if len(out.ReasonCodes) > 0 {
		out.DisplayReason = out.ReasonCodes[0]
	} else {
		out.DisplayReason = "ready"
	}
	return out
}

// Legacy readings remain readable during rollout. Selection is by the same
// resource/window, never by a different bucket or allowance retirement bit.
func legacyReadinessFacts(ctx context.Context, tx pgx.Tx, a Account, now time.Time) ([]ReadinessFact, error) {
	resource, err := localReadinessResource(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT window_kind,bucket,window_minutes,used_percent::float8,resets_at,read_at,source,ordinary_usage_allowed FROM (
        SELECT DISTINCT ON(window_kind,bucket) * FROM account_capacity_readings WHERE account_id IN (`+quotaAccounts+`) AND source<>'estimate'
        ORDER BY window_kind,bucket,read_at DESC,CASE source WHEN 'harness' THEN 0 ELSE 1 END) r ORDER BY window_kind,bucket LIMIT 33`, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReadinessFact{}
	for rows.Next() {
		var kind, bucket, source string
		var used float64
		var minutes int
		var reset, at time.Time
		var allowed *bool
		if err := rows.Scan(&kind, &bucket, &minutes, &used, &reset, &at, &source, &allowed); err != nil {
			return nil, err
		}
		age := max(int64(0), int64(now.Sub(at)/time.Second))
		f := ReadinessFact{ReadinessFactWrite: ReadinessFactWrite{ResourceID: resource, WindowKey: kind + ":" + bucket, Source: source, ObservedAt: at, ReadingAt: &at, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown", StopKind: "none"}, ReadingAgeSeconds: &age}
		if used >= 100 || allowed != nil && !*allowed {
			f.StopKind = "named_reset"
			f.DenialReason = "vendor_denied"
			if used >= 100 {
				f.DenialReason = "quota_exhausted"
			}
		}
		f.legacyReading = &capacity.Reading{WindowKind: kind, Bucket: bucket, WindowMinutes: minutes, UsedPercent: used, ResetsAt: reset, ReadAt: at, Source: source}
		out = append(out, f)
	}
	if len(out) > 32 {
		return nil, fail(503, "too many readiness windows")
	}
	return out, rows.Err()
}

func loadReadiness(ctx context.Context, tx pgx.Tx, a Account, now time.Time, slots int) (AccountReadiness, error) {
	in := ReadinessInput{AccountID: a.ID, Now: now}
	if a.OwnerPersonID == nil {
		in.HardReasons = append(in.HardReasons, "owner_required")
	}
	if !a.OngoingUseApproved {
		in.HardReasons = append(in.HardReasons, "approval_required")
	}
	if a.State != "available" {
		in.HardReasons = append(in.HardReasons, "paused")
	}
	if !probeFresh(a, now) {
		var failure string
		if err := tx.QueryRow(ctx, `SELECT last_probe_failure FROM agent_accounts WHERE id=$1`, a.ID).Scan(&failure); err != nil {
			return AccountReadiness{}, err
		}
		if failure == "auth_failed" {
			in.HardReasons = append(in.HardReasons, "sign_in")
		} else {
			in.HardReasons = append(in.HardReasons, "offline")
		}
	}
	if slots >= a.MaxParallel {
		in.HardReasons = append(in.HardReasons, "slots_occupied")
	}
	var approved, models bool
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE e.account_id=$1 AND (e.state<>'connected' OR c.state<>'connected' OR q.state<>'redeemed' OR e.ongoing_approved_at IS NULL)),
        EXISTS(SELECT 1 FROM model_profiles WHERE enabled AND harness=$2 AND ($3::uuid[] IS NULL OR id=ANY($3::uuid[])))`, a.ID, a.Harness, a.AllowedProfileIDs).Scan(&approved, &models); err != nil {
		return AccountReadiness{}, err
	}
	if !approved {
		in.HardReasons = append(in.HardReasons, "approval_required")
	}
	if !models {
		in.HardReasons = append(in.HardReasons, "models")
	}
	facts, err := admissionFacts(ctx, tx, a, now)
	if err != nil {
		return AccountReadiness{}, err
	}
	in.Facts = facts
	legacy, err := legacyReadinessFacts(ctx, tx, a, now)
	if err != nil {
		return AccountReadiness{}, err
	}
	for _, fact := range in.Facts {
		if fact.ReadingAt != nil && (in.CheckedAt == nil || fact.ReadingAt.After(*in.CheckedAt)) {
			t := *fact.ReadingAt
			in.CheckedAt = &t
		}
	}
	s, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return AccountReadiness{}, err
	}
	switch s.ActiveOverride(now) {
	case "hold":
		in.HardReasons = append(in.HardReasons, "hold")
	case "sprint", "away":
	default:
		next := s.NextStart(now, s.OffDays == "normal")
		if next == nil || next.After(now) {
			in.HardReasons = append(in.HardReasons, "schedule")
		}
	}
	for _, w := range a.Windows {
		if w.capacityReadAt == nil && !w.pairingVerification && !now.Before(w.StartsAt) && now.Before(w.EndsAt) {
			if w.Used+w.Reserved >= w.Allowance {
				in.HardReasons = append(in.HardReasons, "manual_limit")
			}
		}
	}
	limit, err := accountLimitUse(ctx, tx, a, now)
	if err != nil {
		return AccountReadiness{}, err
	}
	if limit != nil && limit.Used >= float64(limit.Amount) {
		in.HardReasons = append(in.HardReasons, "manual_limit")
	}
	for _, v := range legacy {
		if v.ReadingAt != nil && now.Sub(*v.ReadingAt) <= 10*time.Minute && v.ResetsAt != nil && now.Before(*v.ResetsAt) {
			reading := *v.legacyReading
			// Preserve the existing measured schedule/reserve path. Unknown
			// measurements never enter it, so no invented reserve is enforced.
			plan, _, err := readingPacing(ctx, tx, a.ID, reading, now, s)
			if err != nil {
				return AccountReadiness{}, err
			}
			if plan.AvailableNowPercent <= 0 && *v.UsedPercent < 100 {
				reason := "schedule"
				if _, binds := plan.ReserveBinds(); binds {
					reason = "reserve"
				}
				in.HardReasons = append(in.HardReasons, reason)
			}
		}
	}
	out := ProjectReadiness(in)
	return out, nil
}

func (m *Module) readinessList(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	after := strings.ToLower(r.URL.Query().Get("after"))
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeErr(w, fail(400, "limit must be between 1 and 100"))
			return
		}
		limit = n
	}
	if after != "" && !uuidRE.MatchString(after) {
		writeErr(w, fail(400, "invalid readiness cursor"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := struct {
		Items     []AccountReadiness `json:"items"`
		NextAfter *string            `json:"next_after"`
	}{Items: []AccountReadiness{}}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(ctx, tx, p, "account.read", authz.Scope{}) != nil {
			return fail(403, "account read permission required")
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM agent_accounts WHERE NOT `+retiredSQL+` AND ($1::uuid IS NULL OR id>$1::uuid) AND ($2='person' OR registered_by_principal_id=$3::uuid) ORDER BY id LIMIT $4`, nullableUUID(after), string(p.Kind), p.ID, limit+1)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) > limit {
			ids = ids[:limit]
			last := ids[len(ids)-1]
			out.NextAfter = &last
		}
		used, err := occupancy(ctx, tx)
		if err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			a, err := getAccount(ctx, tx, id)
			if err != nil {
				return err
			}
			item, err := loadReadiness(ctx, tx, a, now, used[id])
			if err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
