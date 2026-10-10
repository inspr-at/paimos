// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/inspr-at/paimos/internal/credentialguard"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const (
	flowDefaultWindow = 45 * time.Minute
	flowMaxWindow     = 31 * 24 * time.Hour
	flowRolloutBytes  = 256 << 10
	flowRolloutSteps  = 200
	flowRolloutIncs   = 20
)

var (
	flowRelease   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	flowReportKey = regexp.MustCompile(`^[A-Za-z0-9._:/+-]{1,120}$`)
	// flowEvidenceRef is the reference shape scripts/verify-live.mjs already
	// requires of qualification.evidence: a ticket path, never a URL.
	flowEvidenceRef = regexp.MustCompile(`^[A-Za-z0-9._/#-]{1,200}$`)
	// The rollback classes of Arion v5 §3b.
	flowRollbackClasses = []string{"digest_safe", "restore_required"}
)

func (m *Module) mountFlow(mux *http.ServeMux) {
	mount := func(pattern string, timeout time.Duration, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			if r.Method != http.MethodGet {
				controller := http.NewResponseController(w)
				_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
				defer controller.SetReadDeadline(time.Time{})
			}
			handler(w, r.WithContext(ctx))
		})
	}
	mount("GET /api/projects/{projectId}/delivery/flow", 15*time.Second, m.getFlow)
	mount("GET /api/projects/{projectId}/delivery/flow/runs/{itemId}", 15*time.Second, m.getFlowRun)
	mount("POST /api/projects/{projectId}/delivery/flow/rollout", 15*time.Second, m.reportRollout)
	// The stream manages its own deadlines (heartbeats, write timeouts).
	mux.HandleFunc("GET /api/projects/{projectId}/delivery/flow/stream", m.flowStream)
}

func flowQueryTime(r *http.Request, name string, def time.Time) (time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return def, fail(400, "invalid "+name+": RFC 3339 time expected")
	}
	return t.UTC(), nil
}

