// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/views"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

func projectLeadFixture(t *testing.T, admission harness.LeadAdmission) *harnessFixture {
	f := fixture(t)
	f.mux = http.NewServeMux()
	harness.NewWithLeadAdmission(f.db.App, admission).Mount(f.mux)
	return f
}
func readyLeadChecks(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	g := harness.LeadGate{State: "available", CheckedAt: now}
	return harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}, err
}
func leadCandidate(t *testing.T, f *harnessFixture) (string, string, map[string]any) {
	t.Helper()
	lease := "lease-" + uid()
	body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "coordinator", "advertised_capabilities": []string{"inbox", "pause"}, "harness_session_ref": "lead-ref-" + uid(), "worker_lease": lease}
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", body, "")
	expect(t, w, 201)
	return decode(t, w)["id"].(string), lease, body
}
func startLead(t *testing.T, f *harnessFixture, revision int) map[string]any {
	t.Helper()
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/lead", map[string]any{"expected_revision": revision}, "")
	expect(t, w, 200)
	return decode(t, w)
}
func claimLead(t *testing.T, f *harnessFixture, session, lease string, revision any) map[string]any {
	t.Helper()
	w := f.call(f.agent, "POST", "/api/projects/"+f.project+"/lead/claim", map[string]any{"expected_revision": revision, "session_id": session}, lease)
	expect(t, w, 200)
	return decode(t, w)
}

func TestProjectLeadLifecycleAndExplicitMigration(t *testing.T) {
	f := projectLeadFixture(t, readyLeadChecks)
	base := "/api/projects/" + f.project
	session, lease, _ := leadCandidate(t, f)
	unrelated, _, _ := leadCandidate(t, f)
	children := map[string]string{}
	for _, parent := range []string{session, unrelated} {
		body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "parent_harness_session_id": parent, "harness_session_ref": "child-ref-" + uid(), "worker_lease": "child-lease-" + uid()}
		response := f.call(f.person, "POST", base+"/harness-sessions", body, "")
		expect(t, response, 201)
		children[decode(t, response)["id"].(string)] = parent
	}
	// An existing coordinator does not silently become a lead.
	w := f.call(f.person, "GET", base+"/lead", nil, "")
	expect(t, w, 200)
	if decode(t, w)["state"] != "none" {
		t.Fatal("legacy coordinator was implicitly adopted")
	}
	l := startLead(t, f, 0)
	expect(t, f.call(f.agent, "POST", base+"/lead", map[string]any{"expected_revision": 1}, ""), 403)
	expect(t, f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": 1, "session_id": session}, "wrong-proof-with-at-least-32-bytes"), 403)
	l = claimLead(t, f, session, lease, l["revision"])
	if l["generation"] != float64(1) || l["session_id"] != session || l["process_active"] != true || l["state"] != "starting" {
		t.Fatalf("claim=%v", l)
	}
	// Admission is repeated even for the current session; generation stays put.
	l = claimLead(t, f, session, lease, l["revision"])
	if l["generation"] != float64(1) {
		t.Fatal("same generation claim allocated another generation")
	}
	w = f.call(f.agent, "POST", base+"/lead/pause", map[string]any{"expected_revision": l["revision"], "generation": 1}, lease)
	expect(t, w, 200)
	l = decode(t, w)
	if l["state"] != "paused" || l["process_active"] != true {
		t.Fatalf("pause invented exit: %v", l)
	}
	expect(t, f.call(f.person, "POST", base+"/lead", map[string]any{"expected_revision": l["revision"]}, ""), 409)
	var control string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		var requested bool
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1 AND kind='stop'`, session).Scan(&count); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT pause_record->>'state'='requested' FROM harness_sessions WHERE id=$1`, session).Scan(&requested); err != nil {
			return err
		}
		if count != 1 || !requested {
			t.Fatal("pause did not reuse durable handover control")
		}
		return tx.QueryRow(t.Context(), `SELECT pause_record->>'control_id' FROM harness_sessions WHERE id=$1`, session).Scan(&control)
	})
	finishPaused(t, f, base+"/harness-sessions/"+session, lease, control)
	l = startLead(t, f, int(l["revision"].(float64)))
	next, nextLease, _ := leadCandidate(t, f)
	l = claimLead(t, f, next, nextLease, l["revision"])
	if l["generation"] != float64(2) || l["session_id"] != next {
		t.Fatalf("successor=%v", l)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var previous, state string
		if err := tx.QueryRow(t.Context(), `SELECT continuation_handover->>'succeeds_session_id',continuation_handover->'handover'->>'state' FROM harness_sessions WHERE id=$1`, next).Scan(&previous, &state); err != nil {
			return err
		}
		if previous != session || state != "Current transaction finished; WIP committed." {
			t.Fatal("lead successor lost durable checkpoint")
		}
		for child, original := range children {
			var parent string
			if err := tx.QueryRow(t.Context(), `SELECT parent_id::text FROM harness_sessions WHERE id=$1`, child).Scan(&parent); err != nil {
				return err
			}
			if parent != original {
				t.Fatal("lead succession reassigned a worker")
			}
		}
		var adopted int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.adopted'`).Scan(&adopted); err != nil {
			return err
		}
		if adopted != 0 {
			t.Fatal("lead succession adopted unrelated processes")
		}
		return nil
	})
	// A former generation cannot operate the current lead, even on a shared key.
	expect(t, f.call(f.agent, "POST", base+"/lead/pause", map[string]any{"expected_revision": l["revision"], "generation": 1}, lease), 409)
	expect(t, f.call(f.foreign, "GET", base+"/lead", nil, ""), 403)
}

