// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"errors"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type TierHistory struct {
	ID          int64     `json:"id"`
	Action      string    `json:"action"`
	From        *string   `json:"from_tier"`
	To          string    `json:"to_tier"`
	ActorID     string    `json:"actor_id"`
	ActorName   string    `json:"actor_name"`
	AskedByName *string   `json:"asked_by_name"`
	At          time.Time `json:"at"`
}
type TierCostSegment struct {
	Tier       string  `json:"tier"`
	Multiplier float64 `json:"price_multiplier"`
}
type TierRunCost struct {
	RunID       string            `json:"run_id"`
	Cost        string            `json:"cost_usd"`
	DefaultCost string            `json:"default_cost_usd"`
	Segments    []TierCostSegment `json:"segments"`
	Provisional bool              `json:"provisional"`
}
type TierEstimate struct {
	Tier     string   `json:"tier"`
	N        int      `json:"n"`
	Basis    string   `json:"basis"`
	RunID    *string  `json:"run_id"`
	Cost     *string  `json:"cost_usd"`
	Duration *float64 `json:"duration_ms"`
}

// Caller holds the session fence. Append instead of reconstructing decisions
// from request state: cancelling a pending approval restores that state.
func appendTierHistory(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, action, to, request, control, undo string) error {
	_, err := tx.Exec(ctx, `INSERT INTO harness_tier_history(tenant_id,session_id,action,from_tier,to_tier,actor_id,request_id,control_id,undo_of_control_id)
 VALUES($1,$2,$3,$4,$5,$6,nullif($7,'')::uuid,nullif($8,'')::uuid,nullif($9,'')::uuid)`, p.TenantID, s.ID, action, s.ServiceTier, to, p.ID, request, control, undo)
	return err
}
func completeTierHistory(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, c Control) error {
	// Persist the person who requested the change, not the confirming daemon.
	action := "rejected"
	if c.Outcome != nil && *c.Outcome == "applied" {
		action = "changed"
	}
	_, err := tx.Exec(ctx, `INSERT INTO harness_tier_history(tenant_id,session_id,action,from_tier,to_tier,actor_id,request_id,control_id,undo_of_control_id)
 SELECT tenant_id,session_id,CASE WHEN $2='changed' AND undo_of_control_id IS NOT NULL THEN 'undone' ELSE $2 END,from_tier,to_tier,actor_id,request_id,control_id,undo_of_control_id
 FROM harness_tier_history WHERE tenant_id=$3 AND session_id=$4 AND control_id=$1 AND action IN ('switch_requested','approved','undo_requested') ORDER BY id DESC LIMIT 1`, c.ID, action, p.TenantID, s.ID)
	return err
}
func readTierHistory(ctx context.Context, tx pgx.Tx, s Session) ([]TierHistory, bool, error) {
	rows, err := tx.Query(ctx, `SELECT h.id,h.action,h.from_tier,h.to_tier,h.actor_id::text,p.name,a.name,h.created_at
 FROM harness_tier_history h JOIN principals p ON p.tenant_id=h.tenant_id AND p.id=h.actor_id
 LEFT JOIN harness_tier_requests q ON q.tenant_id=h.tenant_id AND q.id=h.request_id
 LEFT JOIN principals a ON a.tenant_id=q.tenant_id AND a.id=q.requested_by_principal_id
 WHERE h.session_id=$1 ORDER BY h.id DESC LIMIT 51`, s.ID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []TierHistory{}
	for rows.Next() {
		var h TierHistory
		if err = rows.Scan(&h.ID, &h.Action, &h.From, &h.To, &h.ActorID, &h.ActorName, &h.AskedByName, &h.At); err != nil {
			return nil, false, err
		}
		out = append(out, h)
	}
	truncated := len(out) > 50
	if truncated {
		out = out[:50]
	}
	return out, truncated, rows.Err()
}
func positiveFactor(v *float64) bool {
	return v != nil && *v > 0 && *v <= 1000 && !math.IsNaN(*v) && !math.IsInf(*v, 0)
}
func tierRunCost(u SessionModelUsage, price ModelPrice, runID string) *TierRunCost {
	if u.BillingMode != "api" || u.EstimatedCostUSD == nil || u.PriceVersion == nil || len(u.TierSegments) == 0 {
		return nil
	}
	base := estimateUsageCost(u, price)
	if base == nil {
		return nil
	}
	out := &TierRunCost{RunID: runID, Cost: *u.EstimatedCostUSD, DefaultCost: *base, Segments: []TierCostSegment{}, Provisional: u.Provisional}
	for _, seg := range u.TierSegments {
		if seg.Tier == nil || !positiveFactor(seg.PriceMultiplier) {
			return nil
		}
		out.Segments = append(out.Segments, TierCostSegment{Tier: *seg.Tier, Multiplier: *seg.PriceMultiplier})
	}
	return out
}

// Work done by tools and time waiting never scale. ActiveMS is deliberately
// not used: it includes tool execution, so it is not a model-time measurement.
func tierModelTime(u SessionModelUsage, duration float64, target *float64) *float64 {
	if u.ModelTimeMS == nil || !positiveFactor(target) || float64(*u.ModelTimeMS) > duration || len(u.TierSegments) == 0 {
		return nil
	}
	var measured int64
	scaled := 0.0
	for _, seg := range u.TierSegments {
		if seg.ModelTimeMS == nil || *seg.ModelTimeMS < 0 || !positiveFactor(seg.SpeedFactor) {
			return nil
		}
		measured += *seg.ModelTimeMS
		scaled += float64(*seg.ModelTimeMS) * *seg.SpeedFactor / *target
	}
	if measured != *u.ModelTimeMS {
		return nil
	}
	estimate := duration - float64(measured) + scaled
	return &estimate
}
func projectTierCost(base string, multiplier *float64) *string {
	if !positiveFactor(multiplier) {
		return nil
	}
	value, ok := new(big.Rat).SetString(base)
	if !ok {
		return nil
	}
	factor, ok := new(big.Rat).SetString(strconv.FormatFloat(*multiplier, 'f', -1, 64))
	if !ok {
		return nil
	}
	result := value.Mul(value, factor).FloatString(12)
	return &result
}
func (m *Module) tierEvidence(r *http.Request, tx pgx.Tx, s Session, out *TierState) error {
	var err error
	out.History, out.HistoryTruncated, err = readTierHistory(r.Context(), tx, s)
	if err != nil {
		return err
	}
	out.Estimates = []TierEstimate{}
	for _, tier := range []string{"default", "fast", "fastest"} {
		out.Estimates = append(out.Estimates, TierEstimate{Tier: tier, Basis: "No estimate yet · 0 runs. A completed run with final tokens and frozen prices is required."})
	}
	if s.RunID != nil {
		u, e := loadUsage(r.Context(), tx, s.ID, tierModel(s))
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil && u.PriceVersion != nil {
			price, e := loadUsagePrice(r.Context(), tx, u.Model, u.PriceVersion)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			if e == nil {
				var only bool
				if err = tx.QueryRow(r.Context(), `SELECT count(*)=1 FROM harness_session_usage WHERE session_id=$1`, s.ID).Scan(&only); err != nil {
					return err
				}
				if only {
					out.RunCost = tierRunCost(u, price, *s.RunID)
				}
			}
		}
	}
	// One most recent matching completed run, not an average or cross-project
	// sample. A mixed-model session cannot be a whole-run estimate.
	var sampleID, runID string
	var duration float64
	err = tx.QueryRow(r.Context(), `SELECT hs.id::text,ar.id::text,extract(epoch FROM (ar.ended_at-ar.started_at))*1000
 FROM harness_sessions hs JOIN agent_runs ar ON ar.tenant_id=hs.tenant_id AND ar.id=hs.run_id
 JOIN harness_session_usage u ON u.tenant_id=hs.tenant_id AND u.session_id=hs.id AND u.model=$4
 WHERE hs.project_id=$1 AND hs.agent_principal_id=$2 AND hs.harness=$3 AND hs.model=$4
 AND hs.reasoning_effort IS NOT DISTINCT FROM $5::text AND ar.status='completed' AND ar.started_at IS NOT NULL AND ar.ended_at>=ar.started_at
 AND NOT u.provisional AND u.billing_mode='api' AND u.price_version IS NOT NULL AND u.estimated_cost_usd IS NOT NULL
 AND (SELECT count(*) FROM harness_session_usage other WHERE other.session_id=hs.id)=1
 ORDER BY ar.ended_at DESC,hs.id DESC LIMIT 1`, s.ProjectID, s.AgentPrincipalID, s.Harness, tierModel(s), s.ReasoningEffort).Scan(&sampleID, &runID, &duration)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	u, err := loadUsage(r.Context(), tx, sampleID, tierModel(s))
	if err != nil {
		return err
	}
	price, err := loadUsagePrice(r.Context(), tx, u.Model, u.PriceVersion)
	if err != nil {
		return err
	}
	cost := tierRunCost(u, price, runID)
	if cost == nil {
		return nil
	}
	report := sessionTierReport(s, tierModel(s))
	for i := range out.Estimates {
		e := &out.Estimates[i]
		cap, offered := report.Find(e.Tier)
		if !offered {
			e.Basis = "Not offered: no published price · 0 runs."
			continue
		}
		e.Cost = projectTierCost(cost.DefaultCost, cap.PriceMultiplier)
		if e.Cost == nil {
			continue
		}
		e.N = 1
		e.RunID = &runID
		e.Duration = tierModelTime(u, duration, cap.SpeedFactor)
		e.Basis = "Last completed matching run · 1 run · same project, agent, harness, model and effort; frozen price version " + strconv.FormatInt(*u.PriceVersion, 10) + " and tier token segments. Price scales cost; speed scales measured model time only."
		if e.Duration == nil {
			e.Basis += " Time: no estimate yet; measured model time or speed is unavailable."
		}
	}
	return nil
}
