// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	merged                *MergeFact
	mergeErr              error
	pullReads, mergeReads int
}

func (g *receiptGitHub) Pull(ctx context.Context, n int64) (Pull, error) {
	g.pullReads++
	return g.fakeGitHub.Pull(ctx, n)
}

func (g *receiptGitHub) MergedPull(context.Context, int64) (*MergeFact, error) {
	g.mergeReads++
	return g.merged, g.mergeErr
}
func (g *receiptGitHub) MergedGroup(context.Context, string) ([]MergeFact, error) {
	g.mergeReads++
	if g.merged == nil {
		return nil, g.mergeErr
	}
	return []MergeFact{*g.merged}, g.mergeErr
}

type receiptCanonicalGitHub struct {
	*receiptGitHub
	pull auditPull
}

func (g *receiptCanonicalGitHub) mergedPullState(_ context.Context, number int64) (*MergeFact, bool, error) {
	g.mergeReads++
	return mergedPullState(func(_ string, out any) error {
		raw, err := json.Marshal(g.pull)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}, g.pull.Base.Repo.Name, number)
}

func (g *receiptCanonicalGitHub) mergedGroupState(ctx context.Context, sha string, waitForProof bool) ([]MergeFact, bool, error) {
	facts, err := g.MergedGroup(ctx, sha)
	return facts, waitForProof && len(facts) == 0, err
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
	out := make([]int, 9)
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT
			(SELECT count(*) FROM delivery_github_events WHERE delivery_id='retry-delivery'),
			(SELECT count(*) FROM delivery_github_events WHERE delivery_id='fanout-effect'),
			(SELECT count(*) FROM delivery_github_events WHERE delivery_id='audit-checks-retry-delivery'),
			(SELECT count(*) FROM delivery_merge_audit),
			(SELECT count(*) FROM delivery_metric_runs),
			(SELECT count(*) FROM delivery_flow_steps WHERE source='github_app'),
			(SELECT count(*) FROM events WHERE type='delivery.merge_done'),
			(SELECT count(*) FROM events WHERE type IN ('delivery.step','delivery.item','delivery.incident')),
			(SELECT count(*) FROM events WHERE type='delivery.bypass_merge')`).Scan(&out[0], &out[1], &out[2], &out[3], &out[4], &out[5], &out[6], &out[7], &out[8])
	})
	return out
}

func TestDeliveryWebhookAcceptsOpaqueInstallationToken(t *testing.T) {
	// Risk: a valid newly minted GitHub token is refused locally, losing every
	// network-dependent webhook while logging no upstream HTTP status.
	// All signing material and tokens are synthetic; the transport has no network.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("could not generate ephemeral fixture signing material")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "fixture-signing.pem")
	if err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal("could not write ephemeral fixture signing material")
	}
	for _, event := range []string{"pull_request", "check_run", "check_suite", "merge_group"} {
		t.Run(event, func(t *testing.T) {
			f := newFixture(t)
			fixtureName := "pull_request.closed"
			if event == "merge_group" {
				fixtureName = "merge_group.checks_requested"
			}
			raw, err := os.ReadFile("testdata/" + fixtureName + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			head, base, group := strings.Repeat("b", 40), strings.Repeat("a", 40), strings.Repeat("c", 40)
			if event == "pull_request" {
				body["action"] = "opened"
				pull := body["pull_request"].(map[string]any)
				pull["state"], pull["merged"] = "open", false
			} else if event == "check_run" || event == "check_suite" {
				body["action"] = "completed"
				body[event] = map[string]any{"head_sha": head, "pull_requests": []any{map[string]any{"number": 7}}, "name": "go-test", "status": "completed", "conclusion": "success"}
			}
			raw = receiptBody(t, f, body)
			pullRaw, err := os.ReadFile("testdata/pull_request.closed.json")
			if err != nil {
				t.Fatal(err)
			}
			var pullFixture map[string]any
			if err := json.Unmarshal(pullRaw, &pullFixture); err != nil {
				t.Fatal(err)
			}
			pull := pullFixture["pull_request"].(map[string]any)
			pull["state"], pull["merged"] = "open", false
			// A realistic large PR document exceeds the default 64 KiB while
			// staying within the REST reader's existing 4 MiB bound.
			pull["body"] = strings.Repeat("synthetic PR detail ", 5000)
			mintedToken := "ghs_" + strings.Repeat("a", 36)
			minted, revoked, reads := 0, 0, 0
			f.m.config.KeyFile = file
			app := &crossreview.GitHubApp{Config: f.m.config, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var response any
				status := http.StatusOK
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/app/installations/456/access_tokens":
					var request struct {
						Permissions  map[string]string `json:"permissions"`
						Repositories []string          `json:"repositories"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal("invalid fake mint request")
					}
					if len(request.Permissions) != 5 || !reflect.DeepEqual(request.Repositories, []string{"delivery"}) {
						t.Fatal("mint changed the existing repository/permission scope")
					}
					for _, level := range request.Permissions {
						if level != "read" {
							t.Fatal("observer requested write authority")
						}
					}
					minted++
					status = http.StatusCreated
					response = map[string]any{"token": mintedToken, "permissions": request.Permissions, "repository_selection": "selected", "repositories": []any{map[string]string{"full_name": f.m.config.Repository}}}
				case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
					revoked++
					status, response = http.StatusNoContent, nil
				case r.Method == http.MethodGet && r.URL.Path == "/repos/example/delivery/pulls/7":
					reads++
					response = pull
				case r.Method == http.MethodGet && r.URL.Path == "/repos/example/delivery/pulls":
					reads++
					response = []any{pull}
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/"):
					reads++
					response = map[string]any{"ref": body["merge_group"].(map[string]any)["head_ref"], "object": map[string]string{"type": "commit", "sha": group}}
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
					reads++
					response = map[string]any{"total_commits": 1, "status": "ahead", "commits": []any{map[string]any{"sha": group, "parents": []any{map[string]string{"sha": head}, map[string]string{"sha": base}}}}}
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/check-runs"):
					reads++
					response = map[string]any{"total_count": 0, "check_runs": []any{}}
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/statuses"):
					reads++
					response = []any{}
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					reads++
					response = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": 7, "headRefOid": head, "mergeQueueEntry": map[string]any{"id": "fixture-entry", "headCommit": map[string]string{"oid": group}}}}}}
				default:
					t.Fatal("unexpected fake GitHub operation")
				}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal("invalid fake GitHub response")
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded)), Request: r}, nil
			})}}
			f.m.github = AppReader{App: app}
			if w := receiptSend(f, event, "legacy-token-control", raw); w.Code != http.StatusNoContent || minted != 1 || revoked != 1 || reads == 0 {
				t.Fatalf("legacy control failed: HTTP=%d minted=%d revoked=%d reads=%d", w.Code, minted, revoked, reads)
			}
			// Synthetic opaque token shaped like the documented ~520-character
			// stateless format. Its contents have no authority and are never decoded.
			mintedToken = "ghs_123_" + strings.Repeat("a", 120) + "." + strings.Repeat("b", 120) + "." + strings.Repeat("c", 270)
			previousReads := reads
			w := receiptSend(f, event, "stateless-token", raw)
			attempts, _, finished, checkpoints, step, class := receiptState(t, f, "stateless-token")
			if w.Code != http.StatusNoContent {
				if w.Code != http.StatusBadGateway || attempts != 1 || finished != nil || len(checkpoints) != 0 || step == nil || *step != "projection" || class == nil || *class != "github_unavailable" || minted != 2 || revoked != 1 || reads != previousReads {
					t.Fatal("failed for a different reason than local post-mint token rejection")
				}
				t.Fatalf("valid %d-character token refused after HTTP 201 mint: webhook HTTP 502, projection/github_unavailable, zero downstream reads, no token revocation", len(mintedToken))
			}
			if attempts != 1 || finished == nil || len(checkpoints) != len(webhookSteps) || checkpoints["projection"] != webhookRecorded || step != nil || class != nil || minted != 2 || revoked != 2 || reads <= previousReads {
				t.Fatal("stateless token did not finish the same authenticated event and revoke its scoped read token")
			}
		})
	}
}

