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

	// An OPS estimate wins. A finished run has none and is 100 % done, and an
	// explicit human gate stays until a later report omits it.
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
	if v = flowItemView(done, steps, full, at); v.ETA.Basis != "none" || v.ETA.Reason == nil || v.ETA.P50At != nil || v.PctDone != 100 || v.CurrentStepID != nil || v.NextHumanGate == nil || v.NextHumanGate.What != "agm1 GO" {
		t.Fatalf("finished: %+v", v)
	}
	// Before the run ended, the same moment in Replay still shows it open.
	if v = flowItemView(done, steps, full, ended.Add(-time.Minute)); v.PctDone == 100 || v.NextHumanGate == nil || v.NextHumanGate.What != "agm1 GO" {
		t.Fatalf("replayed moment: %+v", v)
	}
}

func TestDeliveryFlowReworkReopensCompletedPhase(t *testing.T) {
	// Risk: an earlier successful build stays done while a fix is still
	// running, so progress and the ETA skip that active build. A side check
	// must not complete the phase it sits beside.
	at := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	row := flowItemRow{ID: "item", Kind: "change", Ref: "AEON-1", Title: "Change"}
	ok, changes, green := flowStr("ok"), flowStr("changes"), flowStr("green")
	build := flowStepAt("b1", "build", "work", at.Add(-40*time.Minute), 20, ok)
	review := flowStepAt("r1", "review", "work", at.Add(-20*time.Minute), 10, changes)
	fix := flowStepAt("b2", "build", "rework", at.Add(-5*time.Minute), -1, nil)
	side := flowStepAt("c1", "ci", "work", at.Add(-4*time.Minute), 1, green)
	side.Side = true
	full := flowHistory{
		{"build", false}:  {N: 5, P50: 30, P90: 40},
		{"review", false}: {N: 4, P50: 10, P90: 20},
		{"ci", false}:     {N: 9, P50: 8, P90: 12},
		{"queue", false}:  {N: 3, P50: 7, P90: 9},
	}
	v := flowItemView(row, []FlowStep{build, review, fix, side}, full, at)
	// The fix has run 5 of build's usual 30. Review asked for changes, so it
	// is not done. The side check does not finish ci. 25+10+8+7 and 35+20+12+9.
	if v.PctDone != 0 || v.CurrentStepID == nil || *v.CurrentStepID != "b2" || v.ETA.Basis != "history" || v.ETA.P50At == nil || v.ETA.P90At == nil ||
		!v.ETA.P50At.Equal(at.Add(50*time.Minute)) || !v.ETA.P90At.Equal(at.Add(76*time.Minute)) {
		t.Fatalf("rework: pct %d current %v eta %+v", v.PctDone, v.CurrentStepID, v.ETA)
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

func TestDeliveryFlowLiveRunsStayOpenUntilCompletion(t *testing.T) {
	// Risk: a queued or running check is stored already ended, a merge-queue
	// run is drawn as work before it starts, or one failed attempt is flaky
	// before a later attempt passes.
	t0 := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	started := t0.Add(time.Minute)
	ci := flowLiveRun{ID: 501, Attempt: 1, Event: "pull_request", Created: t0, Started: t0, Phase: "queued"}
	queued := flowLiveSteps(ci)
	if len(queued) != 1 || queued[0].Key != "run/501/1" || queued[0].StepKey != "ci" || queued[0].Ended != nil || !queued[0].Progress || !queued[0].Started.Equal(t0) {
		t.Fatalf("queued check: %+v", queued)
	}
	ci.Phase, ci.Started = "running", started
	running := flowLiveSteps(ci)
	if len(running) != 1 || running[0].Ended != nil || !running[0].Progress || !running[0].Started.Equal(started) {
		t.Fatalf("running check: %+v", running)
	}
	ci.Phase, ci.Completed, ci.Conclusion = "completed", t0.Add(11*time.Minute), "failure"
	failed := flowLiveSteps(ci)
	if len(failed) != 1 || failed[0].Ended == nil || failed[0].Progress || failed[0].Outcome == nil || *failed[0].Outcome != "red" {
		t.Fatalf("failed check: %+v", failed)
	}
	ci.Attempt, ci.Conclusion = 2, "success"
	passed := flowLiveSteps(ci)
	if len(passed) != 1 || passed[0].Kind != "rework" || passed[0].Outcome == nil || *passed[0].Outcome != "green" || passed[0].Ended == nil {
		t.Fatalf("passed rework: %+v", passed)
	}

	queue := flowLiveRun{ID: 600, Attempt: 1, Event: "merge_group", Created: t0, Started: t0, Phase: "queued"}
	waiting := flowLiveSteps(queue)
	if len(waiting) != 1 || waiting[0].Key != "run/600/1/queued" || waiting[0].Kind != "wait" || waiting[0].Ended != nil || !waiting[0].Progress || waiting[0].WaitReason == nil || *waiting[0].WaitReason != "queue" {
		t.Fatalf("queued merge group: %+v", waiting)
	}
	queue.Phase, queue.Started = "running", started
	active := flowLiveSteps(queue)
	if len(active) != 2 || active[0].Key != "run/600/1/queued" || active[0].Ended == nil || !active[0].Ended.Equal(started) || active[1].Key != "run/600/1" || active[1].StepKey != "queue" || active[1].Ended != nil || !active[1].Progress {
		t.Fatalf("running merge group: %+v", active)
	}
	queue.Phase, queue.Completed, queue.Conclusion = "completed", t0.Add(12*time.Minute), "success"
	done := flowLiveSteps(queue)
	got := map[string]flowStepInput{}
	for _, s := range done {
		got[s.Key] = s
	}
	if len(got) != 2 || got["run/600/1/queued"].Ended == nil || got["run/600/1"].Ended == nil || got["run/600/1"].Outcome == nil || *got["run/600/1"].Outcome != "green" || got["run/600/1"].Progress {
		t.Fatalf("completed merge group: %+v", got)
	}
}

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
		"schema":     func(in *rolloutInput) { in.Schema = "aeon.rollout.v2" },
		"rollback":   func(in *rolloutInput) { in.Direction = "rollback" },
		"release":    func(in *rolloutInput) { in.Release = "126; drop" },
		"outcome":    func(in *rolloutInput) { in.Outcome = "maybe" },
		"credential": func(in *rolloutInput) { title := "Release ghp_" + strings.Repeat("A", 36); in.Title = &title },
		"future":     func(in *rolloutInput) { cut := now.Add(time.Hour); in.CutAt = &cut },
		"unknown key": func(in *rolloutInput) {
			in.Steps[0] = json.RawMessage(strings.Replace(string(in.Steps[0]), `"key"`, `"secret":"x","key"`, 1))
		},
		"bad step": func(in *rolloutInput) {
			in.Steps[0] = json.RawMessage(strings.Replace(string(in.Steps[0]), `"step":"a"`, `"step":"z"`, 1))
		},
		"wait reason": func(in *rolloutInput) {
			in.Steps[0] = json.RawMessage(strings.Replace(string(in.Steps[0]), `"kind":"work"`, `"kind":"work","wait_reason":"queue"`, 1))
		},
		"recovery": func(in *rolloutInput) {
			inc, _ := json.Marshal(map[string]any{"key": "i1", "started_at": now.Add(-time.Hour), "severity": "degraded", "summary": "Live check degraded", "recovery_steps": []string{"k-9"}})
			in.Incidents = []json.RawMessage{inc}
		},
		"evidence url": func(in *rolloutInput) {
			in.Qualification = &rolloutQualification{Evidence: ptr("https://example.com/evidence")}
		},
		"evidence script": func(in *rolloutInput) { in.Qualification = &rolloutQualification{Evidence: ptr("javascript:alert(1)")} },
		"evidence empty":  func(in *rolloutInput) { in.Qualification = &rolloutQualification{Evidence: ptr("")} },
		"evidence too long": func(in *rolloutInput) {
			in.Qualification = &rolloutQualification{Evidence: ptr(strings.Repeat("a", 201))}
		},
		"evidence credential": func(in *rolloutInput) {
			in.Qualification = &rolloutQualification{Evidence: ptr("AEON-487/ghp_" + strings.Repeat("A", 36))}
		},
		"rollback hyphen":  func(in *rolloutInput) { in.RollbackClass = ptr("restore-required") },
		"rollback unknown": func(in *rolloutInput) { in.RollbackClass = ptr("safe") },
		"rollback empty":   func(in *rolloutInput) { in.RollbackClass = ptr("") },
	} {
		in := base()
		mutate(&in)
		if _, err := parseRollout(in, "p", now); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestDeliveryFlowRolloutAcceptsCatalogueRehearsalAndReleaseFacts(t *testing.T) {
	// Risk: the contract names the new steps and facts but the parser refuses
	// them, keeps more of the qualification object than the reference, or maps a
	// fact onto the wrong field.
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	step := func(key, step string, side bool) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"key": key, "step": step, "kind": "work", "side": side, "actor": map[string]any{"type": "ci", "principal_id": nil, "label": "Checks", "model": nil}, "started_at": now.Add(-time.Hour), "ended_at": now.Add(-30 * time.Minute)})
		return raw
	}
	var in rolloutInput
	raw := `{"schema":"aeon.rollout.v1","release":"127","qualification":{"version":"261009063244.0.0","asset":"paimos-agentd-darwin-arm64","touch_id":true,"evidence":"AEON-487/comment/native-qualification"},"rollback_class":"restore_required"}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	in.Steps = []json.RawMessage{step("rehearsal", "rehearsal", true), step("catalogue", "catalogue", false)}
	batch, err := parseRollout(in, "p", now)
	if err != nil {
		t.Fatalf("record refused: %v", err)
	}
	if got := []string{batch.Steps[0].StepKey, batch.Steps[1].StepKey}; got[0] != "rehearsal" || got[1] != "catalogue" || !batch.Steps[0].Side || batch.Steps[1].Side {
		t.Fatalf("steps: %+v", batch.Steps)
	}
	if batch.Item.QualificationEvidence == nil || *batch.Item.QualificationEvidence != "AEON-487/comment/native-qualification" || batch.Item.RollbackClass == nil || *batch.Item.RollbackClass != "restore_required" {
		t.Fatalf("facts: %+v %+v", batch.Item.QualificationEvidence, batch.Item.RollbackClass)
	}
	for _, class := range flowRollbackClasses {
		in.RollbackClass = &class
		if _, err := parseRollout(in, "p", now); err != nil {
			t.Fatalf("class %s: %v", class, err)
		}
	}
	// Nothing reported, nothing recorded: no default is invented.
	in.Qualification, in.RollbackClass = nil, nil
	batch, err = parseRollout(in, "p", now)
	if err != nil || batch.Item.QualificationEvidence != nil || batch.Item.RollbackClass != nil {
		t.Fatalf("empty record: %+v %v", batch.Item, err)
	}
}