func TestProjectLeadAdmissionIsFreshAndFailClosed(t *testing.T) {
	for _, mode := range []string{"missing", "error", "host_error", "dial_full", "harness_full", "account_unknown", "host_stale", "future", "ready"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			var admission harness.LeadAdmission
			if mode != "missing" {
				admission = func(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner string, s harness.Session) (harness.LeadChecks, error) {
					calls++
					checks, err := readyLeadChecks(ctx, tx, p, owner, s)
					if err != nil {
						return checks, err
					}
					switch mode {
					case "error":
						return checks, errors.New("unreadable adapter")
					case "host_error":
						return checks, &harness.LeadAdmissionError{Gate: "host", Err: errors.New("private diagnostic")}
					case "dial_full":
						checks.Dial.State = "full"
					case "harness_full":
						checks.Harness.State = "full"
					case "account_unknown":
						checks.Account.State = "unknown"
					case "host_stale":
						checks.Host.CheckedAt = checks.Host.CheckedAt.Add(-time.Minute)
					case "future":
						checks.Host.CheckedAt = checks.Host.CheckedAt.Add(time.Minute)
					}
					return checks, nil
				}
			}
			f := projectLeadFixture(t, admission)
			session, lease, _ := leadCandidate(t, f)
			l := startLead(t, f, 0)
			if calls != 0 || l["reason"] != "automatic_launch_disabled" || l["automatic_launch_enabled"] != false || l["session_id"] != nil {
				t.Fatalf("fresh intent invented admission or launch: %v, calls=%d", l, calls)
			}
			l = claimLead(t, f, session, lease, l["revision"])
			wantReason := map[string]string{"missing": "admission_unavailable", "error": "admission_unavailable", "host_error": "host_unavailable", "dial_full": "dial_full", "harness_full": "harness_full", "account_unknown": "account_unavailable", "host_stale": "host_unavailable", "future": "host_unavailable", "ready": ""}[mode]
			if l["reason"] != wantReason || l["automatic_launch_enabled"] != false {
				t.Fatalf("incorrect admission reason or launch policy: %v; want reason %q", l, wantReason)
			}
			if mode == "ready" {
				if l["generation"] != float64(1) {
					t.Fatal("ready claim failed")
				}
			} else {
				if l["state"] != "waiting_for_room" || l["session_id"] != nil || l["generation"] != float64(0) {
					t.Fatalf("gate bypass: %v", l)
				}
			}
			claimLead(t, f, session, lease, l["revision"])
			if admission != nil && calls != 2 {
				t.Fatal("claim reused cached admission")
			}
		})
	}
}

func TestProjectLeadLegacyUnclaimedStartStaysCancellable(t *testing.T) {
	f := projectLeadFixture(t, nil)
	startLead(t, f, 0)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET reason='start_checks_unavailable',updated_at=clock_timestamp()-interval '1 day' WHERE project_id=$1`, f.project)
		return err
	})
	base := "/api/projects/" + f.project + "/lead"
	w := f.call(f.person, "GET", base, nil, "")
	expect(t, w, 200)
	l := decode(t, w)
	if l["reason"] != "automatic_launch_disabled" || l["state"] != "waiting_for_room" || l["revision"] != float64(1) || l["automatic_launch_enabled"] != false {
		t.Fatalf("legacy wait remains misleading: %v", l)
	}
	w = f.call(f.person, "POST", base+"/pause", map[string]any{"expected_revision": 1, "generation": 0}, "")
	expect(t, w, 200)
	if got := decode(t, w); got["state"] != "paused" || got["process_active"] != false {
		t.Fatalf("cancel fabricated a process: %v", got)
	}
}

func TestProjectLeadLostContactDoesNotPermitSuccession(t *testing.T) {
	f := projectLeadFixture(t, readyLeadChecks)
	session, lease, body := leadCandidate(t, f)
	views.New(f.db.App).Mount(f.mux)
	l := startLead(t, f, 0)
	l = claimLead(t, f, session, lease, l["revision"])
	base := "/api/projects/" + f.project
	age(t, f, session, "20 minutes", false)
	body["worker_lease"] = "new-lease-" + uid()
	w := f.call(f.person, "POST", base+"/harness-sessions", body, "")
	expect(t, w, 409)
	if decode(t, w)["error"] != "project lead restart requires fresh reference and explicit claim" {
		t.Fatal("failed for wrong reason")
	}
	// Administrative closure / lost heartbeat is not executor-confirmed exit.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),stop_reason='heartbeat_lost',phase='stopped' WHERE id=$1`, session)
		return err
	})
	next, nextLease, nextBody := leadCandidate(t, f)
	nextBody["succeeds_session_id"] = session
	expect(t, f.call(f.person, "POST", base+"/harness-sessions", nextBody, ""), 409)
	w = f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": l["revision"], "session_id": next}, nextLease)
	expect(t, w, 409)
	if decode(t, w)["error"] != "lead process exit is unconfirmed" {
		t.Fatal("wrong succession refusal")
	}
	w = f.call(f.person, "GET", base+"/lead", nil, "")
	expect(t, w, 200)
	if decode(t, w)["process_active"] != true {
		t.Fatal("lost contact released dial occupancy")
	}
	w = f.call(f.person, "GET", "/api/agents/plan", nil, "")
	expect(t, w, 200)
	// Both the unconfirmed old lead and the new observed process consume room.
	if decode(t, w)["running_total"] != float64(2) {
		t.Fatal("existing single dial dropped lost lead occupancy")
	}
	expect(t, f.call(f.agent, "POST", base+"/harness-sessions/"+session+"/confirm-exit", map[string]any{"reason": "process_exited"}, lease), 200)
	l = claimLead(t, f, next, nextLease, l["revision"])
	if l["generation"] != float64(2) {
		t.Fatal("confirmed successor failed")
	}
	w = f.call(f.person, "GET", "/api/agents/plan", nil, "")
	expect(t, w, 200)
	if decode(t, w)["running_total"] != float64(1) {
		t.Fatal("confirmed stopped lead still consumes room")
	}
	// History prevents resurrection even after the current lead pointer moves.
	body["worker_lease"] = lease
	expect(t, f.call(f.person, "POST", base+"/harness-sessions", body, ""), 409)
}

