// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const maxUsageCount int64 = 1_000_000_000_000

var usageModelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// SessionModelUsage is the one current cumulative observation per session/model.
// Null counters and costs are unknown; EstimatedCostUSD is never billed cost.
// Aggregate consumers must not add overlapping managed-run telemetry to it.
type SessionModelUsage struct {
	SessionID         string    `json:"session_id"`
	Model             string    `json:"model"`
	Sequence          int64     `json:"sequence"`
	InputTokens       *int64    `json:"input_tokens"`
	OutputTokens      *int64    `json:"output_tokens"`
	CachedInputTokens *int64    `json:"cached_input_tokens"`
	ReasoningTokens   *int64    `json:"reasoning_tokens"`
	Provisional       bool      `json:"provisional"`
	PriceVersion      *int64    `json:"price_version"`
	EstimatedCostUSD  *string   `json:"estimated_cost_usd"`
	CostStatus        string    `json:"cost_status"`
	Currency          string    `json:"currency"`
	AccountID         *string   `json:"account_id"`
	AccountLabel      *string   `json:"account_label"`
	BillingMode       string    `json:"billing_mode"`
	SubscriptionLabel *string   `json:"subscription_label"`
	MetadataSource    string    `json:"metadata_source"`
	ReportedAt        time.Time `json:"reported_at"`
}

type usageReport struct {
	ReportID          string  `json:"report_id"`
	Model             string  `json:"model"`
	Sequence          int64   `json:"sequence"`
	InputTokens       *int64  `json:"input_tokens"`
	OutputTokens      *int64  `json:"output_tokens"`
	CachedInputTokens *int64  `json:"cached_input_tokens"`
	ReasoningTokens   *int64  `json:"reasoning_tokens"`
	Provisional       *bool   `json:"provisional"`
	AccountID         *string `json:"account_id"`
	AccountLabel      *string `json:"account_label"`
	BillingMode       string  `json:"billing_mode"`
	SubscriptionLabel *string `json:"subscription_label"`
}

type usageReportResult struct {
	Usage    SessionModelUsage `json:"usage"`
	Replayed bool              `json:"replayed"`
}

func validUsageModel(model string) bool {
	return len(model) <= 128 && usageModelRE.MatchString(model)
}

func usageLabel(label *string, max int) bool {
	if label == nil {
		return true
	}
	return utf8.ValidString(*label) && *label != "" && strings.TrimSpace(*label) == *label &&
		utf8.RuneCountInString(*label) <= max && strings.IndexFunc(*label, unicode.IsControl) < 0
}

func (in *usageReport) validate() error {
	if !workorders.UUID(in.ReportID) || !validUsageModel(in.Model) || in.Sequence < 1 || in.Sequence > maxUsageCount || in.Provisional == nil {
		return workorders.Fail(400, "invalid report identity, model, sequence or provisional flag")
	}
	in.ReportID = strings.ToLower(in.ReportID)
	for _, count := range []*int64{in.InputTokens, in.OutputTokens, in.CachedInputTokens, in.ReasoningTokens} {
		if count != nil && (*count < 0 || *count > maxUsageCount) {
			return workorders.Fail(400, "token count must be 0..1000000000000 or null")
		}
	}
	if in.InputTokens != nil && in.CachedInputTokens != nil && *in.CachedInputTokens > *in.InputTokens {
		return workorders.Fail(400, "cached input exceeds inclusive input tokens")
	}
	if in.OutputTokens != nil && in.ReasoningTokens != nil && *in.ReasoningTokens > *in.OutputTokens {
		return workorders.Fail(400, "reasoning tokens exceed output")
	}
	if in.AccountID != nil {
		if !workorders.UUID(*in.AccountID) {
			return workorders.Fail(400, "invalid account id")
		}
		normalized := strings.ToLower(*in.AccountID)
		in.AccountID = &normalized
	}
	if !usageLabel(in.AccountLabel, 128) || !usageLabel(in.SubscriptionLabel, 120) {
		return workorders.Fail(400, "invalid public account or subscription label")
	}
	if in.BillingMode != "unknown" && in.BillingMode != "api" && in.BillingMode != "subscription" {
		return workorders.Fail(400, "invalid billing mode")
	}
	if in.SubscriptionLabel != nil && in.BillingMode != "subscription" {
		return workorders.Fail(400, "subscription label requires subscription billing mode")
	}
	return nil
}

