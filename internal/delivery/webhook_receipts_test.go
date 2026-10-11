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
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

type receiptGitHub struct {
	*metricsFake
	merged   *MergeFact
	mergeErr error
}

func (g *receiptGitHub) MergedPull(context.Context, int64) (*MergeFact, error) {
	return g.merged, g.mergeErr
}
func (g *receiptGitHub) MergedGroup(context.Context, string) ([]MergeFact, error) {
	if g.merged == nil {
		return nil, g.mergeErr
	}
	return []MergeFact{*g.merged}, g.mergeErr
}

func receiptSend(f *fixture, event, id string, raw []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-GitHub-Delivery", id)
	r.Header.Set("X-Hub-Signature-256", signed(raw, f.m.secret))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func receiptBody(t *testing.T, f *fixture, body map[string]any) []byte {
	t.Helper()
	body["installation"] = map[string]any{"id": 456}
	body["repository"] = map[string]any{"full_name": f.m.config.Repository, "default_branch": "main"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The trigger fails at a real database boundary, including the crash window
// between an effect's commit and its receipt checkpoint. It is fixture-local.
func receiptFault(t *testing.T, f *fixture, table, predicate string) func() {
	t.Helper()
	_, err := f.d.Admin.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION aeon_receipt_fault() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF %s THEN RAISE EXCEPTION 'injected receipt failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER aeon_receipt_fault BEFORE INSERT OR UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION aeon_receipt_fault()`, predicate, table))
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := f.d.Admin.Exec(t.Context(), "DROP TRIGGER aeon_receipt_fault ON "+table+"; DROP FUNCTION aeon_receipt_fault()"); err != nil {
			t.Fatal(err)
		}
	}
}

func receiptState(t *testing.T, f *fixture, id string) (attempts int64, next time.Time, finished *time.Time, checkpoints map[string]webhookOutcome, step, class *string) {
	t.Helper()
	var raw []byte
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT attempts,next_attempt_at,finished_at,checkpoints,last_step,last_error_class FROM delivery_webhook_receipts WHERE delivery_id=$1`, id).Scan(&attempts, &next, &finished, &raw, &step, &class)
	})
	if json.Unmarshal(raw, &checkpoints) != nil {
		t.Fatal("invalid checkpoints")
	}
	return
}

func receiptEffects(t *testing.T, f *fixture) []int {
	t.Helper()
	out := make([]int, 8)
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT
			(SELECT count(*) FROM delivery_github_events WHERE delivery_id='retry-delivery'),
			(SELECT count(*) FROM delivery_github_events WHERE delivery_id='fanout-effect'),
			(SELECT count(*) FROM delivery_github_events WHERE delivery_id='audit-checks-retry-delivery'),
			(SELECT count(*) FROM delivery_merge_audit),
			(SELECT count(*) FROM delivery_metric_runs),
			(SELECT count(*) FROM delivery_flow_steps WHERE source='github_app'),
			(SELECT count(*) FROM events WHERE type='delivery.merge_done'),
			(SELECT count(*) FROM events WHERE type LIKE 'delivery.flow.%')`).Scan(&out[0], &out[1], &out[2], &out[3], &out[4], &out[5], &out[6], &out[7])
	})
	return out
}