// Hold the first writer after its tenant fence. Observe the second writer's
// real Postgres lock wait before releasing, proving operations overlapped.
type leadBarrier struct {
	first  atomic.Bool
	locked chan uint32
	resume chan struct{}
}
type leadBarrierKey struct{}

func (b *leadBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "pg_advisory_xact_lock") && strings.Contains(q.SQL, "aeon-pairing:") && b.first.CompareAndSwap(false, true) {
		return context.WithValue(ctx, leadBarrierKey{}, true)
	}
	return ctx
}
func (b *leadBarrier) TraceQueryEnd(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(leadBarrierKey{}) == true && q.Err == nil {
		b.locked <- c.PgConn().PID()
		select {
		case <-b.resume:
		case <-ctx.Done():
		}
	}
}
func TestProjectLeadCompetingStartsClaimsAndRevocation(t *testing.T) {
	for _, mode := range []string{"starts", "claims", "succession", "revocation"} {
		t.Run(mode, func(t *testing.T) {
			f := projectLeadFixture(t, readyLeadChecks)
			var session, lease string
			var l map[string]any
			if mode != "starts" {
				session, lease, _ = leadCandidate(t, f)
				l = startLead(t, f, 0)
			}
			var secondSession, secondLease string
			if mode == "succession" {
				l = claimLead(t, f, session, lease, l["revision"])
				expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
				session, lease, _ = leadCandidate(t, f)
			}
			if mode == "claims" || mode == "succession" {
				secondSession, secondLease, _ = leadCandidate(t, f)
			}
			barrier := &leadBarrier{locked: make(chan uint32, 1), resume: make(chan struct{})}
			cfg := f.db.App.Config()
			cfg.ConnConfig.Tracer = barrier
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			f.mux = http.NewServeMux()
			harness.NewWithLeadAdmission(pool, readyLeadChecks).Mount(f.mux)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			defer func() {
				select {
				case <-barrier.resume:
				default:
					close(barrier.resume)
				}
			}()
			done := make(chan *httptest.ResponseRecorder, 2)
			action := func(second bool) {
				path := "/api/projects/" + f.project + "/lead"
				body := map[string]any{"expected_revision": 0}
				p := f.person
				proof := ""
				if mode != "starts" {
					path += "/claim"
					body = map[string]any{"expected_revision": l["revision"], "session_id": session}
					p = f.agent
					proof = lease
					if second && secondSession != "" {
						body["session_id"] = secondSession
						proof = secondLease
					}
				}
				done <- f.call(p, "POST", path, body, proof)
			}
			if mode == "revocation" {
				go func() {
					err := db.InTenant(dbtest.Seed(ctx), pool, f.person.TenantID, func(tx pgx.Tx) error {
						if err := db.LockWorkTreeTx(ctx, tx); err != nil {
							return err
						}
						_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
						return err
					})
					w := httptest.NewRecorder()
					if err != nil {
						w.Code = 500
					} else {
						w.Code = 200
					}
					done <- w
				}()
			} else {
				go action(false)
			}
			var pid uint32
			select {
			case pid = <-barrier.locked:
			case <-ctx.Done():
				t.Fatal("first writer did not enter fence")
			}
			go action(true)
			for {
				var waiting bool
				err = f.db.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
			}
			close(barrier.resume)
			var first, second *httptest.ResponseRecorder
			select {
			case first = <-done:
			case <-ctx.Done():
				t.Fatal("first mutation did not finish")
			}
			select {
			case second = <-done:
			case <-ctx.Done():
				t.Fatal("competing mutation did not finish")
			}
			if first.Code != 200 {
				first, second = second, first
			}
			expect(t, first, 200)
			if mode == "revocation" {
				expect(t, second, 403)
				if !strings.Contains(second.Body.String(), "permission") {
					t.Fatal("revocation failed for wrong reason")
				}
			} else {
				expect(t, second, 409)
				if decode(t, second)["error"] != "lead revision conflict" {
					t.Fatal("competing writer failed for wrong reason")
				}
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var count, generation int
				if err := tx.QueryRow(t.Context(), `SELECT count(*),coalesce(max(generation),0) FROM project_leads WHERE project_id=$1`, f.project).Scan(&count, &generation); err != nil {
					return err
				}
				if count != 1 || mode == "claims" && generation != 1 || mode == "succession" && generation != 2 || mode == "revocation" && generation != 0 {
					t.Fatal("competing mutation crossed singleton fence")
				}
				return nil
			})
		})
	}
}