// getFlow returns the runs active in [from, to] with their steps and
// incidents, and each run's progress and ETA at the moment at.
func (m *Module) getFlow(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	now := m.now().UTC().Truncate(time.Second)
	to, err := flowQueryTime(r, "to", now)
	if err == nil {
		var from, at time.Time
		if from, err = flowQueryTime(r, "from", to.Add(-flowDefaultWindow)); err == nil {
			if at, err = flowQueryTime(r, "at", minTime(now, to)); err == nil {
				if !from.Before(to) || to.Sub(from) > flowMaxWindow {
					err = fail(400, "from must be before to, at most 31 days apart")
				} else {
					m.writeFlow(w, r, p.TenantID, project, DeliveryFlow{ProjectID: project, Now: now, At: at, From: from, To: to}, p)
					return
				}
			}
		}
	}
	respondError(w, err)
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (m *Module) writeFlow(w http.ResponseWriter, r *http.Request, tid, project string, out DeliveryFlow, p tenant.Principal) {
	out.Items, out.Steps, out.Incidents = []FlowItem{}, []FlowStep{}, []FlowIncident{}
	err := db.InTenant(r.Context(), m.pool, tid, func(tx pgx.Tx) error {
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.read", project); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT `+flowItemColumns+` FROM delivery_flow_items
			WHERE project_id=$1 AND coalesce(started_at,updated_at)<=$2 AND (ended_at IS NULL OR ended_at>=$3)
			ORDER BY started_at DESC NULLS LAST,id LIMIT $4`, project, out.To, out.From, flowItemLimit+1)
		if err != nil {
			return err
		}
		var items []flowItemRow
		for rows.Next() {
			i, err := scanFlowItem(rows)
			if err != nil {
				rows.Close()
				return err
			}
			items = append(items, i)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(items) > flowItemLimit {
			items, out.Truncated = items[:flowItemLimit], true
		}
		if len(items) == 0 {
			return nil
		}
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ID
		}
		// Every step of a returned run, so progress and lanes are complete;
		// the response then keeps the steps that touch the window.
		steps, truncated, err := loadFlowStepsTx(r.Context(), tx, ids, flowStepLimit)
		if err != nil {
			return err
		}
		out.Truncated = out.Truncated || truncated
		incidents, truncated, err := loadFlowIncidentsTx(r.Context(), tx, ids)
		if err != nil {
			return err
		}
		out.Truncated = out.Truncated || truncated
		history, err := loadFlowHistoryTx(r.Context(), tx, project, out.At)
		if err != nil {
			return err
		}
		byItem := map[string][]FlowStep{}
		for _, s := range steps {
			byItem[s.ItemID] = append(byItem[s.ItemID], s)
			if !s.StartedAt.After(out.To) && (s.EndedAt == nil || !s.EndedAt.Before(out.From)) {
				s.Norm = flowNorm(history, s)
				out.Steps = append(out.Steps, s)
			}
		}
		for _, i := range items {
			out.Items = append(out.Items, flowItemView(i, byItem[i.ID], history, out.At))
		}
		for _, inc := range incidents {
			if !inc.StartedAt.After(out.To) && (inc.EndedAt == nil || !inc.EndedAt.Before(out.From)) {
				out.Incidents = append(out.Incidents, inc)
			}
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// getFlowRun returns one whole run for Replay and Compare.
func (m *Module) getFlowRun(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	item := r.PathValue("itemId")
	if !workorders.UUID(item) {
		respondError(w, fail(400, "invalid flow run"))
		return
	}
	now := m.now().UTC().Truncate(time.Second)
	at, err := flowQueryTime(r, "at", now)
	if err != nil {
		respondError(w, err)
		return
	}
	out := DeliveryFlowRun{ProjectID: project, Now: now, At: at}
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.read", project); err != nil {
			return err
		}
		row, err := scanFlowItem(tx.QueryRow(r.Context(), `SELECT `+flowItemColumns+` FROM delivery_flow_items WHERE id=$1 AND project_id=$2`, item, project))
		if err != nil {
			return err
		}
		steps, truncated, err := loadFlowStepsTx(r.Context(), tx, []string{row.ID}, flowStepLimit)
		if err != nil {
			return err
		}
		incidents, incTruncated, err := loadFlowIncidentsTx(r.Context(), tx, []string{row.ID})
		if err != nil {
			return err
		}
		history, err := loadFlowHistoryTx(r.Context(), tx, project, at)
		if err != nil {
			return err
		}
		for i := range steps {
			steps[i].Norm = flowNorm(history, steps[i])
		}
		out.Item = flowItemView(row, steps, history, at)
		out.Steps, out.Incidents, out.Truncated = steps, incidents, truncated || incTruncated
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// rolloutInput is the coordinator's aeon.rollout.v1 record with the flow
// extension. Other rollout fields (digests, pin, observation, the rest of the
// qualification) are accepted and ignored: nothing outside these fields is
// stored.
type rolloutInput struct {
	Schema        string            `json:"schema"`
	Direction     string            `json:"direction"`
	Outcome       string            `json:"outcome"`
	Release       string            `json:"release"`
	Version       string            `json:"version"`
	Title         *string           `json:"title"`
	PRs           []int64           `json:"prs"`
	CutAt         *time.Time        `json:"cut_at"`
	LiveAt        *time.Time        `json:"live_at"`
	HealthyAt     *time.Time        `json:"healthy_at"`
	Target        *rolloutTarget    `json:"target"`
	NextHumanGate *rolloutGate      `json:"next_human_gate"`
	ETA           *rolloutETA       `json:"eta"`
	Steps         []json.RawMessage `json:"steps"`
	Incidents     []json.RawMessage `json:"incidents"`
	// Qualification is the record's hands-on qualification object; only its
	// evidence reference is stored. RollbackClass is the class computed for
	// the release (Arion v5 §3b).
	Qualification *rolloutQualification `json:"qualification"`
	RollbackClass *string               `json:"rollback_class"`
}

type rolloutQualification struct {
	Evidence *string `json:"evidence"`
}

type rolloutTarget struct {
	Minutes  int    `json:"minutes"`
	FromStep string `json:"from_step"`
}

type rolloutGate struct {
	PrincipalID *string `json:"principal_id"`
	What        string  `json:"what"`
}

type rolloutETA struct {
	P50At *time.Time `json:"p50_at"`
	P90At *time.Time `json:"p90_at"`
}

type rolloutStep struct {
	Key        string     `json:"key"`
	Step       string     `json:"step"`
	Round      *int       `json:"round"`
	Kind       string     `json:"kind"`
	Actor      FlowActor  `json:"actor"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	Outcome    *string    `json:"outcome"`
	WaitReason *string    `json:"wait_reason"`
	WaitsFor   *string    `json:"waits_for"`
	Side       bool       `json:"side"`
}

type rolloutIncident struct {
	Key           string     `json:"key"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
	Severity      string     `json:"severity"`
	Summary       string     `json:"summary"`
	RecoverySteps []string   `json:"recovery_steps"`
}

type RolloutResult struct {
	ItemID  string `json:"item_id"`
	Steps   int    `json:"steps"`
	Changed int    `json:"changed"`
}

func strictJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return fail(400, "trailing JSON")
	}
	return nil
}

// flowText refuses labels and summaries that look like a credential: flow
// payloads are read by every project member.
func flowText(s string, max int) bool {
	return len(s) <= max && !credentialguard.Contains(s)
}

func flowTimeOK(t time.Time, now time.Time) bool {
	return !t.IsZero() && !t.After(now.Add(5*time.Minute)) && !t.Before(now.AddDate(0, 0, -400))
}

func validActor(a FlowActor) bool {
	if !validFlowEnum(flowActorTypes, a.Type) || a.Label == "" || !flowText(a.Label, 80) || a.PrincipalID != nil && !workorders.UUID(*a.PrincipalID) {
		return false
	}
	return a.Model == nil || *a.Model != "" && flowText(*a.Model, 80)
}

func validFlowStepInput(s flowStepInput, now time.Time) bool {
	if !validFlowEnum(flowStepKeys, s.StepKey) || !validFlowEnum(flowStepKinds, s.Kind) || s.Round < 1 || s.Round > 1000 || !validActor(s.Actor) || !flowTimeOK(s.Started, now) {
		return false
	}
	if s.Ended != nil && (!flowTimeOK(*s.Ended, now) || s.Ended.Before(s.Started) || s.Ended.Sub(s.Started) > 7*24*time.Hour) {
		return false
	}
	if s.Outcome != nil && !validFlowEnum(flowOutcomes, *s.Outcome) || s.WaitsFor != nil && !workorders.UUID(*s.WaitsFor) {
		return false
	}
	// Only a wait has a wait reason.
	return s.WaitReason == nil || s.Kind == "wait" && validFlowEnum(flowWaitReasons, *s.WaitReason)
}

func parseRollout(in rolloutInput, project string, now time.Time) (flowBatch, error) {
	bad := func(msg string) (flowBatch, error) { return flowBatch{}, fail(400, msg) }
	if in.Schema != "aeon.rollout.v1" {
		return bad("schema must be aeon.rollout.v1")
	}
	if in.Direction != "" && in.Direction != "forward" {
		return bad("only forward rollouts are flow runs")
	}
	if !flowRelease.MatchString(in.Release) {
		return bad("release must be a release label such as 126")
	}
	if len(in.PRs) > 20 || len(in.Steps) > flowRolloutSteps || len(in.Incidents) > flowRolloutIncs {
		return bad("at most 20 prs, 200 steps and 20 incidents")
	}
	for _, pr := range in.PRs {
		if pr < 1 || pr > 2147483647 {
			return bad("invalid pull request number")
		}
	}
	title := "Release " + in.Release
	if in.Title != nil {
		if *in.Title == "" || !flowText(*in.Title, 300) {
			return bad("invalid release title")
		}
		title = *in.Title
	}
	for _, t := range []*time.Time{in.CutAt, in.LiveAt, in.HealthyAt} {
		if t != nil && !flowTimeOK(*t, now) {
			return bad("rollout times must be within the last 400 days")
		}
	}
	item := flowItemInput{Project: project, Kind: "release", Ref: in.Release, Title: title, PRs: in.PRs, Started: in.CutAt,
		Target: &FlowTarget{Minutes: releaseTargetMin, FromStep: "a", Source: "Arion"}, SetGate: true, SetETA: true}
	if in.Target != nil {
		if in.Target.Minutes < 1 || in.Target.Minutes > 10080 || !validFlowEnum(flowStepKeys, in.Target.FromStep) {
			return bad("invalid target")
		}
		item.Target = &FlowTarget{Minutes: in.Target.Minutes, FromStep: in.Target.FromStep, Source: "Arion"}
	}
	switch in.Outcome {
	case "live", "success":
		item.Ended = in.HealthyAt
		if item.Ended == nil {
			item.Ended = in.LiveAt
		}
	case "", "in_progress", "failed":
	default:
		return bad("outcome must be in_progress, live, success or failed")
	}
	if q := in.Qualification; q != nil && q.Evidence != nil {
		if !flowEvidenceRef.MatchString(*q.Evidence) || !flowText(*q.Evidence, 200) {
			return bad("invalid qualification evidence reference")
		}
		item.QualificationEvidence = q.Evidence
	}
	if c := in.RollbackClass; c != nil {
		if !validFlowEnum(flowRollbackClasses, *c) {
			return bad("rollback_class must be digest_safe or restore_required")
		}
		item.RollbackClass = c
	}
	if g := in.NextHumanGate; g != nil {
		if g.What == "" || !flowText(g.What, 120) || g.PrincipalID != nil && !workorders.UUID(*g.PrincipalID) {
			return bad("invalid next human gate")
		}
		item.Gate = &FlowGate{PrincipalID: g.PrincipalID, What: g.What}
	}
	if e := in.ETA; e != nil {
		if e.P50At == nil || e.P90At == nil || e.P90At.Before(*e.P50At) || e.P50At.Before(now.AddDate(0, 0, -400)) || e.P90At.After(now.AddDate(0, 0, 30)) {
			return bad("eta needs p50_at no later than p90_at")
		}
		item.OpsP50, item.OpsP90 = e.P50At, e.P90At
	}
	out := flowBatch{Item: item}
	keys := map[string]bool{}
	for _, raw := range in.Steps {
		var s rolloutStep
		if err := strictJSON(raw, &s); err != nil {
			return bad("invalid rollout step")
		}
		round := 1
		if s.Round != nil {
			round = *s.Round
		}
		step := flowStepInput{Source: "ops_rollout", Key: s.Key, StepKey: s.Step, Round: round, Kind: s.Kind, Actor: s.Actor, Started: s.StartedAt, Ended: s.EndedAt, Outcome: s.Outcome, WaitReason: s.WaitReason, WaitsFor: s.WaitsFor, Side: s.Side}
		if !flowReportKey.MatchString(s.Key) || keys[s.Key] || !validFlowStepInput(step, now) {
			return bad("invalid rollout step " + s.Key)
		}
		keys[s.Key] = true
		out.Steps = append(out.Steps, step)
	}
	incidentKeys := map[string]bool{}
	for _, raw := range in.Incidents {
		var i rolloutIncident
		if err := strictJSON(raw, &i); err != nil {
			return bad("invalid rollout incident")
		}
		ok := flowReportKey.MatchString(i.Key) && !incidentKeys[i.Key] && flowTimeOK(i.StartedAt, now) && (i.Severity == "degraded" || i.Severity == "down") &&
			i.Summary != "" && flowText(i.Summary, 300) && len(i.RecoverySteps) <= 50 && (i.EndedAt == nil || flowTimeOK(*i.EndedAt, now) && !i.EndedAt.Before(i.StartedAt))
		for _, k := range i.RecoverySteps {
			ok = ok && keys[k]
		}
		if !ok {
			return bad("invalid rollout incident " + i.Key)
		}
		incidentKeys[i.Key] = true
		out.Incidents = append(out.Incidents, flowIncidentInput{Source: "ops_rollout", Key: i.Key, Started: i.StartedAt, Ended: i.EndedAt, Severity: i.Severity, Summary: i.Summary, RecoveryKeys: i.RecoverySteps})
	}
	return out, nil
}

// reportRollout records a release run from the OPS rollout record. The record
// is authoritative for its steps: a repeated report converges on the same
// rows and corrects them.
func (m *Module) reportRollout(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, flowRolloutBytes)
	dec := json.NewDecoder(r.Body)
	var in rolloutInput
	if err := dec.Decode(&in); err != nil {
		respondError(w, fail(400, "invalid rollout JSON"))
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		respondError(w, fail(400, "trailing rollout JSON"))
		return
	}
	batch, err := parseRollout(in, project, m.now())
	if err != nil {
		respondError(w, err)
		return
	}
	changed, err := m.flowWrite(r.Context(), p.TenantID, &p, func(ctx context.Context, tx pgx.Tx, apply func([]flowBatch) error) error {
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		// Authorized inside the write, under the tenant fence.
		if err := requireMetrics(ctx, tx, p, "delivery.manage", project); err != nil {
			return err
		}
		if err := knownPrincipalsTx(ctx, tx, batch); err != nil {
			return err
		}
		return apply([]flowBatch{batch})
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, RolloutResult{ItemID: flowItemID(p.TenantID, project, "release", in.Release), Steps: len(batch.Steps), Changed: changed})
}

// knownPrincipalsTx refuses actor and gate ids that are not principals of
// this workspace, so a report cannot point the page at a foreign identity.
func knownPrincipalsTx(ctx context.Context, tx pgx.Tx, b flowBatch) error {
	ids := map[string]bool{}
	for _, s := range b.Steps {
		if s.Actor.PrincipalID != nil {
			ids[*s.Actor.PrincipalID] = true
		}
	}
	if b.Item.Gate != nil && b.Item.Gate.PrincipalID != nil {
		ids[*b.Item.Gate.PrincipalID] = true
	}
	if len(ids) == 0 {
		return nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id=ANY($1::uuid[])`, list).Scan(&n); err != nil {
		return err
	}
	if n != len(list) {
		return fail(400, "unknown principal in rollout")
	}
	return nil
}
