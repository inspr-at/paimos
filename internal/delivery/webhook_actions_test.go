// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Documented GitHub webhook actions for the events this installation
// subscribes to (payload docs, 2026-10-19). Ignored actions are acknowledged
// and stored. Handled actions keep their current processing.
var (
	documentedPullActions = []string{
		"assigned", "auto_merge_disabled", "auto_merge_enabled", "closed",
		"converted_to_draft", "demilestoned", "dequeued", "edited", "enqueued",
		"labeled", "locked", "milestoned", "opened", "ready_for_review",
		"reopened", "review_request_removed", "review_requested", "stacked",
		"synchronize", "unassigned", "unlabeled", "unlocked",
	}
	handledPullActions    = []string{"closed", "dequeued", "edited", "enqueued", "opened", "reopened", "synchronize"}
	documentedCheckRun    = []string{"completed", "created", "requested_action", "rerequested"}
	documentedCheckSuite  = []string{"completed", "requested", "rerequested"}
	handledMergeActions   = []string{"checks_requested", "destroyed"}
	handledWorkflowAction = []string{"completed", "in_progress", "requested"}
)

type webhookEffect struct {
	items, github, events, audits, runs, pulls, marks, flowItems, flowSteps, receipts int
	metricAt                                                                          *time.Time
}