func TestProjectLeadAssignmentFenceAndArchive(t *testing.T) {
	f := projectLeadFixture(t, readyLeadChecks)
	session, lease, _ := leadCandidate(t, f)
	l := startLead(t, f, 0)
	l = claimLead(t, f, session, lease, l["revision"])
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, lease), 200)
	binding, _ := json.Marshal(map[string]any{"session_id": session, "generation": 1})
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		return harness.RequireAssignedLeadTx(t.Context(), tx, f.agent, f.project, binding)
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='archived' WHERE id=$1`, f.project)
		return err
	})
	w := f.call(f.person, "GET", "/api/projects/"+f.project+"/lead", nil, "")
	expect(t, w, 200)
	if got := decode(t, w); got["state"] != "cannot_start" || got["reason"] != "project_archived" || got["process_active"] != true {
		t.Fatalf("archive=%v", got)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		return harness.RequireAssignedLeadTx(t.Context(), tx, f.agent, f.project, binding)
	})
	if err == nil || err.Error() != "assignment lead generation unavailable" {
		t.Fatalf("archive assignment guard=%v", err)
	}
	// Historical rows and unrelated workers remain untouched.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var stopped bool
		if err := tx.QueryRow(t.Context(), `SELECT stopped_at IS NOT NULL FROM harness_sessions WHERE id=$1`, session).Scan(&stopped); err != nil {
			return err
		}
		if stopped {
			t.Fatal("archive fabricated process exit")
		}
		return nil
	})
}

func TestProjectLeadOpenAPIContract(t *testing.T) {
	for _, file := range []string{"../../api/openapi.yaml", "openapi.yaml"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err = yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		prefix := ""
		if file == "openapi.yaml" {
			prefix = "/api"
		}
		paths := doc["paths"].(map[string]any)
		for _, suffix := range []string{"/lead", "/lead/claim", "/lead/pause", "/lead/yield"} {
			path := prefix + "/projects/{projectId}" + suffix
			operation, ok := paths[path].(map[string]any)
			if !ok {
				t.Fatalf("missing %s in %s", path, file)
			}
			post := operation["post"].(map[string]any)
			body := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if body["additionalProperties"] != false {
				t.Fatal("lead write body permits undocumented authorities")
			}
			required := body["required"].([]any)
			if len(required) == 0 || required[0] != "expected_revision" {
				t.Fatal("lead writes lost revision contract")
			}
		}
		remove := paths[prefix+"/projects/{projectId}/lead"].(map[string]any)["delete"].(map[string]any)
		body := remove["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if body["additionalProperties"] != false || body["required"].([]any)[0] != "expected_revision" {
			t.Fatal("lead removal lost its strict revision contract")
		}
		lead := doc["components"].(map[string]any)["schemas"].(map[string]any)["ProjectLead"].(map[string]any)
		properties := lead["properties"].(map[string]any)
		if len(properties["state"].(map[string]any)["enum"].([]any)) != 6 {
			t.Fatal("lead lifecycle states missing")
		}
		for _, private := range []string{"worker_lease", "account_id", "owner_principal_id"} {
			if _, ok := properties[private]; ok {
				t.Fatal("public lead contract exposes private identity or proof")
			}
		}
	}
}

func TestProjectLeadReportingCadenceAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		age   time.Duration
		fresh bool
	}{
		{"before_default_beat", 49 * time.Second, true},
		{"default_beat", 50 * time.Second, true},
		{"one_missed_beat", 100 * time.Second, true},
		{"freshness_boundary", 120 * time.Second, true},
		{"expired", 120*time.Second + time.Microsecond, false},
		{"future", -time.Microsecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := projectLeadFixture(t, readyLeadChecks)
			session, lease, _ := leadCandidate(t, f)
			l := startLead(t, f, 0)
			now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='working',heartbeat_at=$2 WHERE id=$1`, session, now.Add(-tc.age))
				return err
			})
			raw, _ := json.Marshal(map[string]any{"expected_revision": l["revision"], "session_id": session})
			r := httptest.NewRequest("POST", "/api/projects/"+f.project+"/lead/claim", strings.NewReader(string(raw)))
			r.SetPathValue("projectId", f.project)
			r.Header.Set("X-Aeon-Worker-Lease", lease)
			err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
				_, err := harness.ClaimLeadInTx(f.db.App, readyLeadChecks, r, harness.FreezeLeadReportingClock(tx, now), f.agent)
				return err
			})
			if tc.fresh {
				if err != nil {
					t.Fatalf("healthy cadence rejected: %v", err)
				}
			} else {
				if err == nil || err.Error() != "fresh reporting generation required" {
					t.Fatalf("stale claim rejected for wrong reason: %v", err)
				}
				// Set an existing working lead to exercise projection/dispatch as well.
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE project_leads SET session_id=$2,generation=1,state='working' WHERE project_id=$1`, f.project, session)
					return err
				})
			}
			binding, _ := json.Marshal(map[string]any{"session_id": session, "generation": 1})
			err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
				return harness.RequireAssignedLeadTx(t.Context(), harness.FreezeLeadReportingClock(tx, now), f.agent, f.project, binding)
			})
			if tc.fresh && err != nil {
				t.Fatalf("healthy worker assignment rejected: %v", err)
			}
			if !tc.fresh && (err == nil || err.Error() != "assignment lead generation unavailable") {
				t.Fatalf("stale assignment rejected for wrong reason: %v", err)
			}
			err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
				return harness.RequireCurrentLeadTx(t.Context(), harness.FreezeLeadReportingClock(tx, now), f.agent, f.project, session, 1)
			})
			if tc.fresh && err != nil {
				t.Fatalf("healthy dispatch rejected: %v", err)
			}
			if !tc.fresh && (err == nil || err.Error() != "lead generation unavailable") {
				t.Fatalf("stale dispatch rejected for wrong reason: %v", err)
			}
		})
	}
}

// Risk: adoption must retain the live process while requiring both person
// confirmation and the exact generation lease, including on a server whose
// automatic launch admission is deliberately absent.
func TestProjectLeadAdoptionNeedsPersonAndExactLease(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_record", true: "existing_intent"}[existing], func(t *testing.T) {
			f := projectLeadFixture(t, nil)
			base := "/api/projects/" + f.project
			session, lease, registration := leadCandidate(t, f)
			other, otherLease, _ := leadCandidate(t, f)
			expect(t, f.call(f.agent, "POST", base+"/harness-sessions/"+session+"/heartbeat", map[string]any{"activity_sequence": 1, "phase": "working", "activity": "busy"}, lease), 200)
			revision := any(0)
			if existing {
				revision = startLead(t, f, 0)["revision"]
			}
			w := f.call(f.person, "GET", base+"/lead/candidates?limit=1", nil, "")
			expect(t, w, 200)
			page := decode(t, w)
			if len(page["items"].([]any)) != 1 || page["next_cursor"] == nil {
				t.Fatal("eligible coordinators were not paged")
			}
			w = f.call(f.person, "GET", base+"/lead/candidates?limit=1&cursor="+page["next_cursor"].(string), nil, "")
			expect(t, w, 200)
			if len(decode(t, w)["items"].([]any)) != 1 || decode(t, w)["next_cursor"] != nil {
				t.Fatal("exclusive candidate cursor lost or repeated rows")
			}
			expect(t, f.call(f.agent, "POST", base+"/lead/adopt", map[string]any{"expected_revision": revision, "session_id": session}, lease), 403)
			w = f.call(f.person, "POST", base+"/lead/adopt", map[string]any{"expected_revision": revision, "session_id": session}, "")
			expect(t, w, 200)
			pending := decode(t, w)
			if pending["reason"] != "adoption_pending" || pending["generation"] != float64(0) || pending["session_id"] != session {
				t.Fatalf("selection granted a generation: %v", pending)
			}
			expect(t, f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": pending["revision"], "session_id": other}, otherLease), 409)
			expect(t, f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": pending["revision"], "session_id": session}, "incorrect-private-lease-at-least-32-chars"), 403)
			w = f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": pending["revision"], "harness_session_ref": registration["harness_session_ref"]}, lease)
			expect(t, w, 200)
			bound := decode(t, w)
			if bound["session_id"] != session || bound["generation"] != float64(1) || bound["state"] != "working" || bound["process_active"] != true {
				t.Fatalf("adoption restarted or failed: %v", bound)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var owner, dispatch string
				var generations, controls int
				var stopped bool
				if err := tx.QueryRow(t.Context(), `SELECT owner_principal_id::text,dispatch_key_id::text FROM project_leads WHERE project_id=$1`, f.project).Scan(&owner, &dispatch); err != nil {
					return err
				}
				if owner != f.person.ID || dispatch != f.agent.AuthKeyID {
					t.Fatal("wrong owner or unproven dispatch key")
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM project_lead_generations WHERE project_id=$1 AND session_id=$2`, f.project, session).Scan(&generations); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1`, session).Scan(&controls); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT stopped_at IS NOT NULL FROM harness_sessions WHERE id=$1`, session).Scan(&stopped); err != nil {
					return err
				}
				if generations != 1 || controls != 0 || stopped {
					t.Fatal("adoption changed the process or lost generation history")
				}
				var audit string
				if err := tx.QueryRow(t.Context(), `SELECT jsonb_agg(jsonb_build_object('type',type,'before',before,'after',after))::text FROM events WHERE type IN ('lead.adoption_requested','lead.claim_checked')`).Scan(&audit); err != nil {
					return err
				}
				if !strings.Contains(audit, "lead.adoption_requested") || !strings.Contains(audit, "lead.claim_checked") || strings.Contains(audit, lease) || strings.Contains(audit, registration["harness_session_ref"].(string)) {
					t.Fatal("adoption audit missing or private proof exposed")
				}
				return nil
			})
			// Adoption does not enable launch admission or change ordinary reclaims.
			again := claimLead(t, f, session, lease, bound["revision"])
			if again["state"] != "waiting_for_room" || again["reason"] != "admission_unavailable" || again["generation"] != float64(1) || again["automatic_launch_enabled"] != false {
				t.Fatal("ordinary claim admission was weakened")
			}
		})
	}
}

