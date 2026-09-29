// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const analysisSampleLimit = 5000

// Latest-at-event attribution prevents a later instruction report from
// rewriting the cohort of an earlier failure. Only server-recorded merged
// identities assign a rules version; a client-supplied outcome version must
// agree. File hashes may come from local instruction provenance.
const analysisProvenance = `
 LEFT JOIN LATERAL (
  SELECT id FROM harness_instruction_provenance
  WHERE session_id=s.id AND created_at<=x.at ORDER BY revision DESC LIMIT 1
 ) revision ON true
 LEFT JOIN LATERAL (
  SELECT max(version) FILTER (WHERE kind='rules_merged') AS version,
   coalesce(array_agg(content_sha256) FILTER (WHERE kind IN ('agents','claude','skill') AND hash_kind='content'),'{}') AS hashes
  FROM harness_instruction_provenance_items WHERE provenance_id=revision.id
 ) provenance ON true`

func loadAnalysisSamples(ctx context.Context, tx pgx.Tx, tid string, from, until time.Time) ([]analysisSample, error) {
	rows, err := tx.Query(ctx, `
 WITH x AS (
  SELECT 'o:'||o.id::text AS id,o.ticket_node_id,o.session_id,o.rules_version,o.kind,o.payload,o.recorded_at AS at FROM outcome_events o
  WHERE recorded_at >= $1 AND recorded_at < $2
  UNION ALL
  SELECT 'v:'||v.id::text,v.ticket_node_id,v.session_id,NULL,'vote',jsonb_build_object('summary',v.comment),v.created_at FROM agent_delivery_votes v
  WHERE created_at >= $1 AND created_at < $2
 )
 SELECT x.id,n.id::text,n.key,project.key,k.slug,s.harness,provenance.version,provenance.hashes,x.kind,x.payload,x.at
 FROM x JOIN nodes n ON n.id=x.ticket_node_id AND n.tenant_id=$3 AND n.deleted_at IS NULL
 JOIN nodes project ON project.id=n.project_id AND project.tenant_id=n.tenant_id AND project.deleted_at IS NULL
 JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id
 JOIN harness_sessions s ON s.id=x.session_id AND s.tenant_id=n.tenant_id
 `+analysisProvenance+`
 WHERE provenance.version IS NOT NULL AND (x.rules_version IS NULL OR x.rules_version=provenance.version)
 ORDER BY x.at,x.id LIMIT 5001`, from, until, tid)
	if err != nil {
		return nil, err
	}
	samples := []analysisSample{}
	for rows.Next() {
		var s analysisSample
		var project string
		var raw []byte
		if err := rows.Scan(&s.ID, &s.TicketID, &s.TicketKey, &project, &s.TicketKind, &s.Harness, &s.Version, &s.FileHashes, &s.Kind, &raw, &s.At); err != nil {
			rows.Close()
			return nil, err
		}
		var p struct {
			Summary, Verdict, Result string
			Round                    int
			Elapsed                  *float64 `json:"elapsed_seconds"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			rows.Close()
			return nil, err
		}
		s.Summary, s.Result, s.Round, s.Elapsed = p.Summary, p.Result, p.Round, -1
		if p.Verdict != "" {
			s.Result = p.Verdict
		}
		if p.Elapsed != nil {
			s.Elapsed = *p.Elapsed
		}
		s.Href = "/p/" + url.PathEscape(project) + "/" + url.PathEscape(s.TicketKey)
		samples = append(samples, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(samples) > analysisSampleLimit {
		return nil, fail(503, "analysis_budget", "Outcome scan budget reached; no partial analysis was proposed.")
	}
	// Reuse the current inbox so a dismissed learning or deleted/edited comment
	// cannot continue to generate proposals from stale event text.
	projects, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project' AND n.deleted_at IS NULL ORDER BY n.id LIMIT 501`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for projects.Next() {
		var id string
		if err := projects.Scan(&id); err != nil {
			projects.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = projects.Err()
	projects.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 500 {
		return nil, fail(503, "analysis_budget", "Project scan budget reached.")
	}
	for _, id := range ids {
		page, err := knowledge.AnalysisLearnings(ctx, tx, tid, id)
		if err != nil {
			return nil, err
		}
		if page.Truncated {
			return nil, fail(503, "analysis_budget", "Learning scan budget reached; no partial analysis was proposed.")
		}
		for _, l := range page.Items {
			if l.At.Before(from) || !l.At.Before(until) {
				continue
			}
			s := analysisSample{analysisEvidence: analysisEvidence{ID: l.ID, TicketID: l.NodeID, TicketKey: l.Key, Href: l.Href, Kind: "learning"}, Summary: l.Text, At: l.At}
			err := tx.QueryRow(ctx, `WITH x AS (SELECT $2::timestamptz AS at)
    SELECT k.slug,s.harness,provenance.version,provenance.hashes FROM x
    JOIN nodes n ON n.id=$1 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
    JOIN LATERAL (SELECT * FROM harness_sessions WHERE ticket_node_id=n.id AND created_at<=x.at ORDER BY created_at DESC,id LIMIT 1) s ON true
    `+analysisProvenance+` WHERE provenance.version IS NOT NULL`, l.NodeID, l.At).Scan(&s.TicketKind, &s.Harness, &s.Version, &s.FileHashes)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			samples = append(samples, s)
			if len(samples) > analysisSampleLimit {
				return nil, fail(503, "analysis_budget", "Analysis sample budget reached.")
			}
		}
	}
	return samples, nil
}

func readFindings(ctx context.Context, tx pgx.Tx, activeOnly bool) ([]findingData, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,coalesce(source_id::text,''),path,rule_key,coalesce(proposal_id::text,''),status,data,created_at FROM doctrine_findings WHERE (NOT $1 OR status IN ('pending','draft')) ORDER BY created_at DESC,id LIMIT 100`, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []findingData{}
	for rows.Next() {
		var f findingData
		var raw []byte
		var id, sid, path, key, pid, status string
		var at time.Time
		if err := rows.Scan(&id, &sid, &path, &key, &pid, &status, &raw, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		f.ID, f.SourceID, f.Path, f.RuleKey, f.ProposalID, f.Status, f.CreatedAt = id, sid, path, key, pid, status, at
		out = append(out, f)
	}
	return out, rows.Err()
}

func (m *Module) listFindings(r *http.Request, actor tenant.Principal) (any, error) {
	if actor.Kind != tenant.Person || actor.KeyCreatorID != "" {
		return nil, authz.ErrForbidden
	}
	out := struct {
		Findings []finding `json:"findings"`
	}{Findings: []finding{}}
	err := db.InTenant(r.Context(), m.pool, actor.TenantID, func(tx pgx.Tx) error {
		// Findings join projects: a single-project grant must not reveal the
		// workspace's aggregate or references to other projects.
		for _, permission := range []string{"rules.read", "outcome.read", "knowledge.read"} {
			if err := authz.RequireTx(r.Context(), tx, actor, permission, authz.Scope{}); err != nil {
				return err
			}
		}
		rows, err := readFindings(r.Context(), tx, false)
		if err != nil {
			return err
		}
		for _, f := range rows {
			out.Findings = append(out.Findings, f.finding)
		}
		return nil
	})
	return out, err
}

func analysisEvent(ctx context.Context, tx pgx.Tx, actor tenant.Principal, f finding, event string) error {
	_, err := events.Append(ctx, tx, actor, events.Change{Type: event, After: map[string]any{"finding_id": f.ID, "status": f.Status, "pattern": f.Pattern, "count": f.Count, "proposal_id": f.ProposalID}, Metadata: json.RawMessage(`{"job":"doctrine-outcome-analysis","reason":"deterministic outcome evidence"}`)})
	return err
}

func saveFinding(ctx context.Context, tx pgx.Tx, actor tenant.Principal, f findingData, event string) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE doctrine_findings SET status=$2,proposal_id=NULLIF($3,'')::uuid,data=$4 WHERE id=$1`, f.ID, f.Status, f.ProposalID, raw)
	if err != nil {
		return err
	}
	return analysisEvent(ctx, tx, actor, f.finding, event)
}