func TestDocumentedWebhookActions(t *testing.T) {
	// Risk: a documented action is refused, an unused action changes delivery
	// state, or a handled action is stored as ignored.
	assertActionSet(t, "pull_request", documentedPullActions)
	assertActionSet(t, "check_run", documentedCheckRun)
	assertActionSet(t, "check_suite", documentedCheckSuite)
	assertActionSet(t, "merge_group", handledMergeActions)
	assertActionSet(t, "workflow_run", handledWorkflowAction)
	if !validAction("push", "") || validAction("push", "opened") || !validAction("status", "") || validAction("issues", "opened") || validAction("pull_request", "deleted") || validAction("workflow_run", "deleted") {
		t.Fatal("push/status acknowledgement or unknown-action refusal drifted")
	}

	f := newFixture(t)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,$3,'ci.yml','nightly.yml',$4)`, f.person.TenantID, f.project, f.m.config.Repository, f.at)
		return err
	})
	head, base := strings.Repeat("b", 40), strings.Repeat("a", 40)

	for _, action := range ignoredOf(documentedPullActions, handledPullActions) {
		t.Run("ignore/pull_request/"+action, func(t *testing.T) {
			before, fanouts := f.webhookEffect(t), f.fanouts
			auditSend(t, f, "pull_request", actionID("ign", "pull", action), pullActionBody(f.m.config.Repository, action, head, base), 204)
			assertIgnored(t, f, actionID("ign", "pull", action), "pull_request", action, before, fanouts)
		})
	}
	for _, action := range ignoredOf(documentedCheckRun, []string{"completed"}) {
		t.Run("ignore/check_run/"+action, func(t *testing.T) {
			before, fanouts := f.webhookEffect(t), f.fanouts
			auditSend(t, f, "check_run", actionID("ign", "run", action), checkActionBody(action, "check_run", head), 204)
			assertIgnored(t, f, actionID("ign", "run", action), "check_run", action, before, fanouts)
		})
	}
	for _, action := range ignoredOf(documentedCheckSuite, []string{"completed"}) {
		t.Run("ignore/check_suite/"+action, func(t *testing.T) {
			before, fanouts := f.webhookEffect(t), f.fanouts
			auditSend(t, f, "check_suite", actionID("ign", "suite", action), checkActionBody(action, "check_suite", head), 204)
			assertIgnored(t, f, actionID("ign", "suite", action), "check_suite", action, before, fanouts)
		})
	}

	t.Run("redelivery", func(t *testing.T) {
		body := pullActionBody(f.m.config.Repository, "labeled", head, base)
		id := "ign-redelivery"
		before, fanouts := f.webhookEffect(t), f.fanouts
		auditSend(t, f, "pull_request", id, body, 204)
		auditSend(t, f, "pull_request", id, body, 204)
		assertIgnored(t, f, id, "pull_request", "labeled", before, fanouts)
		f.tx(t, func(tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_webhook_receipts WHERE delivery_id=$1`, id).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Fatalf("redelivery stored %d receipts", n)
			}
			return nil
		})
	})

	t.Run("refusals", func(t *testing.T) {
		before, fanouts := f.webhookEffect(t), f.fanouts
		auditSend(t, f, "pull_request", "refuse-unknown-action", pullActionBody(f.m.config.Repository, "deleted", head, base), 400)
		auditSend(t, f, "workflow_run", "refuse-unknown-workflow", map[string]any{"action": "deleted", "workflow_run": map[string]any{}}, 400)
		sendWebhook(t, f, "issues", "refuse-unknown-event", signedJSON(t, f, map[string]any{"action": "opened"}), "", 400, true)
		created := checkActionBody("created", "check_run", head)
		sendWebhook(t, f, "check_run", "refuse-bad-signature", signedJSON(t, f, created), "sha256="+strings.Repeat("ab", 32), 401, false)
		sendWebhook(t, f, "check_run", "refuse-missing-signature", signedJSON(t, f, created), "", 401, false)
		sendWebhook(t, f, "check_run", "refuse-malformed", []byte(`{"action":"created"`), "", 400, true)
		badHead := checkActionBody("created", "check_run", "nope")
		auditSend(t, f, "check_run", "refuse-bad-subject", badHead, 400)
		wrongInstall := checkActionBody("created", "check_run", head)
		wrongInstall["installation"] = map[string]any{"id": 999}
		wrongInstall["repository"] = map[string]any{"full_name": f.m.config.Repository, "default_branch": "main"}
		sendWebhook(t, f, "check_run", "refuse-installation", mustJSON(t, wrongInstall), "", 404, true)
		wrongRepo := checkActionBody("created", "check_run", head)
		wrongRepo["installation"] = map[string]any{"id": 456}
		wrongRepo["repository"] = map[string]any{"full_name": "example/other", "default_branch": "main"}
		sendWebhook(t, f, "check_run", "refuse-repository", mustJSON(t, wrongRepo), "", 404, true)
		assertUntouched(t, f, before, fanouts, nil)
	})

	f.gh.pulls[7] = f.pull()
	opened := f.webhookEffect(t)
	for _, action := range handledPullActions {
		t.Run("handled/pull_request/"+action, func(t *testing.T) {
			auditSend(t, f, "pull_request", actionID("ok", "pull", action), pullActionBody(f.m.config.Repository, action, head, base), 204)
			assertHandled(t, f, actionID("ok", "pull", action))
		})
	}
	if got := f.webhookEffect(t); got.items != opened.items+1 {
		t.Fatalf("opened pull did not project an item: %d → %d", opened.items, got.items)
	}
	for _, tc := range []struct{ event, action, object string }{
		{"check_run", "completed", "check_run"},
		{"check_suite", "completed", "check_suite"},
	} {
		t.Run("handled/"+tc.event, func(t *testing.T) {
			auditSend(t, f, tc.event, actionID("ok", tc.object, tc.action), checkActionBody(tc.action, tc.object, head), 204)
			assertHandled(t, f, actionID("ok", tc.object, tc.action))
		})
	}
	for _, action := range handledMergeActions {
		t.Run("handled/merge_group/"+action, func(t *testing.T) {
			auditSend(t, f, "merge_group", actionID("ok", "group", action), map[string]any{
				"action":      action,
				"merge_group": map[string]any{"head_sha": strings.Repeat("c", 40), "base_sha": base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-7-group"},
			}, 204)
			assertHandled(t, f, actionID("ok", "group", action))
		})
	}
	created := f.at.Add(-time.Minute)
	for i, action := range handledWorkflowAction {
		t.Run("handled/workflow_run/"+action, func(t *testing.T) {
			var conclusion any
			if action == "completed" {
				conclusion = "success"
			}
			auditSend(t, f, "workflow_run", actionID("ok", "flow", action), workflowRunBody(action, int64(9100+i), 1, "push", "main", strings.Repeat("d", 40), ".github/workflows/ci.yml", created, nil, conclusion, 0), 204)
			assertHandled(t, f, actionID("ok", "flow", action))
		})
	}
	t.Run("handled/push", func(t *testing.T) {
		auditSend(t, f, "push", "ok-push", map[string]any{"ref": "refs/heads/topic", "after": strings.Repeat("e", 40)}, 204)
		assertHandled(t, f, "ok-push")
	})
	t.Run("handled/status", func(t *testing.T) {
		auditSend(t, f, "status", "ok-status", map[string]any{"sha": head, "state": "success", "context": "ci/test"}, 204)
		assertHandled(t, f, "ok-status")
	})
	if f.webhookEffect(t).metricAt == nil {
		t.Fatal("handled delivery did not wake the metric source")
	}
}

func assertActionSet(t *testing.T, name string, want []string) {
	t.Helper()
	for _, action := range want {
		if !validAction(name, action) {
			t.Fatalf("%s %s is not acknowledged", name, action)
		}
	}
}

func ignoredOf(all, handled []string) []string {
	out := []string{}
	for _, action := range all {
		if !slices.Contains(handled, action) {
			out = append(out, action)
		}
	}
	return out
}

