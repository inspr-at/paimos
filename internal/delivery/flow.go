// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// Delivery Flow (AEON-1004): runs are releases and changes; every step names
// who works on it or what it waits for. Sources are the OPS rollout record
// (release steps a–l), PAIMOS rounds, review gates and holds, and GitHub App
// workflow runs (CI and merge queue). Payloads carry timing facts only.

type FlowActor struct {
	Type        string  `json:"type"`
	PrincipalID *string `json:"principal_id"`
	Label       string  `json:"label"`
	Model       *string `json:"model"`
}

type FlowNorm struct {
	P50   *float64 `json:"p50_min"`
	P90   *float64 `json:"p90_min"`
	Arion *float64 `json:"arion_min"`
}

type FlowStep struct {
	ID         string     `json:"id"`
	ItemID     string     `json:"item_id"`
	StepKey    string     `json:"step_key"`
	Round      int        `json:"round"`
	Kind       string     `json:"kind"`
	Actor      FlowActor  `json:"actor"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	Outcome    *string    `json:"outcome"`
	WaitReason *string    `json:"wait_reason"`
	WaitsFor   *string    `json:"waits_for"`
	Side       bool       `json:"side"`
	Source     string     `json:"source"`
	Norm       FlowNorm   `json:"norm"`
}

type FlowETA struct {
	P50At  *time.Time `json:"p50_at"`
	P90At  *time.Time `json:"p90_at"`
	Basis  string     `json:"basis"`
	Reason *string    `json:"reason"`
}

type FlowTarget struct {
	Minutes  int    `json:"minutes"`
	FromStep string `json:"from_step"`
	Source   string `json:"source"`
}

type FlowGate struct {
	PrincipalID *string `json:"principal_id"`
	What        string  `json:"what"`
}

type FlowItem struct {
	ID            string      `json:"id"`
	Kind          string      `json:"kind"`
	Ref           string      `json:"ref"`
	Title         string      `json:"title"`
	PRs           []int64     `json:"prs"`
	StartedAt     *time.Time  `json:"started_at"`
	EndedAt       *time.Time  `json:"ended_at"`
	PctDone       int         `json:"pct_done"`
	CurrentStepID *string     `json:"current_step_id"`
	ETA           FlowETA     `json:"eta"`
	Target        *FlowTarget `json:"target"`
	NextHumanGate *FlowGate   `json:"next_human_gate"`
}

type FlowIncident struct {
	ID              string     `json:"id"`
	ItemID          string     `json:"item_id"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	Severity        string     `json:"severity"`
	Summary         string     `json:"summary"`
	RecoveryStepIDs []string   `json:"recovery_step_ids"`
}

type DeliveryFlow struct {
	ProjectID string         `json:"project_id"`
	Now       time.Time      `json:"now"`
	At        time.Time      `json:"at"`
	From      time.Time      `json:"from"`
	To        time.Time      `json:"to"`
	Items     []FlowItem     `json:"items"`
	Steps     []FlowStep     `json:"steps"`
	Incidents []FlowIncident `json:"incidents"`
	Truncated bool           `json:"truncated"`
}

type DeliveryFlowRun struct {
	ProjectID string         `json:"project_id"`
	Now       time.Time      `json:"now"`
	At        time.Time      `json:"at"`
	Item      FlowItem       `json:"item"`
	Steps     []FlowStep     `json:"steps"`
	Incidents []FlowIncident `json:"incidents"`
	Truncated bool           `json:"truncated"`
}

var (
	flowStepKeys    = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "copy_gate", "pin_gate", "build", "review", "ci", "queue", "merge_round", "hold", "mitigation", "switch", "live_check"}
	flowStepKinds   = []string{"work", "wait", "rework", "recovery"}
	flowActorTypes  = []string{"person", "agent", "ci", "queue"}
	flowOutcomes    = []string{"ok", "changes", "red", "flaky", "degraded", "green"}
	flowWaitReasons = []string{"reviewer", "queue", "dependency", "human_gate", "rerun", "release_train"}
	// The critical path whose completion share is pct_done. A release is the
	// Arion release path a → l; a change is built, reviewed, checked and merged.
	releasePath = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}
	changePath  = []string{"build", "review", "ci", "queue"}
	// Arion's per-step targets where the plan names one (minutes).
	arionStepMinutes = map[string]float64{"ci": 8, "queue": 7, "review": 8}
)

const (
	flowHistoryDays    = 30
	flowHistoryMinimum = 3
	releaseTargetMin   = 24
)