func TestDeliveryWebhookInternalRetry(t *testing.T) {
	// Risk: each failed step loses a verified delivery or depends on redelivery;
	// a committed effect without its checkpoint must still converge exactly once.
	for _, step := range webhookSteps {
		for _, afterEffect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after_effect_%t", step, afterEffect), func(t *testing.T) {
				f, metrics := newMetricsFixture(t)
				g := &receiptGitHub{metricsFake: metrics}
				f.m.github = g
				p := f.pull()
				f.gh.pulls[7] = p
				if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
					t.Fatal(err)
				}
				unavailable := !afterEffect
				// Production fan-out is an idempotent sink. Model that contract
				// with a committed unique key, rather than counting invocations.
				f.m.fanout = func(ctx context.Context, raw []byte) error {
					if step == "fanout" && unavailable {
						return crossreview.ErrUnavailable
					}
					return db.InTenant(db.AllProjects(ctx, "idempotent fanout regression sink"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
						_, err := tx.Exec(ctx, `INSERT INTO delivery_github_events(tenant_id,delivery_id,event,repository,received_at,payload_sha256,fanout_done,processed_at)
							VALUES($1,'fanout-effect','status',$2,$3,$4,true,$3) ON CONFLICT(tenant_id,delivery_id) DO NOTHING`, f.person.TenantID, f.m.config.Repository, f.at, digest(raw))
						return err
					})
				}
				g.respond = func(path string) (any, error) {
					if step == "metrics" && unavailable {
						return nil, crossreview.ErrUnavailable
					}
					if strings.HasPrefix(path, "/actions/runs?check_suite_id=900&") {
						run := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", p.Branch, p.Head, f.at.Add(-time.Hour), 10*time.Minute, "success", 900)
						run["pull_requests"] = []any{map[string]any{"number": 7}}
						return map[string]any{"total_count": 1, "workflow_runs": []any{run}}, nil
					}
					return nil, crossreview.ErrUnavailable // optional job details remain an honest gap
				}
				event := "pull_request"
				body := map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "head": map[string]any{"sha": p.Head}, "base": map[string]any{"sha": p.Base, "repo": map[string]any{"full_name": f.m.config.Repository}}}}
				if step == "check_audit" {
					event = "check_run"
					body = map[string]any{"action": "completed", "check_run": map[string]any{"head_sha": p.Head, "name": "go-test", "status": "completed", "conclusion": "success"}}
				}
				if step == "metrics" || step == "flow" {
					event = "check_suite"
					body = map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": p.Head, "status": "completed", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{map[string]any{"number": 7}}}}
				}
				if step == "merge_audit" || step == "completion" {
					fact := mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
					g.merged = &fact
					p.Open, p.Merged = false, true
					f.gh.pulls[7] = p
					g.checks[fact.SHA] = auditGreen()
					body = auditPRBody(f, fact)
					f.tx(t, func(tx pgx.Tx) error {
						if _, err := tx.Exec(t.Context(), `UPDATE nodes SET state='qa',fields=$2 WHERE id=$1`, f.ticket, mergeBenefits); err != nil {
							return err
						}
						_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,updated_by) VALUES($1,$2,$3,$3)`, f.person.TenantID, f.project, f.person.ID)
						return err
					})
				}
				removeFault := func() {}
				wantClass := "github_unavailable"
				if afterEffect {
					removeFault = receiptFault(t, f, "delivery_webhook_receipts", "NEW.delivery_id='retry-delivery' AND NEW.checkpoints ? '"+step+"' AND NOT OLD.checkpoints ? '"+step+"'")
					wantClass = "database"
				} else {
					switch step {
					case "projection":
						f.gh.err = crossreview.ErrUnavailable
					case "check_audit":
						removeFault = receiptFault(t, f, "delivery_github_events", "NEW.delivery_id='audit-checks-retry-delivery'")
						wantClass = "database"
					case "merge_audit":
						g.checksErr = crossreview.ErrUnavailable
					case "flow":
						removeFault = receiptFault(t, f, "delivery_flow_steps", "NEW.source='github_app'")
						wantClass = "database"
					case "completion":
						g.mergeErr = crossreview.ErrUnavailable
					}
				}
				raw := receiptBody(t, f, body)
				if w := receiptSend(f, event, "retry-delivery", raw); w.Code != 502 {
					t.Fatalf("failed %s status %d, want 502", step, w.Code)
				}
				attempts, next, finished, checkpoints, failedStep, class := receiptState(t, f, "retry-delivery")
				if attempts != 1 || finished != nil || checkpoints[step] != "" || failedStep == nil || class == nil || *class != wantClass {
					t.Fatalf("wrong failure: attempts=%d finished=%v checkpoints=%v step=%v class=%v", attempts, finished, checkpoints, failedStep, class)
				}
				if *failedStep != step && !(step == "completion" && *failedStep == "reader-MergedPull") {
					t.Fatalf("failed at %s instead of %s", *failedStep, step)
				}
				if !next.Equal(f.at.Add(5 * time.Second)) {
					t.Fatalf("first retry schedule %s", next)
				}
				before := receiptEffects(t, f)
				removeFault()
				unavailable, f.gh.err, g.checksErr, g.mergeErr = false, nil, nil, nil
				// A fresh module on the same store proves restart recovery. No
				// request is resent; only the worker's due-page operation runs.
				restarted := New(f.d.App, f.m.config, f.m.secret, g, f.m.fanout)
				restarted.now = func() time.Time { return f.at }
				f.at = next.Add(-time.Nanosecond)
				if err := restarted.retryWebhooks(t.Context()); err != nil {
					t.Fatal(err)
				}
				if got := receiptEffects(t, f); !reflect.DeepEqual(got, before) {
					t.Fatalf("retried before due time: %v -> %v", before, got)
				}
				f.at = next
				if step == "flow" || step == "completion" {
					g.respond = func(string) (any, error) { t.Fatal("completed metrics step was reread on retry"); return nil, errRead }
				}
				if err := restarted.retryWebhooks(t.Context()); err != nil {
					t.Fatal(err)
				}
				attempts, _, finished, checkpoints, failedStep, class = receiptState(t, f, "retry-delivery")
				if attempts != 2 || finished == nil || len(checkpoints) != len(webhookSteps) || failedStep != nil || class != nil {
					t.Fatalf("retry did not finish: %d %v %v %v %v", attempts, finished, checkpoints, failedStep, class)
				}
				effects := receiptEffects(t, f)
				want := map[string]int{"projection": 0, "fanout": 1, "check_audit": 2, "merge_audit": 3, "metrics": 4, "flow": 5, "completion": 6}[step]
				if effects[want] != 1 {
					t.Fatalf("%s effect count %v, want exactly one", step, effects)
				}
				if afterEffect && step == "flow" && effects[7] != before[7] {
					t.Fatalf("flow effect was emitted twice: %v -> %v", before, effects)
				}
				if step == "flow" && effects[4] != 1 {
					t.Fatalf("metrics replay duplicated %v", effects)
				}
				if err := restarted.retryWebhooks(t.Context()); err != nil {
					t.Fatal(err)
				}
				if w := receiptSend(f, event, "retry-delivery", raw); w.Code != 204 {
					t.Fatalf("finished redelivery status %d", w.Code)
				}
				if got := receiptEffects(t, f); !reflect.DeepEqual(got, effects) {
					t.Fatalf("duplicate effects: %v -> %v", effects, got)
				}
			})
		}
	}
	if webhookBackoff(2) != 10*time.Second || webhookBackoff(1000000) != 5*time.Minute {
		t.Fatal("backoff did not grow to its bound")
	}
}

type receiptBarrierGitHub struct {
	*fakeGitHub
	entered, release chan struct{}
}

func (g *receiptBarrierGitHub) Pull(ctx context.Context, n int64) (Pull, error) {
	close(g.entered)
	select {
	case <-g.release:
		return g.fakeGitHub.Pull(ctx, n)
	case <-ctx.Done():
		return Pull{}, ctx.Err()
	}
}

func TestDeliveryWebhookReceiptIntegrityAndClaims(t *testing.T) {
	// Risk: unauthenticated input is stored, delivery-id reuse replaces verified
	// facts, or a second replica executes a receipt while its first claim runs.
	f := newFixture(t)
	p := f.pull()
	f.gh.pulls[7] = p
	g := &receiptBarrierGitHub{fakeGitHub: f.gh, entered: make(chan struct{}), release: make(chan struct{})}
	f.m.github = g
	raw := receiptBody(t, f, map[string]any{"action": "opened", "pull_request": map[string]any{"number": 7, "head": map[string]any{"sha": p.Head}, "base": map[string]any{"sha": p.Base, "repo": map[string]any{"full_name": f.m.config.Repository}}}})
	r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
	r.Header.Set("X-GitHub-Event", "pull_request")
	r.Header.Set("X-GitHub-Delivery", "retry-delivery")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("unsigned status %d", w.Code)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_webhook_receipts`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("unsigned receipt persisted")
		}
		return nil
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- receiptSend(f, "pull_request", "retry-delivery", raw) }()
	release := sync.OnceFunc(func() { close(g.release) })
	defer release()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("projection barrier not entered")
	}
	// Network work is now blocked, so this proves persistence precedes it.
	attempts, _, finished, checkpoints, _, _ := receiptState(t, f, "retry-delivery")
	if attempts != 1 || finished != nil || len(checkpoints) != 0 {
		t.Fatal("receipt was not stored before projection")
	}
	other := New(f.d.App, f.m.config, f.m.secret, g, f.m.fanout)
	other.now = f.m.now
	if err := other.processWebhook(t.Context(), "retry-delivery", true); !errors.Is(err, errWebhookBusy) {
		t.Fatalf("second replica claim: %v", err)
	}
	if w := receiptSend(f, "pull_request", "retry-delivery", append(append([]byte(nil), raw...), ' ')); w.Code != 409 {
		t.Fatalf("changed authenticated content status %d", w.Code)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_webhook_receipts`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("foreign tenant can see receipt")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case w := <-done:
		if w.Code != 204 {
			t.Fatalf("first claim status %d", w.Code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first claim did not finish")
	}
	if f.fanouts != 1 {
		t.Fatalf("claim duplicated fanout: %d", f.fanouts)
	}
}
