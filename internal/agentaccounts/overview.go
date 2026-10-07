// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type overviewPage struct {
	Accounts   []overviewAccount `json:"accounts"`
	AsOf       time.Time         `json:"as_of"`
	HasMore    bool              `json:"has_more"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// Deliberately separate from Account and capacity.Reading: neither local keys,
// vendor identity, arbitrary vendor metadata nor config-home bindings belong here.
type overviewAccount struct {
	AccountID       string             `json:"account_id"`
	Provider        string             `json:"provider"`
	Harness         string             `json:"harness"`
	Label           string             `json:"label"`
	HostLabel       string             `json:"host_label"`
	DaemonID        string             `json:"daemon_id"`
	OwnerPersonID   *string            `json:"owner_person_id,omitempty"`
	OwnerPersonName string             `json:"owner_person_name,omitempty"`
	State           string             `json:"state"`
	BillingMode     string             `json:"billing_mode"`
	Windows         []overviewWindow   `json:"windows"`
	Schedule        *capacity.Schedule `json:"schedule,omitempty"`
	Limit           *LimitRule         `json:"limit,omitempty"`
	QuotaPool       string             `json:"quota_pool_fingerprint,omitempty"`
	Routing         *CapacityRouting   `json:"routing,omitempty"`
	Routable        bool               `json:"routable"`
	Wait            *CapacityWait      `json:"wait,omitempty"`
	DetailsRedacted bool               `json:"details_redacted"`
}

type overviewWindow struct {
	Kind        string            `json:"window_kind"`
	Bucket      string            `json:"bucket"`
	StartsAt    time.Time         `json:"starts_at"`
	ResetsAt    time.Time         `json:"resets_at"`
	ReadAt      *time.Time        `json:"read_at,omitempty"`
	Unit        string            `json:"unit"`
	Allowance   int64             `json:"allowance"`
	Used        float64           `json:"used"`
	Reserved    int64             `json:"reserved"`
	UsedPercent *float64          `json:"used_percent,omitempty"`
	Freshness   string            `json:"freshness"`
	Source      string            `json:"source"`
	Pacing      *capacity.Pacing  `json:"pacing,omitempty"`
	Learned     *overviewLearning `json:"learned,omitempty"`
}

type overviewLearning struct {
	LimitTokens *float64 `json:"limit_tokens,omitempty"`
	Burn        float64  `json:"burn_percent_per_hour"`
	Samples     int      `json:"samples"`
}

func (m *Module) overview(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeErr(w, fail(400, "limit must be between 1 and 100"))
			return
		}
	}
	after := strings.ToLower(r.URL.Query().Get("after"))
	if after != "" && !uuidRE.MatchString(after) {
		writeErr(w, fail(400, "after must be an account UUID"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out overviewPage
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		permission := "account.read"
		reader := p
		if p.Kind == tenant.Agent {
			permission = "account.overview.read"
			scopes, err := keyScopes(ctx, tx, r, p)
			if err != nil {
				return err
			}
			reader.Scopes = scopes
		}
		if err := authz.RequireTx(ctx, tx, reader, permission, authz.Scope{}); err != nil {
			return fail(403, "permission required: "+permission)
		}
		// Global advice must see every door in a shared pool, even across pages.
		// Bound the tenant projection BEFORE listAccounts attaches its windows.
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM agent_accounts WHERE NOT `+retiredSQL+` LIMIT 1025) a`).Scan(&count); err != nil {
			return err
		}
		if count > 1024 {
			return fail(503, "too many accounts for a complete routing overview")
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows w JOIN agent_accounts ON agent_accounts.id=w.account_id AND agent_accounts.tenant_id=w.tenant_id WHERE NOT `+retiredSQL+` AND NOT w.pairing_verification AND NOT w.capacity_retired AND w.removed_at IS NULL LIMIT 4097) w`).Scan(&count); err != nil {
			return err
		}
		if count > 4096 {
			return fail(503, "too many windows for a complete routing overview")
		}
		accounts, err := listAccounts(ctx, tx)
		if err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		advice, err := routingAdvice(ctx, tx, accounts, "", runRow{Purpose: "managed"}, now)
		if err != nil {
			return err
		}
		slices.SortFunc(accounts, func(a, b Account) int { return strings.Compare(a.ID, b.ID) })
		page := []Account{}
		for _, a := range accounts {
			if a.ID > after {
				page = append(page, a)
				if len(page) > limit {
					break
				}
			}
		}
		out = overviewPage{Accounts: []overviewAccount{}, AsOf: now, HasMore: len(page) > limit}
		if out.HasMore {
			page = page[:limit]
			out.NextCursor = page[len(page)-1].ID
		}
		ids := make([]string, 0, len(page))
		for _, a := range page {
			ids = append(ids, a.ID)
		}
		policy, err := accountprivacy.Load(ctx, tx, p, ids)
		if err != nil {
			return err
		}
		for _, a := range page {
			route := advice[a.ID]
			// The generic privacy boundary strips timing values on a later
			// sharing change. Do not carry owner calendar metadata in advice.
			if route.Wait != nil {
				wait := *route.Wait
				wait.Timezone = ""
				route.Wait = &wait
			}
			item := overviewAccount{AccountID: a.ID, Provider: overviewProvider(a), Harness: a.Harness, Label: a.Label, HostLabel: a.HostLabel, DaemonID: a.DaemonID, OwnerPersonID: a.OwnerPersonID, OwnerPersonName: a.OwnerPersonName, State: a.State, BillingMode: a.BillingMode, Windows: []overviewWindow{}, Routable: route.AvailableSlots > 0, Wait: route.Wait, DetailsRedacted: !policy[a.ID]}
			if !policy[a.ID] {
				// Available slots and a missing or capacity wait are quota
				// headroom. A private row has one constant shape.
				item.Routable = false
				item.Wait = waitFor("state")
			} else {
				if !item.Routable && item.Wait == nil {
					item.Wait = waitFor("capacity")
				}
				item.QuotaPool, item.Routing = a.QuotaPoolFingerprint, &route
				schedule, err := routingSchedule(ctx, tx, a)
				if err != nil {
					return err
				}
				item.Schedule = &schedule
				item.Limit, err = currentLimit(ctx, tx, a.ID)
				if err != nil {
					return err
				}
				item.Windows, err = overviewWindows(ctx, tx, a, now, schedule)
				if err != nil {
					return err
				}
			}
			out.Accounts = append(out.Accounts, item)
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			err = fail(503, "account overview took too long; try again")
		}
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func overviewProvider(a Account) string {
	if a.Provider != "" {
		return a.Provider
	}
	switch a.Harness {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	case "grok":
		return "xai"
	case "gemini":
		return "google"
	}
	return ""
}

func overviewWindows(ctx context.Context, tx pgx.Tx, a Account, now time.Time, schedule capacity.Schedule) ([]overviewWindow, error) {
	readings, err := readCapacity(ctx, tx, a.ID, true)
	if err != nil {
		return nil, err
	}
	learned, err := loadLearning(ctx, tx, a.ID)
	if err != nil {
		return nil, err
	}
	facts, err := loadReadinessFacts(ctx, tx, a, now)
	if err != nil {
		return nil, err
	}
	windows := slices.Clone(a.Windows)
	out := []overviewWindow{}
	seenFacts := map[string]bool{}
	currentFacts := map[string]ReadinessFact{}
	for _, f := range facts {
		if f.UsedPercent != nil && f.ReadingAt != nil && f.ResetsAt != nil {
			currentFacts[f.ResourceID+":"+f.WindowKey] = f
		}
	}
	for _, w := range windows {
		if w.capacityKind == "fact" {
			// Use the normalized fact's precise percentage and freshness below,
			// rather than a matching ledger's rounded routing hold. Retain old
			// ledgers when no current observation describes their reset.
			if f, ok := currentFacts[w.capacityBucket]; ok && f.ResetsAt.Equal(w.EndsAt) {
				continue
			}
		}
		v := overviewWindow{Kind: w.capacityKind, Bucket: w.capacityBucket, StartsAt: w.StartsAt, ResetsAt: w.EndsAt, Unit: w.Unit, Allowance: w.Allowance, Used: float64(w.Used), Reserved: w.Reserved, ReadAt: w.capacityReadAt, Freshness: "unknown", Source: "manual"}
		if v.Kind == "" {
			v.Kind = "manual"
		}
		if !now.Before(w.EndsAt) {
			v.Freshness = "expired"
		}
		if synthetic(w) {
			v.Source = "synthetic"
		} else if w.capacityReadAt != nil {
			v.Source = "vendor_reported"
			if w.capacitySource == "estimate" {
				v.Source = "learned"
			}
			for _, reading := range readings {
				if reading.WindowKind != w.capacityKind || reading.Bucket != w.capacityBucket || !reading.ResetsAt.Equal(w.EndsAt) || !reading.ReadAt.Equal(*w.capacityReadAt) {
					continue
				}
				percent := reading.UsedPercent
				v.Used, v.UsedPercent, v.Freshness = percent, &percent, reading.Freshness(now)
				metric := learned.metric(reading, now, schedule, "")
				v.Learned = &overviewLearning{Burn: metric.BurnRate, Samples: metric.RunCount}
				if metric.PerMillion > 0 {
					limit := 100_000_000 / metric.PerMillion
					if !math.IsInf(limit, 0) && !math.IsNaN(limit) {
						v.Learned.LimitTokens = &limit
					}
				}
				if now.Before(reading.ResetsAt) {
					pace, _, err := readingPacing(ctx, tx, a.ID, reading, now, schedule)
					if err != nil {
						return nil, err
					}
					v.Pacing = &pace
				}
				break
			}
		}
		out = append(out, v)
	}
	for _, f := range facts {
		if f.UsedPercent == nil || f.ReadingAt == nil || f.ResetsAt == nil {
			continue
		}
		key := f.ResourceID + ":" + f.WindowKey
		if seenFacts[key] {
			continue
		}
		seenFacts[key] = true
		freshness := "stale"
		if !now.Before(*f.ResetsAt) {
			freshness = "expired"
		} else if !f.ReadingAt.After(now) && now.Sub(*f.ReadingAt) <= 10*time.Minute {
			freshness = "fresh"
		}
		var reserved int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved),0)::bigint FROM account_allowance_windows WHERE capacity_kind='fact' AND capacity_bucket=$1 AND starts_at<$2 AND ends_at>$3`, key, f.ResetsAt, f.ReadingAt).Scan(&reserved); err != nil {
			return nil, err
		}
		out = append(out, overviewWindow{Kind: "fact", Bucket: key, StartsAt: *f.ReadingAt, ResetsAt: *f.ResetsAt, ReadAt: f.ReadingAt, Unit: "percent", Allowance: 100, Used: *f.UsedPercent, UsedPercent: f.UsedPercent, Reserved: reserved, Freshness: freshness, Source: "vendor_reported"})
	}
	if len(out) > 4096 {
		return nil, fail(503, "too many windows for a complete account overview")
	}
	return out, nil
}