func TestProjectLeadAdoptionRejectsChangedEligibility(t *testing.T) {
	for _, mode := range []string{"worker", "child", "unowned", "other_owner", "stale", "future", "archived", "stopped", "pausing", "revoked", "stale_after_confirmation", "pausing_after_confirmation", "revoked_after_confirmation"} {
		t.Run(mode, func(t *testing.T) {
			f := projectLeadFixture(t, nil)
			session, lease, _ := leadCandidate(t, f)
			base := "/api/projects/" + f.project
			confirmed := strings.HasSuffix(mode, "_after_confirmation")
			if confirmed {
				expect(t, f.call(f.person, "POST", base+"/lead/adopt", map[string]any{"expected_revision": 0, "session_id": session}, ""), 200)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				q := ""
				switch strings.TrimSuffix(mode, "_after_confirmation") {
				case "worker":
					q = "UPDATE harness_sessions SET role='worker' WHERE id=$1"
				case "child":
					parent, _, _ := leadCandidate(t, f)
					_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET parent_id=$2 WHERE id=$1`, session, parent)
					return err
				case "unowned":
					q = "UPDATE harness_sessions SET owner_principal_id=NULL WHERE id=$1"
				case "other_owner":
					otherOwner := uid()
					_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','other')`, f.person.TenantID, otherOwner)
					if err != nil {
						return err
					}
					_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, session, otherOwner)
					return err
				case "stale":
					q = "UPDATE harness_sessions SET heartbeat_at=clock_timestamp()-interval '3 minutes' WHERE id=$1"
				case "future":
					q = "UPDATE harness_sessions SET heartbeat_at=clock_timestamp()+interval '1 minute' WHERE id=$1"
				case "archived":
					q = `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),archived_at=clock_timestamp(),
						recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture'::bytea,
						recovery_actor_id=agent_principal_id,recovery_reason='fixture' WHERE id=$1`
				case "stopped":
					q = "UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason='stopped' WHERE id=$1"
				case "pausing":
					q = `UPDATE harness_sessions SET pause_record='{"state":"requested"}' WHERE id=$1`
				case "revoked":
					_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
					return err
				}
				_, err := tx.Exec(t.Context(), q, session)
				return err
			})
			var w *httptest.ResponseRecorder
			if confirmed {
				w = f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": 1, "session_id": session}, lease)
			} else {
				w = f.call(f.person, "POST", base+"/lead/adopt", map[string]any{"expected_revision": 0, "session_id": session}, "")
			}
			if strings.HasPrefix(mode, "revoked") {
				expect(t, w, 403)
			} else {
				expect(t, w, 409)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var count int
				err := tx.QueryRow(t.Context(), `SELECT count(*) FROM project_leads WHERE project_id=$1`, f.project).Scan(&count)
				want := 0
				if confirmed {
					want = 1
				}
				if count != want {
					t.Fatal("rejected adoption changed lead intent storage")
				}
				return err
			})
		})
	}
}

