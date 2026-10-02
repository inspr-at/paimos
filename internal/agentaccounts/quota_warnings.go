// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type QuotaWarningSettings struct {
	EarlyPercent  int `json:"early_percent"`
	UrgentPercent int `json:"urgent_percent"`
}

func (s QuotaWarningSettings) valid() bool {
	return s.UrgentPercent >= 1 && s.EarlyPercent <= 50 && s.UrgentPercent < s.EarlyPercent
}
func quotaWarningSettings(ctx context.Context, tx pgx.Tx) (QuotaWarningSettings, error) {
	var s QuotaWarningSettings
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT early_percent FROM quota_warning_settings),10),coalesce((SELECT urgent_percent FROM quota_warning_settings),3)`).Scan(&s.EarlyPercent, &s.UrgentPercent)
	return s, err
}
func (m *Module) warningSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out QuotaWarningSettings
	if r.Method == http.MethodPut {
		if err := decodeJSON(w, r, &out); err != nil {
			writeErr(w, err)
			return
		}
		if !out.valid() {
			writeErr(w, fail(400, "thresholds must be integers from 1 to 50, with urgent lower than early"))
			return
		}
	}
	err := m.inReadinessWrite(ctx, p, func(tx pgx.Tx) error {
		if authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}) != nil || r.Method == http.MethodPut && p.Kind != tenant.Person {
			return fail(403, "workspace settings management required")
		}
		if r.Method == http.MethodGet {
			var err error
			out, err = quotaWarningSettings(ctx, tx)
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO quota_warning_settings(tenant_id,early_percent,urgent_percent) VALUES($1,$2,$3) ON CONFLICT(tenant_id) DO UPDATE SET early_percent=EXCLUDED.early_percent,urgent_percent=EXCLUDED.urgent_percent`, p.TenantID, out.EarlyPercent, out.UrgentPercent); err != nil {
			return err
		}
		return writeEvent(ctx, tx, p, "account.quota_warning_settings_changed", nil, out)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// No missing balance is converted to a percentage. A measured key cap needs
// UsedPercent too; Remaining alone supplies no denominator. Freshness follows A.
func quotaWarningLevel(f ReadinessFact, s QuotaWarningSettings, now time.Time) (string, float64, bool) {
	if !s.valid() || f.WindowKey == "check" || f.ReadingError != "" || f.Source != "agentd" && f.Source != "harness" && f.Source != "provider" || f.ReadingAt == nil || now.Before(*f.ReadingAt) || now.Sub(*f.ReadingAt) > 10*time.Minute || f.ResetsAt != nil && !now.Before(*f.ResetsAt) || f.UsedPercent == nil || math.IsNaN(*f.UsedPercent) || *f.UsedPercent < 0 || *f.UsedPercent > 100 {
		return "", 0, false
	}
	remaining := 100 - *f.UsedPercent
	if remaining <= float64(s.UrgentPercent) {
		return "urgent", remaining, true
	}
	if remaining <= float64(s.EarlyPercent) {
		return "early", remaining, true
	}
	return "", remaining, true
}

type quotaNotice struct{ warning, project, recipient, session, body string }

