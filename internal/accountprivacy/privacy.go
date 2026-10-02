// SPDX-License-Identifier: AGPL-3.0-only
// Package accountprivacy applies the same owner-sharing boundary to HTTP
// projections and durable event replay. Administrators have no implicit bypass.
package accountprivacy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Policy map[string]bool

// Load uses current sharing, including on replay of older events. An account
// cannot disclose a private sibling's shared quota or resource through its own
// projection. No detail is released if the owner is unknown.
func Load(ctx context.Context, tx pgx.Tx, p tenant.Principal, ids []string) (Policy, error) {
	if len(ids) > 1024 {
		return nil, errors.New("too many account privacy targets")
	}
	rows, err := tx.Query(ctx, `SELECT a.id::text,
      COALESCE((a.share_usage OR (a.owner_person_id=$2::uuid AND $3='person') OR (a.registered_by_principal_id=$2::uuid AND $3='agent')),false)
      AND NOT EXISTS (SELECT 1 FROM agent_accounts peer WHERE peer.id<>a.id AND (
        (a.quota_pool_fingerprint<>'' AND peer.quota_pool_fingerprint=a.quota_pool_fingerprint) OR
        EXISTS(SELECT 1 FROM account_readiness_memberships m JOIN account_readiness_memberships pm ON pm.tenant_id=m.tenant_id AND pm.resource_id=m.resource_id
          WHERE m.account_id=a.id AND m.binding_revision=a.link_revision AND pm.account_id=peer.id AND pm.binding_revision=peer.link_revision))
        AND NOT COALESCE((peer.share_usage OR (peer.owner_person_id=$2::uuid AND $3='person') OR (peer.registered_by_principal_id=$2::uuid AND $3='agent')),false))
      FROM agent_accounts a WHERE a.id=ANY($1::uuid[])`, ids, p.ID, string(p.Kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Policy{}
	for rows.Next() {
		var id string
		var allowed bool
		if err := rows.Scan(&id, &allowed); err != nil {
			return nil, err
		}
		out[id] = allowed
	}
	return out, rows.Err()
}

func IDs(raw json.RawMessage, fallback string) ([]string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	if fallback != "" {
		seen[fallback] = true
	}
	var walk func(any, int) error
	walk = func(v any, depth int) error {
		if depth > 32 {
			return errors.New("account response nesting exceeds bound")
		}
		switch obj := v.(type) {
		case []any:
			for _, child := range obj {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			if id := accountID(obj); id != "" {
				seen[id] = true
			}
			for _, child := range obj {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		if len(seen) > 1024 {
			return errors.New("too many account privacy targets")
		}
		return nil
	}
	if err := walk(v, 0); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

func accountID(obj map[string]any) string {
	if id, ok := obj["account_id"].(string); ok && uuid(id) {
		return id
	}
	if _, ok := obj["registered_by_principal_id"]; ok {
		if id, ok := obj["id"].(string); ok && uuid(id) {
			return id
		}
	}
	return ""
}
func uuid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// Redact preserves legacy required shapes: empty arrays, null probe timestamps
// and empty fingerprint strings mean withheld data, never measured zero. New
// availability projections carry details_redacted. History becomes an empty
// array. The registering daemon's own scoped reports remain unaffected.
func Redact(raw json.RawMessage, policy Policy, fallback string, history bool) (json.RawMessage, error) {
	if history && !policy[fallback] {
		return json.RawMessage(`[]`), nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var walk func(any, string, int) (any, error)
	walk = func(v any, id string, depth int) (any, error) {
		if depth > 32 {
			return nil, errors.New("account response nesting exceeds bound")
		}
		switch obj := v.(type) {
		case []any:
			for i, child := range obj {
				next, err := walk(child, id, depth+1)
				if err != nil {
					return nil, err
				}
				obj[i] = next
			}
		case map[string]any:
			if own := accountID(obj); own != "" {
				id = own
			}
			if !policy[id] {
				mask(obj)
			}
			for key, child := range obj {
				next, err := walk(child, id, depth+1)
				if err != nil {
					return nil, err
				}
				obj[key] = next
			}
		}
		return v, nil
	}
	v, err := walk(v, fallback, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func mask(obj map[string]any) {
	// Strip detailed collections at their boundary before visiting children.
	for _, key := range []string{"windows", "readings", "readiness_resources", "facts"} {
		if _, ok := obj[key]; ok {
			obj[key] = []any{}
		}
	}
	for _, key := range []string{"plan", "quota_fingerprint", "quota_pool_fingerprint"} {
		if _, ok := obj[key]; ok {
			obj[key] = ""
		}
	}
	for _, key := range []string{"openrouter_credits", "probe_failure", "limiting_reset", "limit", "spend_month_usd", "cost_limit_supported", "learning", "same_quota_as", "hosts", "resets_at", "until", "read_at", "next_attempt_at", "reading_error", "check_result", "reading_age_seconds", "credit_state", "remaining", "used_percent", "denial_reason", "stop_kind", "backoff_step", "wait_id", "early_recovery_used", "pending_check", "result", "cap_percent", "reserve_percent", "reserve_effective_percent", "reserve_until", "awaiting_reading"} {
		delete(obj, key)
	}
	// Old account timestamps are nullable in the published contract.
	if _, ok := obj["remaining_fraction"]; ok {
		obj["remaining_fraction"] = nil
	}
	if _, ok := obj["unavailable_reasons"]; ok {
		reasons := []string{}
		if available, _ := obj["available"].(bool); !available {
			reasons = append(reasons, "unavailable")
		}
		obj["unavailable_reasons"] = reasons
	}
	if _, ok := obj["last_probe_at"]; ok {
		obj["last_probe_at"] = nil
	}
	if _, ok := obj["last_probe_ok"]; ok {
		obj["last_probe_ok"] = nil
	}
	if _, ok := obj["checked_at"]; ok {
		obj["checked_at"] = nil
	}
	if _, ok := obj["measured_usage"]; ok {
		obj["measured_usage"] = nil
		obj["next_attempt_at"] = nil
		canTry, _ := obj["can_try"].(bool)
		reason := "unavailable"
		if canTry {
			reason = "available"
		}
		obj["reason_codes"] = []string{reason}
		obj["display_reason"] = reason
		// State is availability only, never a claim of known quota headroom.
		if canTry {
			obj["state"] = "unknown"
		} else {
			obj["state"] = "blocked"
		}
	}
	if _, ok := obj["code"]; ok {
		obj["code"] = "unavailable"
	}
	// CapacitySchedule has required timezone/week. Keep a valid neutral
	// placeholder and mark the containing projection, never expose its owner
	// calendar/reserve. Consumers must ignore schedule when details are masked.
	if _, ok := obj["schedule"]; ok {
		week := make([]any, 7)
		for i := range week {
			week[i] = map[string]any{"on": true, "start": 0, "end": 24}
		}
		obj["schedule"] = map[string]any{"timezone": "UTC", "week": week}
	}
	if _, ok := obj["registered_by_principal_id"]; ok {
		obj["details_redacted"] = true
	}
	if _, ok := obj["schedule"]; ok {
		obj["details_redacted"] = true
	}
	if _, ok := obj["measured_usage"]; ok {
		obj["details_redacted"] = true
	}
}

// EventSnapshot masks snapshots with no identifiable account too. Older signal
// events did not carry an account ID; those cannot be safely disclosed. Audit
// IDs/actors remain in the event envelope. Never erase the durable source.
func EventSnapshot(raw json.RawMessage, policy Policy, fallback string) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var walk func(any, string, int) (any, error)
	walk = func(v any, id string, depth int) (any, error) {
		if depth > 32 {
			return nil, errors.New("account event nesting exceeds bound")
		}
		switch obj := v.(type) {
		case []any:
			for i, child := range obj {
				next, err := walk(child, id, depth+1)
				if err != nil {
					return nil, err
				}
				obj[i] = next
			}
		case map[string]any:
			if own := accountID(obj); own != "" {
				id = own
			}
			if !policy[id] {
				safe := map[string]bool{"id": true, "account_id": true, "actor_principal_id": true, "check_id": true, "binding_revision": true, "harness": true, "daemon_id": true, "label": true, "state": true, "ongoing_use_approved": true, "owner_person_id": true, "linked_at": true, "link_revision": true, "registered_by_principal_id": true, "share_usage": true, "max_parallel_runs": true}
				for key := range obj {
					if !safe[key] {
						delete(obj, key)
					}
				}
			}
			for key, child := range obj {
				next, err := walk(child, id, depth+1)
				if err != nil {
					return nil, err
				}
				obj[key] = next
			}
		}
		return v, nil
	}
	v, err := walk(v, fallback, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
