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

func leadFixture(t *testing.T, admission harness.LeadAdmission) *harnessFixture {
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
	f := leadFixture(t, readyLeadChecks)
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
	for _, mode := range []string{"missing", "error", "dial_full", "harness_full", "account_unknown", "host_stale", "future", "ready"} {
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
			f := leadFixture(t, admission)
			session, lease, _ := leadCandidate(t, f)
			l := startLead(t, f, 0)
			l = claimLead(t, f, session, lease, l["revision"])
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

func TestProjectLeadLostContactDoesNotPermitSuccession(t *testing.T) {
	f := leadFixture(t, readyLeadChecks)
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
			f := leadFixture(t, readyLeadChecks)
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
	f := leadFixture(t, readyLeadChecks)
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
		for _, suffix := range []string{"/lead", "/lead/claim", "/lead/pause"} {
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
			f := leadFixture(t, readyLeadChecks)
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