func (in usageReport) snapshot(sessionID string) SessionModelUsage {
	return SessionModelUsage{SessionID: sessionID, Model: in.Model, Sequence: in.Sequence,
		InputTokens: in.InputTokens, OutputTokens: in.OutputTokens, CachedInputTokens: in.CachedInputTokens,
		ReasoningTokens: in.ReasoningTokens,
		Provisional:     *in.Provisional || in.InputTokens == nil || in.OutputTokens == nil || in.CachedInputTokens == nil,
		AccountID:       in.AccountID, AccountLabel: in.AccountLabel, BillingMode: in.BillingMode,
		SubscriptionLabel: in.SubscriptionLabel, Currency: "USD", CostStatus: "unknown", MetadataSource: "reported"}
}

func sameUsageValue[T comparable](a, b *T) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func usageTransition(old, next SessionModelUsage) error {
	if next.Sequence <= old.Sequence {
		return workorders.Fail(409, "usage sequence must increase")
	}
	for _, counts := range [][2]*int64{{old.InputTokens, next.InputTokens}, {old.OutputTokens, next.OutputTokens}, {old.CachedInputTokens, next.CachedInputTokens}, {old.ReasoningTokens, next.ReasoningTokens}} {
		if counts[0] != nil && (counts[1] == nil || *counts[1] < *counts[0]) {
			return workorders.Fail(409, "known cumulative tokens cannot decrease or become unknown")
		}
	}
	if old.InputTokens != nil && old.CachedInputTokens != nil && next.InputTokens != nil && next.CachedInputTokens != nil &&
		*next.InputTokens-*next.CachedInputTokens < *old.InputTokens-*old.CachedInputTokens {
		return workorders.Fail(409, "cumulative uncached input cannot decrease")
	}
	if !old.Provisional && (next.Provisional ||
		!sameUsageValue(old.InputTokens, next.InputTokens) || !sameUsageValue(old.OutputTokens, next.OutputTokens) ||
		!sameUsageValue(old.CachedInputTokens, next.CachedInputTokens) || !sameUsageValue(old.ReasoningTokens, next.ReasoningTokens) ||
		!sameUsageValue(old.AccountID, next.AccountID) ||
		!sameUsageValue(old.AccountLabel, next.AccountLabel) || old.BillingMode != next.BillingMode ||
		!sameUsageValue(old.SubscriptionLabel, next.SubscriptionLabel)) {
		return workorders.Fail(409, "final usage counters and metadata are immutable")
	}
	return nil
}

const usageColumns = `session_id::text,model,sequence,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,provisional,price_version,estimated_cost_usd::text,account_id::text,account_label,billing_mode,subscription_label,reported_at`

func scanUsage(row pgx.Row) (SessionModelUsage, error) {
	out := SessionModelUsage{Currency: "USD", CostStatus: "unknown", MetadataSource: "reported"}
	err := row.Scan(&out.SessionID, &out.Model, &out.Sequence, &out.InputTokens, &out.OutputTokens, &out.CachedInputTokens, &out.ReasoningTokens,
		&out.Provisional, &out.PriceVersion, &out.EstimatedCostUSD, &out.AccountID, &out.AccountLabel, &out.BillingMode, &out.SubscriptionLabel, &out.ReportedAt)
	// Only api billing is priced. A stored estimate on a subscription or
	// unknown row is historical: it is never returned, and its price version
	// never pins a later api price.
	if out.BillingMode != "api" {
		out.PriceVersion, out.EstimatedCostUSD = nil, nil
	}
	if out.EstimatedCostUSD != nil {
		out.CostStatus = "estimated"
	}
	return out, err
}

func loadUsage(ctx context.Context, tx pgx.Tx, sessionID, model string) (SessionModelUsage, error) {
	return scanUsage(tx.QueryRow(ctx, `SELECT `+usageColumns+` FROM harness_session_usage WHERE session_id=$1 AND model=$2`, sessionID, model))
}