func TestDeliveryWebhookTerminalNoOps(t *testing.T) {
	// Risk: empty terminal results create an endless stream of GitHub reads,
	// or a closed/unmerged PR is incorrectly completed on a later sweep.
	for _, scenario := range []string{"closed_unmerged", "other_branch_merge", "direct_push", "destroyed_closed_unmerged", "destroyed_other_branch"} {
		t.Run(scenario, func(t *testing.T) {
			f, metrics := newMetricsFixture(t)
			g := &receiptCanonicalGitHub{receiptGitHub: &receiptGitHub{metricsFake: metrics}}
			f.m.github = g
			p := f.pull()
			f.gh.pulls[7] = p
			fact := mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
			body := auditPRBody(f, fact)
			pull := body["pull_request"].(map[string]any)
			event := "pull_request"
			switch scenario {
			case "closed_unmerged":
				pull["merged"] = false
				pull["state"] = "closed"
				p.Open = false
			case "other_branch_merge":
				pull["base"].(map[string]any)["ref"] = "release"
				p.Open, p.Merged = false, true
			case "direct_push":
				event = "push"
				body = map[string]any{"ref": "refs/heads/main", "after": fact.SHA}
			case "destroyed_closed_unmerged", "destroyed_other_branch":
				recordedMergeWebhook(t, f, "merge_group.checks_requested", "terminal-queue-request", 204, nil)
				event = "merge_group"
				body = map[string]any{"action": "destroyed", "merge_group": map[string]any{"head_sha": fact.SHA, "base_sha": p.Base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-7-group"}}
				g.pull.Number, g.pull.State = 7, "closed"
				g.pull.Base.Ref, g.pull.Base.Repo.Name = "main", f.m.config.Repository
				p.Open = false
				if scenario == "destroyed_other_branch" {
					g.pull.Merged, p.Merged = true, true
					g.pull.Base.Ref = "release"
				}
			}
			f.gh.pulls[7] = p
			beforeState := mergedState(t, f, f.ticket)
			raw := receiptBody(t, f, body)
			if w := receiptSend(f, event, "retry-delivery", raw); w.Code != 204 {
				t.Fatalf("terminal status %d", w.Code)
			}
			attempts, _, finished, checkpoints, step, class := receiptState(t, f, "retry-delivery")
			if attempts != 1 || finished == nil || len(checkpoints) != len(webhookSteps) || checkpoints["completion"] != webhookNothingToRecord || step != nil || class != nil {
				t.Fatalf("terminal result still pending: %d %v %v %v %v", attempts, finished, checkpoints, step, class)
			}
			for _, step := range []string{"check_audit", "merge_audit", "metrics", "flow"} {
				if checkpoints[step] != webhookNothingToRecord {
					t.Fatalf("empty %s was not a no-op: %s", step, checkpoints[step])
				}
			}
			wantMergeReads := 0
			if scenario == "direct_push" || strings.HasPrefix(scenario, "destroyed_") {
				wantMergeReads = 1
			}
			if g.mergeReads != wantMergeReads {
				t.Fatalf("terminal canonical reads %d, want %d", g.mergeReads, wantMergeReads)
			}
			effects := receiptEffects(t, f)
			reads := []int{g.pullReads, g.mergeReads, metrics.checkReads, len(metrics.calls), f.fanouts}
			restarted := New(f.d.App, f.m.config, f.m.secret, g, f.m.fanout)
			restarted.now = f.m.now
			for range 3 {
				f.at = f.at.Add(24 * time.Hour)
				if err := restarted.retryWebhooks(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if w := receiptSend(f, event, "retry-delivery", raw); w.Code != 204 {
				t.Fatalf("terminal replay status %d", w.Code)
			}
			if got := []int{g.pullReads, g.mergeReads, metrics.checkReads, len(metrics.calls), f.fanouts}; !reflect.DeepEqual(got, reads) {
				t.Fatalf("terminal receipt reread GitHub/fanout: %v -> %v", reads, got)
			}
			attempts, _, _, _, _, _ = receiptState(t, f, "retry-delivery")
			if attempts != 1 || mergedState(t, f, f.ticket) != beforeState || !reflect.DeepEqual(receiptEffects(t, f), effects) || effects[6] != 0 {
				t.Fatal("terminal replay changed attempts, effects or ticket")
			}
		})
	}
}

func TestDeliveryWebhookRetryBudget(t *testing.T) {
	// Risk: missing proof, a refused gate or persistent upstream failure never
	// stops; exhaustion hides failure, bypasses a gate, or restarts more reads.
	for _, scenario := range []string{"missing_merge_proof", "refused_gate", "github_failure", "reserved_last_attempt"} {
		t.Run(scenario, func(t *testing.T) {
			f, g := mergeFixture(t)
			fact := mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
			if scenario == "refused_gate" {
				g.facts[7] = fact
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{}' WHERE id=$1`, f.ticket)
					return err
				})
			}
			if scenario == "github_failure" {
				g.beforeRead = func() { g.err = fmt.Errorf("%w: %s", crossreview.ErrUnavailable, webhookPrivateText) }
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			f.m.webhookFailures.logger = logger
			raw := receiptBody(t, f, auditPRBody(f, fact))
			wantStatus := 204
			if scenario == "github_failure" {
				wantStatus = 502
			}
			if w := receiptSend(f, "pull_request", "retry-delivery", raw); w.Code != wantStatus {
				t.Fatalf("first attempt status %d, want %d", w.Code, wantStatus)
			}
			attempts, next, finished, checkpoints, step, class := receiptState(t, f, "retry-delivery")
			if attempts != 1 || finished != nil || checkpoints["completion"] != "" || step == nil || !next.Equal(f.at.Add(time.Minute)) {
				t.Fatal("first unresolved attempt was not retained with backoff")
			}
			if scenario == "github_failure" {
				if *step != "reader-MergedPull" || class == nil || *class != "github_unavailable" {
					t.Fatal("wrong initial failure reason")
				}
			} else if *step != "completion" || class != nil {
				t.Fatal("pending proof/gate reported a failure")
			}
			restarted := New(f.d.App, f.m.config, f.m.secret, g, f.m.fanout)
			restarted.now = f.m.now
			restarted.webhookFailures.logger = logger
			if scenario == "reserved_last_attempt" {
				// Model a process dying after the final reservation was committed.
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE delivery_webhook_receipts SET attempts=$2 WHERE delivery_id=$1`, "retry-delivery", webhookMaxAttempts)
					return err
				})
				f.at = next
				reads := g.reads
				if err := restarted.retryWebhooks(t.Context()); err != nil {
					t.Fatal(err)
				}
				if g.reads != reads {
					t.Fatal("restart exceeded the reserved final attempt's read budget")
				}
			} else {
				for attempt := int64(2); attempt <= webhookMaxAttempts; attempt++ {
					reads := g.reads
					f.at = next.Add(-time.Nanosecond)
					if err := restarted.retryWebhooks(t.Context()); err != nil || g.reads != reads {
						t.Fatalf("retry before due time: %v, reads %d -> %d", err, reads, g.reads)
					}
					f.at = next
					err := restarted.retryWebhooks(t.Context())
					if scenario == "github_failure" {
						if !errors.Is(err, crossreview.ErrUnavailable) {
							t.Fatalf("failed attempt reported success or wrong cause: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					attempts, next, finished, checkpoints, step, class = receiptState(t, f, "retry-delivery")
					if attempts != attempt || g.reads != reads+1 {
						t.Fatalf("attempt accounting %d/%d, reads %d -> %d", attempts, attempt, reads, g.reads)
					}
					if attempt < webhookMaxAttempts && (finished != nil || checkpoints["completion"] != "" || !next.Equal(f.at.Add(webhookBackoff(attempt)))) {
						t.Fatal("unfinished attempt lost its checkpoint or backoff")
					}
				}
			}
			attempts, _, finished, checkpoints, step, class = receiptState(t, f, "retry-delivery")
			if attempts != webhookMaxAttempts || finished == nil || checkpoints["completion"] != webhookGaveUp || step == nil || class == nil || *class != "retry_exhausted" || mergedState(t, f, f.ticket) != "qa" {
				t.Fatalf("retry budget did not retain a terminal failure: %d %v %v %v %v", attempts, finished, checkpoints, step, class)
			}
			f.tx(t, func(tx pgx.Tx) error {
				var payload []byte
				if err := tx.QueryRow(t.Context(), `SELECT payload FROM delivery_webhook_receipts WHERE delivery_id='retry-delivery'`).Scan(&payload); err != nil {
					return err
				}
				if !bytes.Equal(payload, raw) {
					t.Fatal("exhaustion discarded the verified payload")
				}
				if scenario == "refused_gate" {
					var count int
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM status_autopilot_proposals WHERE node_id=$1 AND status='pending'`, f.ticket).Scan(&count); err != nil {
						return err
					}
					if count != 1 {
						t.Fatal("exhaustion erased or duplicated the Needs You refusal")
					}
				}
				return nil
			})
			reads, fanouts, effects := g.reads, f.fanouts, receiptEffects(t, f)
			for range 3 {
				f.at = f.at.Add(24 * time.Hour)
				if err := restarted.retryWebhooks(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if w := receiptSend(f, "pull_request", "retry-delivery", raw); w.Code != 204 {
				t.Fatalf("exhausted replay status %d", w.Code)
			}
			if g.reads != reads || f.fanouts != fanouts || !reflect.DeepEqual(receiptEffects(t, f), effects) {
				t.Fatal("exhaustion restarted network work or duplicated effects")
			}
			gaveUp := 0
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) != nil {
					t.Fatal("invalid log record")
				}
				if entry["msg"] == "GitHub webhook delivery gave up" {
					gaveUp++
					if entry["outcome"] != "gave_up" || entry["error_class"] != "retry_exhausted" || entry["attempts"] != float64(webhookMaxAttempts) || entry["delivery_id"] != "retry-delivery" || entry["level"] != "WARN" {
						t.Fatalf("incomplete exhaustion diagnostic: %v", entry)
					}
				}
			}
			if gaveUp != 1 || strings.Contains(logs.String(), webhookPrivateText) || strings.Contains(logs.String(), string(raw)) {
				t.Fatal("exhaustion diagnostic was missing, duplicated or contained private content")
			}
		})
	}
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
				created := f.at.Add(-time.Hour)
				g.respond = func(path string) (any, error) {
					if step == "metrics" && unavailable {
						return nil, crossreview.ErrUnavailable
					}
					if strings.HasPrefix(path, "/actions/runs?check_suite_id=900&") {
						run := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", p.Branch, p.Head, created, 10*time.Minute, "success", 900)
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
				if !next.Equal(f.at.Add(time.Minute)) {
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
				if step == "flow" && effects[7] == 0 {
					t.Fatal("fixture did not retain any Flow event")
				}
				if afterEffect && step == "merge_audit" && (before[8] == 0 || effects[8] != before[8]) {
					t.Fatalf("merge audit event was emitted twice: %v -> %v", before, effects)
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
	if webhookBackoff(2) != 2*time.Minute || webhookBackoff(1000000) != time.Hour {
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
