// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Observation is the shared client/server model evidence contract.
type Observation = modelreport.Observation

type ReportResult struct {
	Recorded int `json:"recorded"`
	Added    int `json:"added"`
	Proposed int `json:"proposed"`
}

func validateObservations(in []Observation) error {
	if in == nil {
		return fail(400, "model observations must be an array")
	}
	if len(in) > 200 {
		return fail(400, "at most 200 model observations")
	}
	for _, o := range in {
		if !uuidRE.MatchString(o.ReportID) || !validHarness(o.Harness) || !modelreport.ValidTuple(o.Model, o.Effort) {
			return fail(400, "invalid model observation")
		}
		if o.Status != "advertised" && o.Status != "working" && o.Status != "invalid" {
			return fail(400, "invalid observation status")
		}
	}
	return nil
}

// ReportInSession accepts only reports for the authenticated worker's own
// harness after the caller verifies its existing harness.worker scope and lease.
func ReportInSession(ctx context.Context, tx pgx.Tx, p tenant.Principal, harness, sessionID string, in []Observation) error {
	if len(in) == 0 {
		return nil
	}
	for _, o := range in {
		if o.Harness != harness {
			return fail(400, "model report harness mismatch")
		}
	}
	_, err := recordReports(ctx, tx, p, in, "agent", sessionID)
	return err
}

const (
	maxPrincipalReceipts = 1000
	maxHourlyReceipts    = 200
)

// Health reports can affect routing, so the authenticated caller must have
// attempted this exact tuple. Session routes additionally pin the proved lease.
func authorizeObservation(ctx context.Context, tx pgx.Tx, p tenant.Principal, o Observation, sessionID string) error {
	var enrolled bool
	if p.Kind == tenant.Agent {
		// Sessions are self-registered metadata, not account enrollment.
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE registered_by_principal_id=$1 AND harness=$2 AND state='available')`, p.ID, o.Harness).Scan(&enrolled); err != nil {
			return err
		}
		if !enrolled {
			return fail(403, "model evidence requires an enrolled harness and attempted model")
		}
	}
	if o.Status == "advertised" {
		return nil
	}
	err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM harness_sessions s
 LEFT JOIN agent_runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
 LEFT JOIN model_profiles m ON m.tenant_id=r.tenant_id AND m.id=r.model_profile_id
 WHERE s.agent_principal_id=$1 AND s.harness=$2 AND s.stopped_at IS NULL AND s.archived_at IS NULL
 AND ($5='' OR s.id::text=$5)
 AND ((s.model=$3 AND s.reasoning_effort=$4)
 OR (m.harness=$2 AND m.model=$3 AND m.effort=$4)))`, p.ID, o.Harness, o.Model, o.Effort, sessionID).Scan(&enrolled)
	if err != nil {
		return err
	}
	if !enrolled {
		return fail(403, "model evidence requires an enrolled harness and attempted model")
	}
	return nil
}