func (m *Module) reportUsage(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	if !workorders.UUID(r.PathValue("projectId")) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	// Serializes first reports, receipt lookup and current snapshots across all
	// reporters of this generation. A stopped generation can still settle usage.
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(403, "harness worker proof rejected")
	}
	if err != nil {
		return nil, err
	}
	lease := r.Header.Get("X-Aeon-Worker-Lease")
	if p.Kind != tenant.Agent || p.ID != s.AgentPrincipalID || len(lease) < 32 || len(lease) > 256 ||
		subtle.ConstantTimeCompare(digest("lease", lease), s.leaseDigest) != 1 {
		return nil, workorders.Fail(403, "harness worker proof rejected")
	}
	if s.ArchivedAt != nil {
		return nil, workorders.Fail(410, "harness generation archived")
	}
	var in usageReport
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	var prior []byte
	err = tx.QueryRow(ctx, `SELECT payload_digest FROM harness_usage_receipts WHERE session_id=$1 AND report_id=$2`, s.ID, in.ReportID).Scan(&prior)
	if err == nil {
		if subtle.ConstantTimeCompare(prior, sum[:]) != 1 {
			return nil, workorders.Fail(409, "report id already used with different payload")
		}
		out, err := loadUsage(ctx, tx, s.ID, in.Model)
		return usageReportResult{Usage: out, Replayed: true}, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	next := in.snapshot(s.ID)
	old, err := loadUsage(ctx, tx, s.ID, in.Model)
	var before any
	if err == nil {
		if err := usageTransition(old, next); err != nil {
			return nil, err
		}
		next.PriceVersion = old.PriceVersion
		before = old
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if next.AccountID != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE id=$1 AND harness=$2)`, next.AccountID, s.Harness).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, workorders.Fail(400, "account must belong to tenant and session harness")
		}
	}
	if next.BillingMode == "api" {
		price, err := loadUsagePrice(ctx, tx, next.Model, next.PriceVersion)
		if err == nil {
			next.PriceVersion = &price.Version
			next.EstimatedCostUSD = estimateUsageCost(next, price)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	} else {
		next.PriceVersion = nil
		next.EstimatedCostUSD = nil
	}
	out, err := scanUsage(tx.QueryRow(ctx, `INSERT INTO harness_session_usage
        (tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,provisional,price_version,estimated_cost_usd,account_id,account_label,billing_mode,subscription_label)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13,$14,$15)
        ON CONFLICT (tenant_id,session_id,model) DO UPDATE SET sequence=EXCLUDED.sequence,input_tokens=EXCLUDED.input_tokens,
        output_tokens=EXCLUDED.output_tokens,cached_input_tokens=EXCLUDED.cached_input_tokens,reasoning_tokens=EXCLUDED.reasoning_tokens,provisional=EXCLUDED.provisional,
        price_version=EXCLUDED.price_version,estimated_cost_usd=EXCLUDED.estimated_cost_usd,account_id=EXCLUDED.account_id,
        account_label=EXCLUDED.account_label,billing_mode=EXCLUDED.billing_mode,subscription_label=EXCLUDED.subscription_label,reported_at=clock_timestamp()
        RETURNING `+usageColumns, p.TenantID, s.ID, next.Model, next.Sequence, next.InputTokens, next.OutputTokens, next.CachedInputTokens, next.ReasoningTokens,
		next.Provisional, next.PriceVersion, next.EstimatedCostUSD, next.AccountID, next.AccountLabel, next.BillingMode, next.SubscriptionLabel))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO harness_usage_receipts(tenant_id,session_id,report_id,model,sequence,payload_digest) VALUES($1,$2,$3,$4,$5,$6)`,
		p.TenantID, s.ID, in.ReportID, in.Model, in.Sequence, sum[:]); err != nil {
		return nil, err
	}
	if s.Management == "unmanaged" && out.AccountID != nil && old.AccountID != nil && *out.AccountID == *old.AccountID && out.InputTokens != nil && out.OutputTokens != nil && old.InputTokens != nil && old.OutputTokens != nil {
		delta := *out.InputTokens + *out.OutputTokens - *old.InputTokens - *old.OutputTokens
		if err := agentaccounts.ObserveSessionTokens(ctx, tx, p.ID, *out.AccountID, out.Model, delta, old.ReportedAt, out.ReportedAt); err != nil {
			return nil, err
		}
	}
	return usageReportResult{Usage: out}, record(ctx, tx, p, s, "usage_reported", before, out)
}

func (m *Module) sessionUsage(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if !workorders.UUID(r.PathValue("projectId")) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+usageColumns+` FROM harness_session_usage WHERE session_id=$1 ORDER BY model`, s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []SessionModelUsage{}
	for rows.Next() {
		item, err := scanUsage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return struct {
		SessionID string              `json:"session_id"`
		Reported  bool                `json:"reported"`
		Items     []SessionModelUsage `json:"items"`
	}{s.ID, len(items) > 0, items}, rows.Err()
}