// Preparation performs every existing-row lock/write before the caller's final
// event flush. The tenant and pairing fences serialize reports across machines.
func prepareQuotaWarnings(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Account, now time.Time) ([]quotaNotice, error) {
	settings, err := quotaWarningSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	facts, err := loadReadinessFacts(ctx, tx, a, now)
	if err != nil {
		return nil, err
	}
	legacy, err := legacyReadinessFacts(ctx, tx, a, now)
	if err != nil {
		return nil, err
	}
	// Local resources for a person-confirmed login pool use a stable quota
	// key. Adding or removing a computer cannot replace its warning identity.
	// Explicit shared resources keep A's resource identity; unconfirmed
	// fingerprints never establish sharing.
	resources, err := ReadinessResources(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	quotaKeys := map[string]string{}
	for _, r := range resources {
		if r.Kind == "endpoint_concurrency" {
			continue
		}
		quotaKeys[r.ID] = r.ID
		var identityKind string
		if err := tx.QueryRow(ctx, `SELECT identity_kind FROM account_readiness_resources WHERE id=$1`, r.ID).Scan(&identityKind); err != nil {
			return nil, err
		}
		if a.QuotaPoolFingerprint != "" && identityKind == "account" && (r.Kind == "subscription_quota" || r.Kind == "key_cap") {
			quotaKeys[r.ID] = "pool:" + a.Harness + ":" + a.QuotaPoolFingerprint
		}
	}
	// The winning timestamp for each resource/window is the sole authority.
	latest := map[string]ReadinessFact{}
	for _, f := range append(facts, legacy...) {
		if _, applies := quotaKeys[f.ResourceID]; !applies {
			continue
		}
		key := quotaKeys[f.ResourceID] + "/" + f.WindowKey
		old, ok := latest[key]
		if !ok || f.ReadingAt != nil && (old.ReadingAt == nil || f.ReadingAt.After(*old.ReadingAt)) {
			latest[key] = f
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	notices := []quotaNotice{}
	for _, key := range keys {
		f := latest[key]
		quotaKey := quotaKeys[f.ResourceID]
		level, remaining, fresh := quotaWarningLevel(f, settings, now)
		if !fresh {
			continue
		}
		reset := "none"
		if f.ResetsAt != nil {
			reset = f.ResetsAt.UTC().Format(time.RFC3339Nano)
		}
		// A newer observation of recovery or a new measured reset clears the
		// old episode. Clock passage, missing data and delayed samples cannot.
		if _, err := tx.Exec(ctx, `UPDATE account_quota_warnings SET recovered_at=$4 WHERE quota_key=$1 AND window_key=$2 AND recovered_at IS NULL AND reading_at<$4 AND ($5 OR reset_key<>$3)`, quotaKey, f.WindowKey, reset, *f.ReadingAt, level == ""); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_quota_warnings SET reading_at=$4,remaining_percent=$5 WHERE quota_key=$1 AND window_key=$2 AND reset_key=$3 AND recovered_at IS NULL AND reading_at<$4`, quotaKey, f.WindowKey, reset, *f.ReadingAt, remaining); err != nil {
			return nil, err
		}
		if level == "" {
			continue
		}
		threshold := settings.EarlyPercent
		if level == "urgent" {
			threshold = settings.UrgentPercent
		}
		if level == "urgent" {
			if _, err := tx.Exec(ctx, `INSERT INTO account_quota_warnings(tenant_id,resource_id,window_key,reset_key,threshold_percent,severity,suppressed,reading_at,remaining_percent,resets_at,quota_key) VALUES($1,$2,$3,$4,$5,'early',true,$6,$7,$8,$9) ON CONFLICT(tenant_id,quota_key,window_key,reset_key,threshold_percent) DO UPDATE SET recovered_at=coalesce(account_quota_warnings.recovered_at,EXCLUDED.reading_at) WHERE account_quota_warnings.reading_at<=EXCLUDED.reading_at`, p.TenantID, f.ResourceID, f.WindowKey, reset, settings.EarlyPercent, *f.ReadingAt, remaining, f.ResetsAt, quotaKey); err != nil {
				return nil, err
			}
		}
		var warning string
		err = tx.QueryRow(ctx, `INSERT INTO account_quota_warnings(tenant_id,resource_id,window_key,reset_key,threshold_percent,severity,reading_at,remaining_percent,resets_at,quota_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,quota_key,window_key,reset_key,threshold_percent) DO UPDATE SET reading_at=EXCLUDED.reading_at,remaining_percent=EXCLUDED.remaining_percent,recovered_at=NULL WHERE account_quota_warnings.reading_at<=EXCLUDED.reading_at AND NOT account_quota_warnings.suppressed RETURNING id::text`, p.TenantID, f.ResourceID, f.WindowKey, reset, threshold, level, *f.ReadingAt, remaining, f.ResetsAt, quotaKey).Scan(&warning)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pending, err := quotaNoticeRecipients(ctx, tx, warning, f.ResourceID, quotaKey)
		if err != nil {
			return nil, err
		}
		notices = append(notices, pending...)
	}
	err = withQuotaNoticeVisibility(ctx, tx, func() error {
		// Pre-lock FK parents in sorted batches before any event counter.
		for _, target := range []struct{ column, table string }{{"project", "nodes"}, {"recipient", "principals"}, {"session", "harness_sessions"}} {
			ids := []string{}
			seen := map[string]bool{}
			for _, n := range notices {
				id := n.project
				if target.column == "recipient" {
					id = n.recipient
				} else if target.column == "session" {
					id = n.session
				}
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				if _, err := tx.Exec(ctx, `SELECT id FROM `+target.table+` WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, ids); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return notices, err
}

// Only the internal notice step sees all projects. The caller has already
// passed account.probe under the tenant fence. No project data enters its
// response, and its original visibility is restored before further work.
func withQuotaNoticeVisibility(ctx context.Context, tx pgx.Tx, fn func() error) error {
	var visible string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&visible); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return err
	}
	if err := fn(); err != nil {
		// The caller rolls the entire transaction back on any error.
		return err
	}
	_, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, visible)
	return err
}

