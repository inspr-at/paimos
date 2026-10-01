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
func ReportInSession(ctx context.Context, tx pgx.Tx, p tenant.Principal, harness string, in []Observation) error {
	if len(in) == 0 {
		return nil
	}
	for _, o := range in {
		if o.Harness != harness {
			return fail(400, "model report harness mismatch")
		}
	}
	_, err := recordReports(ctx, tx, p, in, "agent")
	return err
}

func recordReports(ctx context.Context, tx pgx.Tx, p tenant.Principal, in []Observation, source string) (ReportResult, error) {
	out := ReportResult{}
	if err := validateObservations(in); err != nil {
		return out, err
	}
	if err := ensureCatalog(ctx, tx, p); err != nil {
		return out, err
	}
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
	for _, o := range in {
		raw, _ := json.Marshal(o)
		tag, err := tx.Exec(ctx, `INSERT INTO model_report_receipts(tenant_id,principal_id,report_id,content) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, p.TenantID, p.ID, o.ReportID, string(raw))
		if err != nil {
			return out, err
		}
		if tag.RowsAffected() == 0 {
			var equal bool
			if err := tx.QueryRow(ctx, `SELECT content=$3::jsonb FROM model_report_receipts WHERE principal_id=$1 AND report_id=$2`, p.ID, o.ReportID, string(raw)).Scan(&equal); err != nil {
				return out, err
			}
			if !equal {
				return out, fail(409, "report id already used for different evidence")
			}
			continue
		}
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
		if _, err := insertProfile(ctx, tx, p.TenantID, pin); err != nil {
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
 last_failing_at=CASE WHEN $5='invalid' THEN now() ELSE model_observations.last_failing_at END,
 failures=CASE WHEN $5='working' THEN 0 WHEN $5='invalid' THEN least(model_observations.failures+1,1000) ELSE model_observations.failures END,
 suppressed_until=CASE WHEN $5='working' THEN NULL WHEN $5='invalid' AND model_observations.failures>=1 THEN now()+interval '24 hours' ELSE model_observations.suppressed_until END`, tenantID, o.Harness, o.Model, o.Effort, o.Status, source)
	return err
}

// Known pins retain declared tiers and efforts. Unknown identifiers are never
// added to ladders; conservative profiles are merely available for person policy.
func observedPin(o Observation) (profileWrite, bool) {
	for _, model := range seedModels {
		if model.Harness == o.Harness && model.ID == o.Model {
			for _, e := range model.Efforts {
				if e == o.Effort {
					return profileWrite{catalogSlug(model, e), CatalogVersion, model.Harness, model.Family, model.ID, e, model.Tier}, true
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
	return profileWrite{"observed-" + o.Harness + "-" + hex.EncodeToString(sum[:12]), "observed-1", o.Harness, family, o.Model, o.Effort, "standard"}, true
}

// EvidenceID derives a stable id from public evidence.
func EvidenceID(evidence string) string { return modelreport.EvidenceID(evidence) }
