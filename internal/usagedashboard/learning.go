// SPDX-License-Identifier: AGPL-3.0-only
package usagedashboard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const LearningMinimum = 5
const LearningWindow = 30
const maxLearningSamples = 12000

// LearningCell uses the actual model version, never the catalog revision or
// the profile planned by a preference. A moving alias needs raw version evidence.
type LearningCell struct {
	ProfileID, Family, Line, Version, Effort, Kind, Bucket string
}

func CellFor(p modelregistry.Profile, kind, bucket string) LearningCell {
	family, line, version := modelregistry.ProfileLine(p)
	if version == "alias" && p.ModelVersion != "" {
		version = p.ModelVersion
	}
	return LearningCell{p.ID, family, line, version, strings.ToLower(p.Effort), kind, bucket}
}

func (c LearningCell) SameLine(s LearningCell) bool {
	return c.Family == s.Family && c.Line == s.Line && c.Effort == s.Effort && c.Kind == s.Kind && c.Bucket == s.Bucket
}
func (c LearningCell) Exact(s LearningCell) bool {
	return c.SameLine(s) && c.Version == s.Version && c.Version != "alias" && c.Version != ""
}

type LearningSample struct {
	Cell          LearningCell
	Hours, Tokens float64
	EstimateHours *float64
}

