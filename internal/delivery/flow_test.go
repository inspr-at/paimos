// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func flowStepAt(id, key, kind string, start time.Time, minutes float64, outcome *string) FlowStep {
	s := FlowStep{ID: id, ItemID: "item", StepKey: key, Round: 1, Kind: kind, Actor: FlowActor{Type: "agent", Label: "Builder"}, StartedAt: start, Outcome: outcome, Source: "paimos"}
	if minutes >= 0 {
		end := start.Add(time.Duration(minutes * float64(time.Minute)))
		s.EndedAt = &end
	}
	return s
}

func TestDeliveryFlowETAFromHistoryOrNoneWithReason(t *testing.T) {
	// Risk: an ETA is extrapolated from too little history, counts a step
	// that is already done, or ignores how long the open step has run.
	at := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	row := flowItemRow{ID: "item", Kind: "change", Ref: "AEON-1", Title: "Change"}
	steps := []FlowStep{
		flowStepAt("s1", "build", "work", at.Add(-50*time.Minute), 30, nil),
		flowStepAt("s2", "review", "wait", at.Add(-20*time.Minute), 5, nil),
		flowStepAt("s3", "review", "work", at.Add(-15*time.Minute), -1, nil),
	}
	full := flowHistory{
		{"build", false}:  {N: 5, P50: 30, P90: 40},
		{"review", false}: {N: 4, P50: 10, P90: 20},
		{"ci", false}:     {N: 9, P50: 8, P90: 12},
		{"queue", false}:  {N: 3, P50: 7, P90: 9},
	}
	v := flowItemView(row, steps, full, at)
	if v.ETA.Basis != "history" || v.ETA.Reason != nil || v.ETA.P50At == nil || v.ETA.P90At == nil {
		t.Fatalf("eta: %+v", v.ETA)
	}
	// Review has run 15 of its usual 10 (p50) and 20 (p90) minutes; build is
	// done. p50: 0 + 8 + 7; p90: 5 + 12 + 9.
	if !v.ETA.P50At.Equal(at.Add(15*time.Minute)) || !v.ETA.P90At.Equal(at.Add(26*time.Minute)) {
		t.Fatalf("eta p50 %s p90 %s", v.ETA.P50At, v.ETA.P90At)
	}
	if v.PctDone != 25 || v.CurrentStepID == nil || *v.CurrentStepID != "s3" {
		t.Fatalf("progress %d current %v", v.PctDone, v.CurrentStepID)
	}

	thin := flowHistory{{"build", false}: {N: 5, P50: 30, P90: 40}, {"review", false}: {N: 2, P50: 10, P90: 20}, {"ci", false}: {N: 9, P50: 8, P90: 12}}
	v = flowItemView(row, steps, thin, at)
	if v.ETA.Basis != "none" || v.ETA.P50At != nil || v.ETA.P90At != nil || v.ETA.Reason == nil ||
		!strings.Contains(*v.ETA.Reason, "review (2)") || !strings.Contains(*v.ETA.Reason, "queue (0)") || strings.Contains(*v.ETA.Reason, "build") {
		t.Fatalf("thin eta: %+v %v", v.ETA, v.ETA.Reason)
	}

	// An OPS estimate wins; a finished run has none and is 100 % done.
	p50, p90 := at.Add(time.Hour), at.Add(2*time.Hour)
	ops := row
	ops.OpsP50, ops.OpsP90 = &p50, &p90
	if v = flowItemView(ops, steps, thin, at); v.ETA.Basis != "ops" || !v.ETA.P50At.Equal(p50) {
		t.Fatalf("ops eta: %+v", v.ETA)
	}
	done := row
	ended := at.Add(-time.Minute)
	done.Ended = &ended
	done.Gate = &FlowGate{What: "agm1 GO"}
	if v = flowItemView(done, steps, full, at); v.ETA.Basis != "none" || v.ETA.Reason == nil || v.PctDone != 100 || v.CurrentStepID != nil || v.NextHumanGate != nil {
		t.Fatalf("finished: %+v", v)
	}
	// Before the run ended, the same moment in Replay still shows it open.
	if v = flowItemView(done, steps, full, ended.Add(-time.Minute)); v.PctDone == 100 || v.NextHumanGate == nil || v.NextHumanGate.What != "agm1 GO" {
		t.Fatalf("replayed moment: %+v", v)
	}
}

func TestDeliveryFlowNormsSeparateWaitsAndNeedHistory(t *testing.T) {
	h := flowHistory{{"review", true}: {N: 3, P50: 4, P90: 9}, {"review", false}: {N: 2, P50: 11, P90: 30}}
	wait := flowNorm(h, FlowStep{StepKey: "review", Kind: "wait"})
	work := flowNorm(h, FlowStep{StepKey: "review", Kind: "work"})
	if wait.P50 == nil || *wait.P50 != 4 || wait.Arion != nil {
		t.Fatalf("wait norm: %+v", wait)
	}
	if work.P50 != nil || work.P90 != nil || work.Arion == nil || *work.Arion != 8 {
		t.Fatalf("work norm below the history minimum: %+v", work)
	}
}