func harnessShape(t *testing.T, f *harnessFixture, session, child string) string {
	t.Helper()
	var shape string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT concat_ws('|',
			coalesce(parent_id::text,'root'),
			coalesce(handed_over_to_id::text,'none'),
			coalesce(adopted_from_id::text,'none'),
			(stopped_at IS NOT NULL)::text,
			coalesce(pause_record->>'state',''),
			phase,
			(SELECT count(*)::text FROM harness_controls WHERE session_id=$1),
			(SELECT coalesce(parent_id::text,'missing') FROM harness_sessions WHERE id=$2)
		) FROM harness_sessions WHERE id=$1`, session, child).Scan(&shape)
	})
	return shape
}

func leadStatus(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	expect(t, w, status)
	if decode(t, w)["error"] != message {
		t.Fatalf("error %v want %s", decode(t, w)["error"], message)
	}
}

// Risk: confirming an adoption stores the session immediately. Pause must not
// run against that still-live process, and Cancel must drop only the selection.
func TestProjectLeadUnclaimedAdoptionRejectsPauseAndCancelClearsSelection(t *testing.T) {
	t.Run("pause_rejected_and_cancel_clears", func(t *testing.T) {
		f := projectLeadFixture(t, nil)
		base := "/api/projects/" + f.project
		session, lease, registration := leadCandidate(t, f)
		other, otherLease, _ := leadCandidate(t, f)
		expect(t, f.call(f.agent, "POST", base+"/harness-sessions/"+session+"/heartbeat", map[string]any{"activity_sequence": 1, "phase": "working", "activity": "busy"}, lease), 200)
		expect(t, f.call(f.agent, "POST", base+"/harness-sessions/"+other+"/heartbeat", map[string]any{"activity_sequence": 1, "phase": "working", "activity": "busy"}, otherLease), 200)
		childBody := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "parent_harness_session_id": session, "harness_session_ref": "child-ref-" + uid(), "worker_lease": "child-lease-" + uid()}
		childW := f.call(f.person, "POST", base+"/harness-sessions", childBody, "")
		expect(t, childW, 201)
		child := decode(t, childW)["id"].(string)
		w := f.call(f.person, "POST", base+"/lead/adopt", map[string]any{"expected_revision": 0, "session_id": session}, "")
		expect(t, w, 200)
		pending := decode(t, w)
		before := harnessShape(t, f, session, child)
		w = f.call(f.person, "POST", base+"/lead/pause", map[string]any{"expected_revision": pending["revision"], "generation": 0}, "")
		leadStatus(t, w, 409, "unclaimed adoption cannot be paused")
		w = f.call(f.agent, "POST", base+"/lead/pause", map[string]any{"expected_revision": pending["revision"], "generation": 0}, "incorrect-private-lease-at-least-32-chars")
		leadStatus(t, w, 403, "current lead worker proof required")
		w = f.call(f.agent, "POST", base+"/lead/pause", map[string]any{"expected_revision": pending["revision"], "generation": 0}, lease)
		leadStatus(t, w, 409, "unclaimed adoption cannot be paused")
		if harnessShape(t, f, session, child) != before {
			t.Fatal("rejected pause changed the live session")
		}
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var reason string
			var generation int
			var selected *string
			var generations, paused int
			if err := tx.QueryRow(t.Context(), `SELECT reason,generation,session_id::text FROM project_leads WHERE project_id=$1`, f.project).Scan(&reason, &generation, &selected); err != nil {
				return err
			}
			if reason != "adoption_pending" || generation != 0 || selected == nil || *selected != session {
				t.Fatal("rejected pause changed the unclaimed selection")
			}
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM project_lead_generations WHERE project_id=$1`, f.project).Scan(&generations); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type IN ('lead_paused','harness.lead_paused')`).Scan(&paused); err != nil {
				return err
			}
			if generations != 0 || paused != 0 {
				t.Fatal("rejected pause wrote a generation or a pause event")
			}
			return nil
		})
		w = f.call(f.person, "POST", base+"/lead/adopt/cancel", map[string]any{"expected_revision": 0}, "")
		leadStatus(t, w, 409, "lead revision conflict")
		w = f.call(f.agent, "POST", base+"/lead/adopt/cancel", map[string]any{"expected_revision": pending["revision"]}, lease)
		leadStatus(t, w, 403, "person required to adopt lead")
		w = f.call(f.person, "POST", base+"/lead/adopt/cancel", map[string]any{"expected_revision": pending["revision"]}, "")
		expect(t, w, 200)
		cleared := decode(t, w)
		if cleared["session_id"] != nil || cleared["reason"] != "selection_cleared" || cleared["generation"] != float64(0) || cleared["state"] != "waiting_for_room" || cleared["revision"].(float64) <= pending["revision"].(float64) {
			t.Fatalf("cancel did not clear only the selection: %v", cleared)
		}
		if harnessShape(t, f, session, child) != before {
			t.Fatal("cancel paused, stopped or reparented the session")
		}
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var reason string
			var generation int
			var sessionID, dispatch *string
			if err := tx.QueryRow(t.Context(), `SELECT reason,generation,session_id::text,dispatch_key_id::text FROM project_leads WHERE project_id=$1`, f.project).Scan(&reason, &generation, &sessionID, &dispatch); err != nil {
				return err
			}
			if reason != "selection_cleared" || generation != 0 || sessionID != nil || dispatch != nil {
				t.Fatal("cleared selection kept a session or a dispatch key")
			}
			var audit string
			if err := tx.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(jsonb_build_object('type',type,'before',before,'after',after))::text,'') FROM events WHERE type='lead.adoption_cancelled'`).Scan(&audit); err != nil {
				return err
			}
			if !strings.Contains(audit, "lead.adoption_cancelled") || strings.Contains(audit, lease) || strings.Contains(audit, registration["harness_session_ref"].(string)) {
				t.Fatal("cancel audit missing or private proof exposed")
			}
			return nil
		})
		w = f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": cleared["revision"], "session_id": session}, lease)
		leadStatus(t, w, 409, "explicit start request required after cancelled adoption")
		w = f.call(f.person, "POST", base+"/lead/adopt", map[string]any{"expected_revision": cleared["revision"], "session_id": other}, "")
		expect(t, w, 200)
		again := decode(t, w)
		if again["session_id"] != other || again["reason"] != "adoption_pending" || again["generation"] != float64(0) {
			t.Fatalf("another adoption did not proceed: %v", again)
		}
		w = f.call(f.person, "POST", base+"/lead/adopt/cancel", map[string]any{"expected_revision": again["revision"]}, "")
		expect(t, w, 200)
		cleared = decode(t, w)
		started := startLead(t, f, int(cleared["revision"].(float64)))
		if started["reason"] != "automatic_launch_disabled" || started["automatic_launch_enabled"] != false || started["session_id"] != nil || started["generation"] != float64(0) || started["state"] != "waiting_for_room" || started["revision"].(float64) <= cleared["revision"].(float64) {
			t.Fatalf("explicit start after cancel failed: %v", started)
		}
		// The read projection reports launch policy; the stored intent remains an
		// explicit ordinary start, so cancellation does not fence its later claim.
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var reason string
			if err := tx.QueryRow(t.Context(), `SELECT reason FROM project_leads WHERE project_id=$1`, f.project).Scan(&reason); err != nil {
				return err
			}
			if reason != "awaiting_generation" {
				t.Fatal("explicit start retained the cancelled adoption reason")
			}
			return nil
		})
		if harnessShape(t, f, session, child) != before {
			t.Fatal("start after cancel touched the original session")
		}
	})
	t.Run("existing_intent", func(t *testing.T) {
		f := projectLeadFixture(t, nil)
		base := "/api/projects/" + f.project
		session, lease, _ := leadCandidate(t, f)
		expect(t, f.call(f.agent, "POST", base+"/harness-sessions/"+session+"/heartbeat", map[string]any{"activity_sequence": 1, "phase": "working", "activity": "busy"}, lease), 200)
		childW := f.call(f.person, "POST", base+"/harness-sessions", map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "parent_harness_session_id": session, "harness_session_ref": "child-ref-" + uid(), "worker_lease": "child-lease-" + uid()}, "")
		expect(t, childW, 201)
		child := decode(t, childW)["id"].(string)
		revision := startLead(t, f, 0)["revision"]
		w := f.call(f.person, "POST", base+"/lead/adopt", map[string]any{"expected_revision": revision, "session_id": session}, "")
		expect(t, w, 200)
		pending := decode(t, w)
		before := harnessShape(t, f, session, child)
		w = f.call(f.person, "POST", base+"/lead/pause", map[string]any{"expected_revision": pending["revision"], "generation": 0}, "")
		leadStatus(t, w, 409, "unclaimed adoption cannot be paused")
		w = f.call(f.person, "POST", base+"/lead/adopt/cancel", map[string]any{"expected_revision": pending["revision"]}, "")
		expect(t, w, 200)
		cleared := decode(t, w)
		if cleared["session_id"] != nil || cleared["reason"] != "selection_cleared" || harnessShape(t, f, session, child) != before {
			t.Fatal("cancel of an existing intent changed the session")
		}
	})
	t.Run("claimed_adoption_can_pause_and_cancel_refuses", func(t *testing.T) {
		f := projectLeadFixture(t, nil)
		base := "/api/projects/" + f.project
		session, lease, _ := leadCandidate(t, f)
		expect(t, f.call(f.agent, "POST", base+"/harness-sessions/"+session+"/heartbeat", map[string]any{"activity_sequence": 1, "phase": "working", "activity": "busy"}, lease), 200)
		w := f.call(f.person, "POST", base+"/lead/adopt", map[string]any{"expected_revision": 0, "session_id": session}, "")
		expect(t, w, 200)
		pending := decode(t, w)
		w = f.call(f.agent, "POST", base+"/lead/claim", map[string]any{"expected_revision": pending["revision"], "session_id": session}, lease)
		expect(t, w, 200)
		bound := decode(t, w)
		if bound["state"] != "working" || bound["generation"] != float64(1) || bound["session_id"] != session {
			t.Fatalf("claim did not bind the adoption: %v", bound)
		}
		w = f.call(f.person, "POST", base+"/lead/adopt/cancel", map[string]any{"expected_revision": bound["revision"]}, "")
		leadStatus(t, w, 409, "unclaimed adoption required")
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var selected string
			if err := tx.QueryRow(t.Context(), `SELECT session_id::text FROM project_leads WHERE project_id=$1`, f.project).Scan(&selected); err != nil {
				return err
			}
			if selected != session {
				t.Fatal("cancel of a claimed generation cleared its session")
			}
			return nil
		})
		w = f.call(f.person, "POST", base+"/lead/pause", map[string]any{"expected_revision": bound["revision"], "generation": bound["generation"]}, "")
		expect(t, w, 200)
		if decode(t, w)["state"] != "paused" {
			t.Fatal("a claimed generation could not pause")
		}
	})
}