func actionID(prefix, kind, action string) string {
	return strings.ReplaceAll(prefix+"-"+kind+"-"+action, "_", "-")
}

func pullActionBody(repo, action, head, base string) map[string]any {
	return map[string]any{"action": action, "pull_request": map[string]any{
		"number": 7,
		"head":   map[string]any{"sha": head},
		"base":   map[string]any{"sha": base, "repo": map[string]any{"full_name": repo}},
	}}
}

func checkActionBody(action, object, head string) map[string]any {
	return map[string]any{"action": action, object: map[string]any{
		"head_sha":      head,
		"pull_requests": []any{map[string]any{"number": 7}},
	}}
}

func mustJSON(t *testing.T, body map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func signedJSON(t *testing.T, f *fixture, body map[string]any) []byte {
	t.Helper()
	raw := mustJSON(t, body)
	return raw
}

func sendWebhook(t *testing.T, f *fixture, event, id string, raw []byte, signature string, want int, sign bool) {
	t.Helper()
	if sign {
		signature = signed(raw, f.m.secret)
	}
	r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
	if signature != "" {
		r.Header.Set("X-Hub-Signature-256", signature)
	}
	r.Header.Set("X-GitHub-Delivery", id)
	r.Header.Set("X-GitHub-Event", event)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("webhook %s/%s: %d != %d", event, id, w.Code, want)
	}
}

func (f *fixture) webhookEffect(t *testing.T) webhookEffect {
	t.Helper()
	var e webhookEffect
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT
			(SELECT count(*) FROM delivery_items),
			(SELECT count(*) FROM delivery_github_events),
			(SELECT count(*) FROM events),
			(SELECT count(*) FROM delivery_merge_audit),
			(SELECT count(*) FROM delivery_metric_runs),
			(SELECT count(*) FROM delivery_metric_pulls),
			(SELECT count(*) FROM delivery_metric_marks),
			(SELECT count(*) FROM delivery_flow_items),
			(SELECT count(*) FROM delivery_flow_steps),
			(SELECT count(*) FROM delivery_webhook_receipts),
			(SELECT last_event_at FROM delivery_metric_sources WHERE project_id=$1)`, f.project).Scan(
			&e.items, &e.github, &e.events, &e.audits, &e.runs, &e.pulls, &e.marks, &e.flowItems, &e.flowSteps, &e.receipts, &e.metricAt)
	})
	return e
}

func assertIgnored(t *testing.T, f *fixture, id, event, action string, before webhookEffect, fanouts int) {
	t.Helper()
	after := f.webhookEffect(t)
	if after.receipts != before.receipts+1 {
		t.Fatalf("%s receipts %d, want %d", id, after.receipts, before.receipts+1)
	}
	after.receipts = before.receipts
	assertUntouched(t, f, before, fanouts, &after)
	attempts, _, finished, points, step, class := receiptState(t, f, id)
	if attempts != 0 || finished == nil || len(points) != 1 || points["ignored"] != "ignored" || points["projection"] != "" || step != nil || class != nil {
		t.Fatalf("%s ignored receipt: attempts %d finished %v points %v step %v class %v", id, attempts, finished != nil, points, step, class)
	}
	var gotEvent, gotAction string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT event, action FROM delivery_webhook_receipts WHERE delivery_id=$1`, id).Scan(&gotEvent, &gotAction)
	})
	if gotEvent != event || gotAction != action {
		t.Fatalf("%s stored %s/%s", id, gotEvent, gotAction)
	}
}

func assertUntouched(t *testing.T, f *fixture, before webhookEffect, fanouts int, after *webhookEffect) {
	t.Helper()
	if after == nil {
		got := f.webhookEffect(t)
		after = &got
	}
	same := after.items == before.items && after.github == before.github && after.events == before.events && after.audits == before.audits && after.runs == before.runs && after.pulls == before.pulls && after.marks == before.marks && after.flowItems == before.flowItems && after.flowSteps == before.flowSteps && after.receipts == before.receipts && f.fanouts == fanouts && sameTime(before.metricAt, after.metricAt)
	if !same {
		t.Fatalf("state changed: before %+v after %+v fanouts %d want %d", before, *after, f.fanouts, fanouts)
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func assertHandled(t *testing.T, f *fixture, id string) {
	t.Helper()
	attempts, _, finished, points, step, class := receiptState(t, f, id)
	if attempts != 1 || finished == nil || points["projection"] == "" || points["ignored"] != "" || step != nil || class != nil {
		t.Fatalf("%s handled receipt: attempts %d finished %v points %v step %v class %v", id, attempts, finished != nil, points, step, class)
	}
}