func TestDeliveryFlowRunsMapToChecksQueueAndFlakes(t *testing.T) {
	// Risk: a re-run that passed hides that the first attempt was a flake, or a
	// merge-queue wait is drawn as work.
	t0 := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	pr := int64(7)
	head := strings.Repeat("d", 40)
	runs := []metricRun{
		{ID: 501, Attempt: 2, Event: "pull_request", Head: head, PR: &pr, Created: t0.Add(20 * time.Minute), Started: t0.Add(21 * time.Minute), Completed: t0.Add(31 * time.Minute), Conclusion: "success"},
		{ID: 501, Attempt: 1, Event: "pull_request", Head: head, PR: &pr, Created: t0, Started: t0.Add(time.Minute), Completed: t0.Add(11 * time.Minute), Conclusion: "failure"},
		{ID: 600, Attempt: 1, Event: "merge_group", Head: strings.Repeat("e", 40), PR: &pr, Created: t0.Add(40 * time.Minute), Started: t0.Add(44 * time.Minute), Completed: t0.Add(52 * time.Minute), Conclusion: "success"},
		{ID: 700, Attempt: 1, Event: "pull_request", Head: head, PR: &pr, Created: t0, Started: t0, Completed: t0.Add(time.Minute), Conclusion: "cancelled"},
	}
	got := map[string]flowStepInput{}
	for _, s := range flowStepsFromRuns(runs) {
		got[s.Key] = s
	}
	want := map[string]string{
		"run/501/1":        "ci work 1 flaky",
		"run/501/2/rerun":  "ci wait 2 -",
		"run/501/2":        "ci rework 2 green",
		"run/600/1/queued": "queue wait 1 -",
		"run/600/1":        "queue work 1 green",
		"run/700/1":        "ci work 1 -",
	}
	if len(got) != len(want) {
		t.Fatalf("steps: %+v", got)
	}
	for key, w := range want {
		s, ok := got[key]
		outcome := "-"
		if ok && s.Outcome != nil {
			outcome = *s.Outcome
		}
		if !ok || strings.Join([]string{s.StepKey, s.Kind, itoaInt(s.Round), outcome}, " ") != w || s.Source != "github_app" || s.Ended == nil {
			t.Fatalf("%s: %+v, want %s", key, s, w)
		}
	}
	if q := got["run/600/1/queued"]; q.WaitReason == nil || *q.WaitReason != "queue" || !q.Started.Equal(t0.Add(40*time.Minute)) || !q.Ended.Equal(t0.Add(44*time.Minute)) {
		t.Fatalf("queue wait: %+v", q)
	}
	if r := got["run/501/2/rerun"]; r.WaitReason == nil || *r.WaitReason != "rerun" || !r.Started.Equal(t0.Add(11*time.Minute)) {
		t.Fatalf("rerun wait: %+v", r)
	}
}

func itoaInt(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestDeliveryFlowRolloutValidation(t *testing.T) {
	// Risk: a report stores a credential-like label for every member to read,
	// accepts a step the page cannot draw, or a recovery step that is not in
	// the record.
	now := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	base := func() rolloutInput {
		step, _ := json.Marshal(map[string]any{"key": "a-1", "step": "a", "kind": "work", "actor": map[string]any{"type": "ci", "principal_id": nil, "label": "Checks", "model": nil}, "started_at": now.Add(-time.Hour)})
		return rolloutInput{Schema: "aeon.rollout.v1", Release: "126", Steps: []json.RawMessage{step}}
	}
	if _, err := parseRollout(base(), "p", now); err != nil {
		t.Fatalf("valid rollout: %v", err)
	}
	for name, mutate := range map[string]func(*rolloutInput){
		"schema":      func(in *rolloutInput) { in.Schema = "aeon.rollout.v2" },
		"rollback":    func(in *rolloutInput) { in.Direction = "rollback" },
		"release":     func(in *rolloutInput) { in.Release = "126; drop" },
		"outcome":     func(in *rolloutInput) { in.Outcome = "maybe" },
		"credential":  func(in *rolloutInput) { title := "Release ghp_" + strings.Repeat("A", 36); in.Title = &title },
		"future":      func(in *rolloutInput) { cut := now.Add(time.Hour); in.CutAt = &cut },
		"unknown key": func(in *rolloutInput) { in.Steps[0] = json.RawMessage(strings.Replace(string(in.Steps[0]), `"key"`, `"secret":"x","key"`, 1)) },
		"bad step":    func(in *rolloutInput) { in.Steps[0] = json.RawMessage(strings.Replace(string(in.Steps[0]), `"step":"a"`, `"step":"z"`, 1)) },
		"wait reason": func(in *rolloutInput) {
			in.Steps[0] = json.RawMessage(strings.Replace(string(in.Steps[0]), `"kind":"work"`, `"kind":"work","wait_reason":"queue"`, 1))
		},
		"recovery": func(in *rolloutInput) {
			inc, _ := json.Marshal(map[string]any{"key": "i1", "started_at": now.Add(-time.Hour), "severity": "degraded", "summary": "Live check degraded", "recovery_steps": []string{"k-9"}})
			in.Incidents = []json.RawMessage{inc}
		},
	} {
		in := base()
		mutate(&in)
		if _, err := parseRollout(in, "p", now); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