// flowID derives a stable row id, so replays converge on the same rows and
// incidents can name recovery steps by their reporter key.
func flowID(parts ...string) string {
	h := sha256.Sum256([]byte("aeon-delivery-flow\x00" + strings.Join(parts, "\x00")))
	h[6] = (h[6] & 15) | 80
	h[8] = (h[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

func flowItemID(tid, project, kind, ref string) string {
	return flowID(tid, "item", project, kind, ref)
}

func flowStepID(tid, item, source, key string) string {
	return flowID(tid, "step", item, source, key)
}

func flowIncidentID(tid, item, source, key string) string {
	return flowID(tid, "incident", item, source, key)
}

// flowNormKey separates waits from work: "usual" for a wait is other waits.
type flowNormKey struct {
	Step string
	Wait bool
}

type flowNormStat struct {
	N        int
	P50, P90 float64
}

// flowHistory is p50/p90 of finished steps per step_key over the last 30 days.
type flowHistory map[flowNormKey]flowNormStat

func minutesBetween(a, b time.Time) float64 {
	return math.Round(b.Sub(a).Minutes()*10) / 10
}

func flowNorm(h flowHistory, s FlowStep) FlowNorm {
	var n FlowNorm
	if stat, ok := h[flowNormKey{s.StepKey, s.Kind == "wait"}]; ok && stat.N >= flowHistoryMinimum {
		p50, p90 := stat.P50, stat.P90
		n.P50, n.P90 = &p50, &p90
	}
	if s.Kind != "wait" {
		if v, ok := arionStepMinutes[s.StepKey]; ok {
			n.Arion = &v
		}
	}
	return n
}

func flowPath(kind string) []string {
	if kind == "release" {
		return releasePath
	}
	return changePath
}

// flowItemRow is the stored item before the view is derived at a moment.
type flowItemRow struct {
	ID, Project, Kind, Ref, Title string
	Ticket                        *string
	PRs                           []int64
	Started, Ended                *time.Time
	Target                        *FlowTarget
	Gate                          *FlowGate
	OpsP50, OpsP90                *time.Time
}

// endedBy reports whether t is set and not after at.
func endedBy(t *time.Time, at time.Time) bool {
	return t != nil && !t.After(at)
}

// flowItemView derives progress, the current step and the ETA at a moment.
// steps are the item's steps; history excludes nothing, it only knows
// finished steps. A thin history yields basis "none" with the reason; it is
// never extrapolated from fewer than flowHistoryMinimum runs.
func flowItemView(row flowItemRow, steps []FlowStep, h flowHistory, at time.Time) FlowItem {
	out := FlowItem{ID: row.ID, Kind: row.Kind, Ref: row.Ref, Title: row.Title, PRs: row.PRs, StartedAt: row.Started, EndedAt: row.Ended, Target: row.Target}
	if out.PRs == nil {
		out.PRs = []int64{}
	}
	path := flowPath(row.Kind)
	done := map[string]bool{}
	open := map[string]FlowStep{}
	var current *FlowStep
	for i := range steps {
		s := steps[i]
		if s.StartedAt.After(at) {
			continue
		}
		if endedBy(s.EndedAt, at) {
			if s.Kind != "wait" && s.Outcome == nil || s.Outcome != nil && (*s.Outcome == "ok" || *s.Outcome == "green") {
				done[s.StepKey] = true
			}
			continue
		}
		if !s.Side {
			if current == nil || s.StartedAt.After(current.StartedAt) || s.StartedAt.Equal(current.StartedAt) && s.ID > current.ID {
				current = &steps[i]
			}
		}
		if s.Kind != "wait" {
			if prev, ok := open[s.StepKey]; !ok || s.StartedAt.Before(prev.StartedAt) {
				open[s.StepKey] = s
			}
		}
		if s.Kind == "wait" && s.WaitReason != nil && *s.WaitReason == "human_gate" && row.Gate == nil && !endedBy(row.Ended, at) {
			out.NextHumanGate = &FlowGate{PrincipalID: s.WaitsFor, What: s.StepKey}
		}
	}
	finished := endedBy(row.Ended, at)
	if current != nil && !finished {
		id := current.ID
		out.CurrentStepID = &id
	}
	if row.Gate != nil && !finished {
		g := *row.Gate
		out.NextHumanGate = &g
	}
	count := 0
	for _, k := range path {
		if done[k] {
			count++
		}
	}
	out.PctDone = int(math.Round(100 * float64(count) / float64(len(path))))
	if finished {
		out.PctDone = 100
		out.NextHumanGate = nil
		reason := "The run has finished."
		out.ETA = FlowETA{Basis: "none", Reason: &reason}
		return out
	}
	if row.OpsP50 != nil || row.OpsP90 != nil {
		out.ETA = FlowETA{P50At: row.OpsP50, P90At: row.OpsP90, Basis: "ops"}
		return out
	}
	var p50, p90 float64
	var thin []string
	for _, k := range path {
		if done[k] {
			continue
		}
		stat := h[flowNormKey{k, false}]
		if stat.N < flowHistoryMinimum {
			thin = append(thin, fmt.Sprintf("%s (%d)", k, stat.N))
			continue
		}
		elapsed := 0.0
		if s, ok := open[k]; ok {
			elapsed = at.Sub(s.StartedAt).Minutes()
		}
		p50 += math.Max(stat.P50-elapsed, 0)
		p90 += math.Max(stat.P90-elapsed, 0)
	}
	if len(thin) > 0 {
		reason := fmt.Sprintf("Too little history: fewer than %d finished runs in the last %d days for step %s.", flowHistoryMinimum, flowHistoryDays, strings.Join(thin, ", "))
		out.ETA = FlowETA{Basis: "none", Reason: &reason}
		return out
	}
	a := at.Add(time.Duration(p50 * float64(time.Minute))).UTC().Round(time.Second)
	b := at.Add(time.Duration(p90 * float64(time.Minute))).UTC().Round(time.Second)
	out.ETA = FlowETA{P50At: &a, P90At: &b, Basis: "history"}
	return out
}

func validFlowEnum(values []string, v string) bool { return slices.Contains(values, v) }

func sortedPRs(prs []int64) []int64 {
	out := append([]int64{}, prs...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return slices.Compact(out)
}
