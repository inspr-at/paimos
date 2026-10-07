// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type quarantineGitHub struct {
	*fakeGitHub
	workflow string
	runs     []queueRun
	fail     bool
}

func (g *quarantineGitHub) QueueChecks(_ context.Context, _ queueSuite, run *queueRun) (string, []queueRun, error) {
	if g.fail {
		return "", nil, errRead
	}
	if run != nil {
		return g.workflow, []queueRun{*run}, nil
	}
	return g.workflow, g.runs, nil
}

func queueWebhook(t *testing.T, f *fixture, event, id string, check queueRun, want int) {
	t.Helper()
	body := map[string]any{"action": "completed", "installation": map[string]int{"id": 456}, "repository": map[string]string{"full_name": f.m.config.Repository}, "check_run": check, "check_suite": check.Suite}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
	r.Header.Set("X-Hub-Signature-256", signed(raw, f.m.secret))
	r.Header.Set("X-GitHub-Delivery", id)
	r.Header.Set("X-GitHub-Event", event)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("queue webhook %s/%s: %d != %d", event, id, w.Code, want)
	}
}

func TestQueueQuarantineBindsHeadClassifiesFailuresAndIsolates(t *testing.T) {
	// Risks: wrong-record quarantine, false enqueue success, duplicate actions,
	// and tenant/project leaks. One signed sequence covers the trust boundary;
	// injected time proves deadlines without latency or sleeps.
	f := newFixture(t)
	g := &quarantineGitHub{fakeGitHub: f.gh, workflow: "CI"}
	f.m.github = g
	p := f.pull()
	p.Queued, p.QueueHead = true, strings.Repeat("c", 40)
	f.gh.pulls[7] = p
	f.webhook(t, "pull_request", "enqueued", "quarantine-enqueued", 204)
	var settings Settings
	f.call(t, f.person, "GET", "/api/settings/delivery", nil, 200, &settings)
	if settings.RequiredWorkflow == nil || *settings.RequiredWorkflow != "CI" {
		t.Fatal("default required workflow is not CI")
	}
	f.call(t, f.person, "PUT", "/api/settings/delivery", map[string]any{"required_workflow": "Tenant CI", "required_checks": []string{"tenant-check"}}, 200, &settings)
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery-settings", map[string]any{"deadlines": map[string]int{"queue_failed": 17}}, 200, &settings)
	if *settings.RequiredWorkflow != "Tenant CI" || !reflect.DeepEqual(*settings.RequiredChecks, []string{"tenant-check"}) {
		t.Fatal("project omission did not inherit tenant settings")
	}
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery-settings", map[string]any{"required_workflow": "CI", "required_checks": []string{"web"}, "deadlines": map[string]int{"queue_failed": 17}}, 200, &settings)
	if *settings.RequiredWorkflow != "CI" || !reflect.DeepEqual(*settings.RequiredChecks, []string{"web"}) {
		t.Fatal("project override did not win")
	}
	path := func(head string) string {
		return "/api/delivery/enqueue-allowed?repository=example/delivery&pr=7&head=" + head
	}
	answer := func(head string, allowed bool, count int) EnqueueAllowed {
		t.Helper()
		var out EnqueueAllowed
		f.call(t, f.agent, "GET", path(head), nil, 200, &out)
		if out.Allowed != allowed || len(out.Failures) != count || !strings.Contains(out.Reason, head[:7]) {
			t.Fatalf("wrong enqueue answer: %+v", out)
		}
		return out
	}
	countFailures := func() int {
		t.Helper()
		var n int
		f.tx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_queue_failures`).Scan(&n)
		})
		return n
	}
	suite := queueSuite{ID: 10, Head: p.QueueHead, Branch: "gh-readonly-queue/main/pr-7-" + p.Head}
	suite.App.Slug = "github-actions"
	c := queueRun{ID: 1, Name: "web", Head: p.QueueHead, Status: "completed", Conclusion: "failure", Completed: &f.at, Suite: suite}
	c.App.Slug = "github-actions"
	answer(p.Head, true, 0)
	// A required-looking name cannot replace authoritative workflow identity.
	g.workflow = "Cross-family policy preview"
	queueWebhook(t, f, "check_run", "other-workflow", c, 204)
	c.ID++
	g.workflow = "CI"
	c.Name = "tenant-check"
	queueWebhook(t, f, "check_run", "non-required", c, 204)
	answer(p.Head, true, 0)
	if countFailures() != 0 {
		t.Fatal("ignored workflow/check was classified")
	}
	c.Name = "web"
	for _, conclusion := range []string{"cancelled", "timed_out", "startup_failure", "stale"} {
		c.ID++
		c.Conclusion = conclusion
		queueWebhook(t, f, "check_run", "infra-"+conclusion, c, 204)
	}
	out := answer(p.Head, true, 4)
	for _, failure := range out.Failures {
		if failure.Kind != "infra" {
			t.Fatal("infrastructure blamed on builder")
		}
	}
	if f.item(t, 7).State != InQueue {
		t.Fatal("infra failure quarantined the projection")
	}
	c.ID++
	c.Conclusion = "failure"
	f.at = f.at.Add(time.Minute)
	g.fail = true
	queueWebhook(t, f, "check_run", "required-failure", c, 502)
	answer(p.Head, true, 4)
	g.fail = false
	queueWebhook(t, f, "check_run", "required-failure", c, 204)
	i := f.item(t, 7)
	if i.State != QueueFailed || i.Owner != "builder" || !i.Since.Equal(f.at) || i.Deadline == nil || !i.Deadline.Equal(f.at.Add(17*time.Minute)) {
		t.Fatalf("queue failure lacks builder/deadline: %+v", i)
	}
	answer(p.Head, false, 5)
	queueWebhook(t, f, "check_run", "required-failure", c, 204)
	queueWebhook(t, f, "check_run", "same-run-new-delivery", c, 204)
	g.runs = []queueRun{c}
	queueWebhook(t, f, "check_suite", "same-run-suite", c, 204)
	if countFailures() != 5 || !reflect.DeepEqual(i, f.item(t, 7)) {
		t.Fatal("event/run replay duplicated or moved quarantine")
	}
	// Success on a rerun cannot lift a required-failure quarantine for the head.
	c.ID++
	c.Conclusion = "success"
	queueWebhook(t, f, "check_run", "same-head-success", c, 204)
	answer(p.Head, false, 5)
	p.Head, p.Queued, p.QueueHead = strings.Repeat("d", 40), false, ""
	f.gh.pulls[7] = p
	f.at = f.at.Add(time.Minute)
	f.webhook(t, "pull_request", "synchronize", "new-head", 204)
	newItem := f.item(t, 7)
	if newItem.State == QueueFailed || newItem.Observation.QueueFailure {
		t.Fatal("new PR head inherited old quarantine")
	}
	answer(p.Head, true, 0)
	c.ID++
	c.Conclusion = "failure"
	queueWebhook(t, f, "check_run", "late-old-head", c, 204)
	if !reflect.DeepEqual(newItem, f.item(t, 7)) {
		t.Fatal("late old completion changed the new head")
	}
	answer(p.Head, true, 0)
	answer(strings.Repeat("b", 40), false, 6)
	f.call(t, f.foreign, "GET", path(p.Head), nil, 404, nil)
	noRead := f.agent
	noRead.Scopes = nil
	f.call(t, noRead, "GET", path(p.Head), nil, 403, nil)
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), f.foreign), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_queue_failures`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("foreign tenant saw queue history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Rebuild(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(newItem, f.item(t, 7)) {
		t.Fatal("rebuild changed head/projection")
	}
	answer(p.Head, true, 0)
	answer(strings.Repeat("b", 40), false, 6)
	// A queue event with no recorded enqueue binding stays retryable; never
	// associate it with today's PR head just because the PR number matches.
	c.Suite.Head, c.Head = strings.Repeat("e", 40), strings.Repeat("e", 40)
	c.Suite.Branch = "gh-readonly-queue/main/pr-7-" + strings.Repeat("e", 40)
	queueWebhook(t, f, "check_run", "unbound-group", c, 502)
	if countFailures() != 6 {
		t.Fatal("unbound group created historical evidence")
	}
}

func TestQueueWorkflowReadRejectsWrongSubjectAndPartialSuite(t *testing.T) {
	// Risk: workflow spoofing or a partial suite falsely releases enqueue.
	suite := queueSuite{ID: 42, Head: strings.Repeat("c", 40), Branch: "gh-readonly-queue/main/pr-7-" + strings.Repeat("b", 40)}
	suite.App.Slug = "github-actions"
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	run := queueRun{ID: 55, Name: "web", Head: suite.Head, Status: "completed", Conclusion: "failure", Suite: suite, Completed: &at}
	run.App.Slug = "github-actions"
	mode := "ok"
	get := func(path string, out any) error {
		var body any
		switch {
		case strings.HasPrefix(path, "/actions/runs?"):
			if !strings.Contains(path, "check_suite_id=42&head_sha="+suite.Head+"&per_page=2") {
				t.Fatal("workflow lookup did not bind suite and head")
			}
			head, event := suite.Head, "merge_group"
			if mode == "wrong-head" {
				head = strings.Repeat("d", 40)
			}
			if mode == "wrong-event" {
				event = "pull_request"
			}
			body = map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{"name": "CI", "check_suite_id": 42, "head_sha": head, "head_branch": suite.Branch, "event": event}}}
		case strings.HasPrefix(path, "/check-suites/42/check-runs?"):
			if mode == "unavailable" {
				return errRead
			}
			n := 1
			if mode == "partial" {
				n = 2
			}
			body = map[string]any{"total_count": n, "check_runs": []queueRun{run}}
		default:
			return fmt.Errorf("unexpected queue read path")
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
	for _, pointer := range []*queueRun{&run, nil} {
		workflow, runs, err := readQueueChecks(get, suite, pointer)
		if err != nil || workflow != "CI" || len(runs) != 1 || runs[0].ID != run.ID {
			t.Fatal("exact workflow/check observation failed", err)
		}
	}
	for _, invalid := range []string{"wrong-head", "wrong-event", "partial", "unavailable"} {
		mode = invalid
		if _, _, err := readQueueChecks(get, suite, nil); !errors.Is(err, errRead) {
			t.Fatalf("%s read reported success or wrong refusal: %v", mode, err)
		}
	}
}