func quotaNoticeRecipients(ctx context.Context, tx pgx.Tx, warning, resource, quotaKey string) ([]quotaNotice, error) {
	pending := []quotaNotice{}
	err := withQuotaNoticeVisibility(ctx, tx, func() error {
		rows, err := tx.Query(ctx, `SELECT lead.project_id::text,lead.agent_principal_id::text,lead.id::text,left(coalesce(nullif(child.display_label,''),child.id::text),160)
          FROM harness_sessions child JOIN agent_runs run ON run.tenant_id=child.tenant_id AND run.id=child.run_id JOIN agent_accounts a ON a.tenant_id=run.tenant_id AND a.id=run.account_id
          JOIN harness_sessions lead ON lead.tenant_id=child.tenant_id AND lead.project_id=child.project_id AND lead.id=child.parent_id AND lead.role='coordinator'
          WHERE child.stopped_at IS NULL AND child.archived_at IS NULL AND lead.stopped_at IS NULL AND lead.archived_at IS NULL AND (`+warningMembershipSQL+`)
		  AND CASE aeon_principal_visibility(lead.tenant_id,lead.agent_principal_id) WHEN '*' THEN true WHEN '' THEN false ELSE lead.project_id=ANY(aeon_principal_visibility(lead.tenant_id,lead.agent_principal_id)::uuid[]) END
          ORDER BY lead.project_id,lead.id,child.id LIMIT 201`, resource, quotaKey)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var n quotaNotice
			var name string
			if err := rows.Scan(&n.project, &n.recipient, &n.session, &name); err != nil {
				rows.Close()
				return err
			}
			count++
			n.warning = warning
			if len(pending) > 0 && pending[len(pending)-1].session == n.session {
				pending[len(pending)-1].body += ", " + name
			} else {
				n.body = name
				pending = append(pending, n)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count > 200 || len(pending) > 32 {
			return fail(503, "too many quota-warning recipients or sessions")
		}
		for i := range pending {
			pending[i].body = "Account availability is limited for sessions in this project: " + pending[i].body + ". See /agents for current availability and any quota details shared by the owner."
		}
		return nil
	})
	return pending, err
}

// a is the current target account; stale resource bindings never participate.
const warningMembershipSQL = `($2=$1::uuid::text AND EXISTS(SELECT 1 FROM account_readiness_memberships member JOIN agent_accounts origin ON origin.tenant_id=member.tenant_id AND origin.id=member.account_id AND origin.link_revision=member.binding_revision WHERE member.resource_id=$1 AND (member.account_id=a.id OR (a.quota_pool_fingerprint<>'' AND a.quota_pool_fingerprint=origin.quota_pool_fingerprint AND a.harness=origin.harness)))) OR (a.quota_pool_fingerprint<>'' AND $2='pool:'||a.harness||':'||a.quota_pool_fingerprint)`

func flushQuotaNotices(ctx context.Context, tx pgx.Tx, p tenant.Principal, notices []quotaNotice) error {
	if len(notices) == 0 {
		return nil
	}
	return withQuotaNoticeVisibility(ctx, tx, func() error {
		// Create/lock the System actor during preparation, before event counters.
		// Notices deliberately carry no exact quota or threshold, so later sharing
		// revocation cannot turn an immutable inbox body into a privacy leak.
		for _, n := range notices {
			digest := sha256.Sum256([]byte(n.warning + "/" + n.project + "/" + n.session))
			key := "quota-warning/" + hex.EncodeToString(digest[:])
			var present bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE sender_principal_id=$1 AND idempotency_key=$2)`, p.ID, key).Scan(&present); err != nil {
				return err
			}
			if present {
				continue
			}
			var id string
			if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
				return err
			}
			ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &n.project, Type: "inbox.sent", After: map[string]any{"id": id, "sender_principal_id": p.ID, "recipient_principal_id": n.recipient}})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id,sender_label) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'System')`, p.TenantID, id, p.ID, n.recipient, ev.ID, n.body, key, n.session); err != nil {
				return err
			}
		}
		return nil
	})
}