// LoadLearningSamples is the read-only AEON-503 learning query. Count a
// completed ticket once, with fully measured worker runs and complete final
// counters. Mixed-model/placement tickets, missing active time and unknown
// identity are excluded rather than attributed to a guessed model. Wall time
// and elapsed_seconds include waits and must never substitute for active_ms.
// visible gates both the source project and the outcome project.
func LoadLearningSamples(ctx context.Context, tx pgx.Tx, targets []LearningCell, project string, visible func(string) bool) ([]LearningSample, error) {
	families := []string{}
	for _, c := range targets {
		if !contains(families, c.Family) {
			families = append(families, c.Family)
		}
	}
	if len(families) == 0 {
		return nil, nil
	}
	// Resolve source permission before selecting windows, so hidden history
	// cannot crowd visible samples out of the newest-30 window.
	projectRows, err := tx.Query(ctx, `SELECT DISTINCT project_id::text FROM outcome_events
 WHERE kind='ticket_done' AND ($1::text='' OR project_id=NULLIF($1,'')::uuid) LIMIT 4097`, project)
	if err != nil {
		return nil, err
	}
	projects := []string{}
	for projectRows.Next() {
		var id string
		if err := projectRows.Scan(&id); err != nil {
			projectRows.Close()
			return nil, err
		}
		projects = append(projects, id)
	}
	err = projectRows.Err()
	projectRows.Close()
	if err != nil {
		return nil, err
	}
	if len(projects) > 4096 {
		return nil, workorders.Fail(http.StatusBadRequest, "too many history projects; narrow the project")
	}
	allowed := []string{}
	for _, id := range projects {
		if visible(id) {
			allowed = append(allowed, id)
		}
	}
	if len(allowed) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `WITH done AS (
 SELECT DISTINCT ON (o.ticket_node_id) o.ticket_node_id, o.project_id, o.recorded_at
 FROM outcome_events o JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.ticket_node_id
 WHERE o.tenant_id=current_setting('aeon.tenant_id')::uuid AND o.kind='ticket_done'
 AND o.session_id IS NOT NULL AND n.deleted_at IS NULL AND o.project_id=ANY($3::uuid[])
 AND ($2::text='' OR o.project_id=NULLIF($2,'')::uuid)
 ORDER BY o.ticket_node_id,o.recorded_at DESC,o.id DESC
), workers AS (
 SELECT d.ticket_node_id,d.project_id,d.recorded_at,s.id,s.run_id,s.project_id AS source_project,
 s.model_profile_id,s.model_raw,s.reasoning_effort,s.work_placement,
 r.active_ms,u.tokens,
 (s.stopped_at IS NOT NULL AND s.stopped_at<=d.recorded_at AND r.status='completed'
  AND s.project_id=d.project_id AND r.active_ms>0 AND u.complete AND u.tokens>0 AND s.model_profile_id IS NOT NULL
  AND s.work_placement->>'kind' IS NOT NULL AND s.work_placement->>'bucket' IN ('normal','complex')) AS complete,
 snap.snapshot
 FROM done d JOIN harness_sessions s ON s.tenant_id=current_setting('aeon.tenant_id')::uuid
 AND s.ticket_node_id=d.ticket_node_id AND s.role='worker' AND s.created_at<=d.recorded_at
 LEFT JOIN LATERAL (SELECT x.started_at,x.snapshot FROM ticket_estimate_snapshots x
  WHERE x.tenant_id=s.tenant_id AND x.ticket_node_id=d.ticket_node_id AND x.started_at<=d.recorded_at
  ORDER BY x.started_at DESC,x.id DESC LIMIT 1) snap ON true
 LEFT JOIN agent_runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
 LEFT JOIN LATERAL (SELECT sum(x.input_tokens+x.output_tokens)::float8 AS tokens,
  bool_and(x.input_tokens IS NOT NULL AND x.output_tokens IS NOT NULL AND x.cached_input_tokens IS NOT NULL AND NOT x.provisional) AS complete
  FROM harness_session_usage x WHERE x.tenant_id=s.tenant_id AND x.session_id=s.id) u ON true
 WHERE (snap.started_at IS NULL OR s.created_at>=snap.started_at)
 AND ((SELECT aeon_visible_all()) OR s.project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
), tickets AS (
 SELECT ticket_node_id,project_id,recorded_at,min(source_project::text) AS source_project,
 min(model_profile_id::text) AS profile_id,min(coalesce(model_raw,'')) AS raw,
 min(lower(coalesce(reasoning_effort,''))) AS effort,
 min(work_placement->>'kind') AS kind,min(work_placement->>'bucket') AS bucket,
 sum(active_ms)::float8/3600000 AS hours,sum(tokens) AS tokens,
 min(CASE WHEN jsonb_typeof(snapshot->'estimate_hours')='number' THEN (snapshot->>'estimate_hours')::float8 END) AS estimate
 FROM workers GROUP BY ticket_node_id,project_id,recorded_at
 HAVING bool_and(coalesce(complete,false)) AND count(DISTINCT run_id)=count(*)
 AND count(DISTINCT model_profile_id)=1 AND count(DISTINCT coalesce(model_raw,''))=1
 AND count(DISTINCT lower(coalesce(reasoning_effort,'')))=1
 AND count(DISTINCT work_placement->>'kind')=1 AND count(DISTINCT work_placement->>'bucket')=1
 AND count(DISTINCT source_project)=1
), ranked AS (
 SELECT t.*,p.harness,p.family,p.model,p.effort AS profile_effort,
 row_number() OVER (PARTITION BY p.id,t.raw,t.effort,t.kind,t.bucket ORDER BY t.recorded_at DESC,t.ticket_node_id) AS rn
 FROM tickets t JOIN model_profiles p ON p.tenant_id=current_setting('aeon.tenant_id')::uuid AND p.id=t.profile_id::uuid
 WHERE p.family=ANY($1::text[])
)
SELECT project_id::text,source_project,profile_id,harness,family,model,profile_effort,raw,effort,kind,bucket,hours,tokens,estimate
FROM ranked WHERE rn<=30 ORDER BY recorded_at DESC,ticket_node_id LIMIT 12001`, families, project, allowed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		sample                        LearningSample
		outcomeProject, sourceProject string
	}
	pending := []candidate{}
	out := []LearningSample{}
	count := 0
	for rows.Next() {
		count++
		if count > maxLearningSamples {
			return nil, workorders.Fail(http.StatusBadRequest, "too much model history; narrow the project")
		}
		var p modelregistry.Profile
		var sample LearningSample
		var outcomeProject, sourceProject, raw, effort, kind, bucket string
		if err := rows.Scan(&outcomeProject, &sourceProject, &p.ID, &p.Harness, &p.Family, &p.Model, &p.Effort, &raw, &effort, &kind, &bucket, &sample.Hours, &sample.Tokens, &sample.EstimateHours); err != nil {
			return nil, err
		}

		// A conflicting reported effort is not the profile's effort.
		if effort != "" && effort != strings.ToLower(p.Effort) {
			continue
		}
		sample.Cell = CellFor(p, kind, bucket)
		if sample.Cell.Version == "alias" && raw != "" {
			actual := p
			actual.Model = raw
			f, l, v := modelregistry.ProfileLine(actual)
			if f == sample.Cell.Family && l == sample.Cell.Line && v != "alias" && v != "" {
				sample.Cell.Version = v
			}
		}
		for _, c := range targets {
			if c.SameLine(sample.Cell) || c.ProfileID == sample.Cell.ProfileID {
				pending = append(pending, candidate{sample, outcomeProject, sourceProject})
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// Permission callbacks may use this transaction; close the result first.
	for _, c := range pending {
		if visible(c.outcomeProject) && visible(c.sourceProject) {
			out = append(out, c.sample)
		}
	}
	return out, nil
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func LearningMedian(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	m := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[m]
	}
	return (sorted[m-1] + sorted[m]) / 2
}

type ModelEstimateHistory struct {
	State        string   `json:"state"`
	Basis        string   `json:"basis"`
	Tickets      int      `json:"tickets"`
	Hours        *float64 `json:"hours"`
	Tokens       *int64   `json:"tokens"`
	SpeedFactor  *float64 `json:"speed_factor"`
	SpeedTickets int      `json:"speed_tickets"`
}

// History never publishes a typical number below the minimum. Speed is
// learned only from the exact cell with a frozen, positive size estimate.
func History(samples []LearningSample, c LearningCell) ModelEstimateHistory {
	exact := SelectLearning(samples, func(s LearningSample) bool { return c.Exact(s.Cell) })
	chosen := exact
	level := "cell"
	if len(chosen) < LearningMinimum {
		chosen = SelectLearning(samples, func(s LearningSample) bool { return c.SameLine(s.Cell) })
		level = "line"
	}
	out := ModelEstimateHistory{State: "uncalibrated", Tickets: len(chosen), Basis: fmt.Sprintf("uncalibrated %s %s on %s %s %s (n=%d)", c.Bucket, c.Kind, c.Line, c.Version, c.Effort, len(chosen))}
	var speeds []float64
	for _, s := range exact {
		if s.EstimateHours != nil && *s.EstimateHours > 0 {
			speeds = append(speeds, s.Hours / *s.EstimateHours)
		}
	}
	out.SpeedTickets = len(speeds)
	if len(speeds) >= LearningMinimum {
		v := LearningMedian(speeds)
		out.SpeedFactor = &v
	}
	if len(chosen) < LearningMinimum {
		return out
	}
	var hours, tokens []float64
	for _, s := range chosen {
		hours = append(hours, s.Hours)
		tokens = append(tokens, s.Tokens)
	}
	h, n := LearningMedian(hours), int64(math.Round(LearningMedian(tokens)))
	out.State, out.Hours, out.Tokens = "calibrated", &h, &n
	out.Basis = LearningBasis(chosen, c, level)
	return out
}

func SelectLearning(samples []LearningSample, matches func(LearningSample) bool) []LearningSample {
	out := []LearningSample{}
	for _, s := range samples {
		if matches(s) {
			out = append(out, s)
			if len(out) == LearningWindow {
				break
			}
		}
	}
	return out
}

func LearningBasis(samples []LearningSample, c LearningCell, level string) string {
	versions := []string{}
	for _, s := range samples {
		if !contains(versions, s.Cell.Version) {
			versions = append(versions, s.Cell.Version)
		}
	}
	sort.Strings(versions)
	work := c.Bucket + " " + c.Kind
	if level == "profile" {
		work = "across kinds and complexity buckets"
	}
	return fmt.Sprintf("median of %d %s tickets on %s %s (%s; based on %s; n=%d)", len(samples), work, c.Line, c.Effort, level, strings.Join(versions, ", "), len(samples))
}

func (m *Module) modelEstimates(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	q := r.URL.Query()
	id, kind, bucket, project := q.Get("profile_id"), q.Get("kind"), q.Get("bucket"), q.Get("project_id")
	if len(r.URL.RawQuery) > 2048 || !workorders.UUID(id) || len(kind) == 0 || len(kind) > 64 || strings.ContainsAny(kind, "\r\n\t") || (bucket != "normal" && bucket != "complex") || (project != "" && !workorders.UUID(project)) {
		return nil, workorders.Fail(http.StatusBadRequest, "invalid model estimate query")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "harness.read", authz.Scope{ProjectID: project}); err != nil {
		return nil, err
	}
	var profile modelregistry.Profile
	if err := tx.QueryRow(r.Context(), `SELECT p.id::text,p.harness,p.family,p.model,p.effort,coalesce(d.model_display->>'model_version','') FROM model_profiles p LEFT JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id WHERE p.id=$1::uuid`, id).Scan(&profile.ID, &profile.Harness, &profile.Family, &profile.Model, &profile.Effort, &profile.ModelVersion); err != nil {
		if err == pgx.ErrNoRows {
			return nil, workorders.Fail(404, "model profile not found")
		}
		return nil, err
	}
	cell := CellFor(profile, kind, bucket)
	var visibilityErr error
	cache := map[string]bool{}
	visible := func(project string) bool {
		if v, ok := cache[project]; ok {
			return v
		}
		err := authz.RequireTx(r.Context(), tx, p, "harness.read", authz.Scope{ProjectID: project})
		if err != nil && !errors.Is(err, authz.ErrForbidden) {
			visibilityErr = err
		}
		cache[project] = err == nil
		return err == nil
	}
	samples, err := LoadLearningSamples(r.Context(), tx, []LearningCell{cell}, project, visible)
	if err != nil {
		return nil, err
	}
	if visibilityErr != nil {
		return nil, visibilityErr
	}
	return History(samples, cell), nil
}