func recordReports(ctx context.Context, tx pgx.Tx, p tenant.Principal, in []Observation, source, sessionID string) (out ReportResult, err error) {
	if err := validateObservations(in); err != nil {
		return out, err
	}
	pending, err := prepareCatalogDeferred(ctx, tx, p)
	if err != nil {
		return out, err
	}
	defer func() {
		if err == nil {
			err = flushCatalogChanges(ctx, tx, p, pending)
		}
	}()
	if err := catalogLock(ctx, tx); err != nil {
		return out, err
	}
	var enabled, autoAdd bool
	if err := tx.QueryRow(ctx, `SELECT agent_reports_enabled,auto_add_profiles FROM model_refresh_settings`).Scan(&enabled, &autoAdd); err != nil {
		return out, err
	}
	if source == "agent" && !enabled {
		return out, nil
	}
	// Bound retention and writes per principal while preserving recent replays.
	if _, err := tx.Exec(ctx, `DELETE FROM model_report_receipts WHERE principal_id=$1 AND created_at<now()-interval '7 days'`, p.ID); err != nil {
		return out, err
	}
	var total, hourly int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE created_at>now()-interval '1 hour') FROM model_report_receipts WHERE principal_id=$1`, p.ID).Scan(&total, &hourly); err != nil {
		return out, err
	}
	for _, o := range in {
		raw, _ := json.Marshal(o)
		var equal bool
		err := tx.QueryRow(ctx, `SELECT content=$3::jsonb FROM model_report_receipts WHERE principal_id=$1 AND report_id=$2`, p.ID, o.ReportID, string(raw)).Scan(&equal)
		if err != nil && err != pgx.ErrNoRows {
			return out, err
		}
		if err == nil {
			if !equal {
				return out, fail(409, "report id already used for different evidence")
			}
			continue
		}
		if err := authorizeObservation(ctx, tx, p, o, sessionID); err != nil {
			return out, err
		}
		if total >= maxPrincipalReceipts || hourly >= maxHourlyReceipts {
			return out, fail(429, "model report receipt limit reached")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_report_receipts(tenant_id,principal_id,report_id,content) VALUES($1,$2,$3,$4)`, p.TenantID, p.ID, o.ReportID, string(raw)); err != nil {
			return out, err
		}
		total++
		hourly++
		if err := observe(ctx, tx, p.TenantID, o, source); err != nil {
			return out, err
		}
		out.Recorded++
		if o.Status == "invalid" || KnownInvalid(o.Model) {
			continue
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE harness=$1 AND model=$2 AND effort=$3)`, o.Harness, o.Model, o.Effort).Scan(&exists); err != nil {
			return out, err
		}
		if exists {
			continue
		}
		pin, ok := observedPin(o)
		if !autoAdd || !ok {
			out.Proposed++
			continue
		}
		pin.Source = "harness"
		if _, err := insertObservedProfile(ctx, tx, p.TenantID, pin); err != nil {
			return out, err
		}
		out.Added++
	}
	if out.Recorded > 0 {
		if err := writeEvent(ctx, tx, p, "model.observations_reported", nil, map[string]any{"source": source, "result": out, "observations": in}); err != nil {
			return out, err
		}
	}
	return out, nil
}

func observe(ctx context.Context, tx pgx.Tx, tenantID string, o Observation, source string) error {
	_, err := tx.Exec(ctx, `INSERT INTO model_observations(tenant_id,harness,model,effort,last_working_at,last_failing_at,failures,source)
 VALUES($1,$2,$3,$4,CASE WHEN $5='working' THEN now() END,CASE WHEN $5='invalid' THEN now() END,CASE WHEN $5='invalid' THEN 1 ELSE 0 END,$6)
 ON CONFLICT(tenant_id,harness,model,effort) DO UPDATE SET
 last_seen_at=now(),source=EXCLUDED.source,
 last_working_at=CASE WHEN $5='working' THEN now() ELSE model_observations.last_working_at END,
 last_failing_at=CASE WHEN $5='invalid' AND (model_observations.failures=0 OR model_observations.last_failing_at<=now()-interval '5 minutes') THEN now() ELSE model_observations.last_failing_at END,
 failures=CASE WHEN $5='working' THEN 0 WHEN $5='invalid' AND (model_observations.failures=0 OR model_observations.last_failing_at<=now()-interval '5 minutes') THEN least(model_observations.failures+1,1000) ELSE model_observations.failures END,
 suppressed_until=CASE WHEN $5='working' THEN NULL WHEN $5='invalid' AND model_observations.failures>=1 AND model_observations.last_failing_at<=now()-interval '5 minutes' THEN now()+interval '24 hours' ELSE model_observations.suppressed_until END`, tenantID, o.Harness, o.Model, o.Effort, o.Status, source)
	return err
}

// Known pins retain declared tiers and efforts. Unknown identifiers are never
// added to ladders; conservative profiles are merely available for person policy.
func observedPin(o Observation) (profileWrite, bool) {
	for _, model := range seedModels {
		if model.Harness == o.Harness && model.ID == o.Model {
			for _, e := range model.Efforts {
				if e == o.Effort {
					return profileWrite{Slug: catalogSlug(model, e), Version: CatalogVersion, Harness: model.Harness, Family: model.Family, Model: model.ID, Effort: e, Tier: model.Tier}, true
				}
			}
			return profileWrite{}, false
		}
	}
	family := map[string]string{"codex": "openai", "claude": "anthropic", "grok": "xai", "cursor": "cursor"}[o.Harness]
	if o.Harness == "pi" {
		for _, f := range []string{"anthropic", "openai", "xai"} {
			if strings.HasPrefix(o.Model, f+"/") {
				family = f
			}
		}
	}
	if family == "" {
		return profileWrite{}, false
	}
	// Bound supported efforts instead of trusting agent-supplied capabilities.
	if o.Effort != "default" && o.Effort != "medium" && o.Effort != "high" && o.Effort != "xhigh" && o.Effort != "low" {
		return profileWrite{}, false
	}
	sum := sha256.Sum256([]byte(o.Harness + "/" + o.Model + "/" + o.Effort))
	return profileWrite{Slug: "observed-" + o.Harness + "-" + hex.EncodeToString(sum[:12]), Version: "observed-1", Harness: o.Harness, Family: family, Model: o.Model, Effort: o.Effort, Tier: "standard"}, true
}

// EvidenceID derives a stable id from public evidence.
func EvidenceID(evidence string) string { return modelreport.EvidenceID(evidence) }