func quotaSystemActor(ctx context.Context, tx pgx.Tx, tenantID string, needed bool) (tenant.Principal, error) {
	p := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent, Name: "System"}
	if !needed {
		return p, nil
	}
	err := tx.QueryRow(ctx, `SELECT aeon_authz_system_actor($1::uuid)::text`, tenantID).Scan(&p.ID)
	return p, err
}

type QuotaWarningSession struct {
	SessionID        string     `json:"session_id"`
	ProjectID        string     `json:"project_id"`
	AccountID        string     `json:"account_id"`
	Availability     string     `json:"availability"`
	DetailsRedacted  bool       `json:"details_redacted"`
	Severity         string     `json:"severity"`
	RemainingPercent float64    `json:"remaining_percent"`
	ThresholdPercent int        `json:"threshold_percent"`
	WindowKey        string     `json:"window_key"`
	ResetsAt         *time.Time `json:"resets_at"`
}

func (m *Module) warningSessions(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, fail(400, "invalid warning cursor"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := struct {
		Items     []QuotaWarningSession `json:"items"`
		NextAfter *string               `json:"next_after"`
	}{Items: []QuotaWarningSession{}}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(ctx, tx, p, "account.read", authz.Scope{}) != nil {
			return fail(403, "account read permission required")
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		// Select the most urgent applicable binding window per session. RLS
		// limits both sessions and runs to projects visible to the caller.
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON(s.id) s.id::text,s.project_id::text,a.id::text,CASE WHEN q.remaining_percent<=coalesce((SELECT urgent_percent FROM quota_warning_settings),3) THEN 'urgent' ELSE 'early' END,q.remaining_percent,CASE WHEN q.remaining_percent<=coalesce((SELECT urgent_percent FROM quota_warning_settings),3) THEN coalesce((SELECT urgent_percent FROM quota_warning_settings),3) ELSE coalesce((SELECT early_percent FROM quota_warning_settings),10) END,q.window_key,q.resets_at
          FROM harness_sessions s JOIN agent_runs run ON run.tenant_id=s.tenant_id AND run.id=s.run_id JOIN agent_accounts a ON a.tenant_id=run.tenant_id AND a.id=run.account_id
          JOIN account_quota_warnings q ON q.tenant_id=a.tenant_id AND ((q.quota_key=q.resource_id::text AND EXISTS(SELECT 1 FROM account_readiness_memberships member JOIN agent_accounts origin ON origin.tenant_id=member.tenant_id AND origin.id=member.account_id AND origin.link_revision=member.binding_revision WHERE member.resource_id=q.resource_id AND (member.account_id=a.id OR (a.quota_pool_fingerprint<>'' AND a.quota_pool_fingerprint=origin.quota_pool_fingerprint AND a.harness=origin.harness)))) OR (a.quota_pool_fingerprint<>'' AND q.quota_key='pool:'||a.harness||':'||a.quota_pool_fingerprint))
          WHERE s.stopped_at IS NULL AND s.archived_at IS NULL AND q.recovered_at IS NULL AND NOT q.suppressed AND q.remaining_percent<=coalesce((SELECT early_percent FROM quota_warning_settings),10) AND q.reading_at BETWEEN $2::timestamptz-interval '10 minutes' AND $2::timestamptz AND (q.resets_at IS NULL OR q.resets_at>$2::timestamptz) AND ($1::uuid IS NULL OR s.id>$1) AND ($3='person' OR a.registered_by_principal_id=$4)
          ORDER BY s.id,q.remaining_percent,q.id LIMIT $5`, nullableUUID(after), now, string(p.Kind), p.ID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v QuotaWarningSession
			v.Availability = "limited"
			if err := rows.Scan(&v.SessionID, &v.ProjectID, &v.AccountID, &v.Severity, &v.RemainingPercent, &v.ThresholdPercent, &v.WindowKey, &v.ResetsAt); err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			id := out.Items[len(out.Items)-1].SessionID
			out.NextAfter = &id
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
