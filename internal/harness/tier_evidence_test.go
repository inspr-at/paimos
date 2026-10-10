// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func tierHistoryActions(t *testing.T, f *harnessFixture, path string) []string {
	t.Helper()
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	rows := decode(t, w)["history"].([]any)
	out := []string{}
	for _, item := range rows {
		h := item.(map[string]any)
		out = append(out, h["action"].(string))
		if h["actor_id"] == nil || h["actor_name"] == "" || h["at"] == nil {
			t.Fatal("history omitted attribution", h)
		}
	}
	return out
}

// Fail at the database boundary if a tier history write follows an audit
// insert in the same transaction. This proves the event-counter fence without
// relying on timing or a concurrent test accidentally avoiding the race.
func tierHistoryEventFence(t *testing.T, f *harnessFixture) {
	t.Helper()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION tier_history_event_fence() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
  IF EXISTS(SELECT 1 FROM events WHERE tenant_id=NEW.tenant_id AND xmin=pg_current_xact_id()::xid) THEN
   RAISE EXCEPTION 'tier history must precede the tenant event counter';
  END IF;
  RETURN NEW;
 END $$;
 CREATE TRIGGER tier_history_event_fence BEFORE INSERT ON harness_tier_history FOR EACH ROW EXECUTE FUNCTION tier_history_event_fence()`)
		return err
	})
}

func TestTierHistoryPreservesEachTransitionAndUndo(t *testing.T) {
	f := fixture(t)
	tierHistoryEventFence(t, f)
	path, lease, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	ask := map[string]any{"request_id": uid(), "tier": "fast", "reason": "QA waits"}
	expect(t, f.call(f.agent, "POST", path+"/tier/ask", ask, ""), 201)
	decide := tierChangeBody(t, f, path, "fast", identity)
	decide["decision"] = "decline"
	expect(t, f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, ""), 201)
	expect(t, f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, ""), 201)
	ask["request_id"] = uid()
	expect(t, f.call(f.agent, "POST", path+"/tier/ask", ask, ""), 201)
	decide = tierChangeBody(t, f, path, "fast", identity)
	decide["decision"] = "approve"
	w := f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, "")
	expect(t, w, 201)
	control := decode(t, w)["pending"].(map[string]any)["id"].(string)
	undo := tierChangeBody(t, f, path, "default", identity)
	undo["undo_of_control_id"] = control
	expect(t, f.call(f.person, "POST", path+"/tier", undo, ""), 201)
	expect(t, f.call(f.person, "POST", path+"/tier", undo, ""), 201)
	got := tierHistoryActions(t, f, path)
	want := []string{"cancelled", "approved", "requested", "declined", "requested"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cancel/replay lost history: %v", got)
	}
	// The same restored request is approved again, then confirmed and reversed.
	decide = tierChangeBody(t, f, path, "fast", identity)
	decide["decision"] = "approve"
	w = f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, "")
	expect(t, w, 201)
	control = decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	undo = tierChangeBody(t, f, path, "default", identity)
	undo["undo_of_control_id"] = control
	w = f.call(f.person, "POST", path+"/tier", undo, "")
	expect(t, w, 201)
	reverse := decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+reverse+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	got = tierHistoryActions(t, f, path)
	want = append([]string{"undone", "undo_requested", "changed", "approved"}, want...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("confirmed Undo history: %v", got)
	}
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	changed := decode(t, w)["history"].([]any)[2].(map[string]any)
	if changed["actor_id"] != f.person.ID || changed["asked_by_name"] == nil || changed["from_tier"] != "default" || changed["to_tier"] != "fast" {
		t.Fatal("confirmation lost person/agent", changed)
	}
	expect(t, f.call(f.foreign, "GET", path+"/tier", nil, ""), 404)
	wrong := tierChangeBody(t, f, path, "fast", identity)
	wrong["undo_of_control_id"] = control
	expect(t, f.call(f.person, "POST", path+"/tier", wrong, ""), 409)
}
func TestTierEstimateHasSourceAndHonestEmptyTime(t *testing.T) {
	f := fixture(t)
	path, lease, _ := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if decode(t, w)["estimates"].([]any)[0].(map[string]any)["n"] != float64(0) {
		t.Fatal("invented sample")
	}
	expect(t, f.call(f.person, "POST", "/api/model-prices", map[string]any{"model": "fixture-model", "version": 1, "input_usd_per_million": "1", "output_usd_per_million": "1", "cached_input_usd_per_million": "1"}, ""), 201)
	in := map[string]any{"report_id": uid(), "model": "fixture-model", "sequence": 1, "service_tier": "default", "input_tokens": 2400000, "output_tokens": 0, "cached_input_tokens": 0, "provisional": false, "billing_mode": "api", "model_time_ms": 600000}
	expect(t, f.call(f.agent, "POST", path+"/usage", in, lease), 200)
	sessionID := path[strings.LastIndex(path, "/")+1:]
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed',started_at='2026-10-01 00:00:00+00',ended_at='2026-10-01 00:15:00+00' WHERE id=(SELECT run_id FROM harness_sessions WHERE id=$1)`, sessionID)
		return err
	})
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	state := decode(t, w)
	e := state["estimates"].([]any)[1].(map[string]any)
	if e["n"] != float64(1) || e["run_id"] == nil || e["cost_usd"] != "4.800000000000" || !strings.Contains(e["basis"].(string), "frozen price version 1") || e["duration_ms"] != nil {
		t.Fatal("source/unknown speed", e)
	}
	defaultEstimate := state["estimates"].([]any)[0].(map[string]any)
	if defaultEstimate["actual"] != true || defaultEstimate["cost_usd"] != "2.400000000000" || !strings.Contains(defaultEstimate["basis"].(string), "Last run: 2400000 tokens, $2.400000000000, 15.0 min (10.0 min model time) at Default.") {
		t.Fatal("missing measured sample details", defaultEstimate)
	}
	if e["actual"] != false {
		t.Fatal("projected tier marked actual", e)
	}
	if defaultEstimate["duration_ms"] != float64(900000) {
		t.Fatal("default changed tools/waits", defaultEstimate)
	}
	unpriced := state["estimates"].([]any)[2].(map[string]any)
	if unpriced["n"] != float64(0) || unpriced["cost_usd"] != nil {
		t.Fatal("invented unpriced estimate", unpriced)
	}
	// A later catalog revision does not reprice the sample's frozen base cost.
	expect(t, f.call(f.person, "POST", "/api/model-prices", map[string]any{"model": "fixture-model", "version": 2, "input_usd_per_million": "99", "output_usd_per_million": "99", "cached_input_usd_per_million": "99"}, ""), 201)
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if decode(t, w)["estimates"].([]any)[1].(map[string]any)["cost_usd"] != e["cost_usd"] {
		t.Fatal("catalog repriced history")
	}
	in["report_id"] = uid()
	in["sequence"] = 2
	in["model_time_ms"] = 599999
	expect(t, f.call(f.agent, "POST", path+"/usage", in, lease), 409)
}

func TestTierHistoryExpiryAndBoundedRead(t *testing.T) {
	f := fixture(t)
	tierHistoryEventFence(t, f)
	path, _, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	w := f.call(f.person, "POST", path+"/tier", tierChangeBody(t, f, path, "fast", identity), "")
	expect(t, w, 201)
	control := decode(t, w)["pending"].(map[string]any)["id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_controls SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, control)
		return err
	})
	got := tierHistoryActions(t, f, path)
	if !reflect.DeepEqual(got, []string{"rejected", "switch_requested"}) {
		t.Fatal("expiry history missing", got)
	}
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["history"].([]any)) != 2 {
		t.Fatal("expiry replay duplicated history")
	}
	sessionID := path[strings.LastIndex(path, "/")+1:]
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_tier_history(tenant_id,session_id,action,from_tier,to_tier,actor_id) SELECT $1,$2,'requested','default','fast',$3 FROM generate_series(1,55)`, f.person.TenantID, sessionID, f.agent.ID)
		return err
	})
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["history"].([]any)) != 50 || decode(t, w)["history_truncated"] != true {
		t.Fatal("history unbounded or silent truncation")
	}
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_tier_history`).Scan(&n)
		if n != 0 {
			t.Fatal("cross-tenant tier history exposed")
		}
		return err
	})
}

func TestTierAuditFailureRollsBackDecisionAndHistory(t *testing.T) {
	f := fixture(t)
	path, _, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	before := tierChangeBody(t, f, path, "fast", identity)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_tier_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
  IF NEW.type='harness.control_requested' THEN RAISE EXCEPTION 'audit unavailable' USING ERRCODE='XX000'; END IF;
  RETURN NEW;
 END $$;
 CREATE TRIGGER reject_tier_audit BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_tier_audit()`)
		return err
	})
	expect(t, f.call(f.person, "POST", path+"/tier", before, ""), 500)
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	s := decode(t, w)
	if s["pending"] != nil || len(s["history"].([]any)) != 0 || s["revision"] != before["expected_revision"] {
		t.Fatal("failed audit committed a tier decision", s)
	}
}

func TestTierEvidenceSampleLookupUsesPartialIndex(t *testing.T) {
	f := fixture(t)
	path, _, _ := tierSession(t, f)
	sessionID := path[strings.LastIndex(path, "/")+1:]
	f.tx(t, f.person, func(tx pgx.Tx) error {
		// A one-row table makes every tenant-leading index equally selective,
		// so a new unrelated index can win the planner's cost tie. Keep the
		// indexed sample and add matching generations without runs: only the
		// partial index can exclude them before reading the table.
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions
   (tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,model,reasoning_effort)
   SELECT tenant_id,project_id,agent_principal_id,harness,host,management,role,
    decode(md5(n::text),'hex'),lease_digest,model,reasoning_effort
   FROM harness_sessions CROSS JOIN generate_series(1,256) n WHERE id=$1`, sessionID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `ANALYZE harness_sessions`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`); err != nil {
			return err
		}
		var plan string
		err := tx.QueryRow(t.Context(), `EXPLAIN (FORMAT JSON) SELECT id FROM harness_sessions
   WHERE tenant_id=$1 AND project_id=$2 AND agent_principal_id=$3 AND harness='codex' AND model='fixture-model'
   AND reasoning_effort IS NOT DISTINCT FROM 'high' AND run_id IS NOT NULL`, f.person.TenantID, f.project, f.agent.ID).Scan(&plan)
		if err != nil {
			return err
		}
		if !strings.Contains(plan, "harness_sessions_tier_sample") {
			t.Fatal("sample identity has no usable partial index", plan)
		}
		var found string
		err = tx.QueryRow(t.Context(), `SELECT id::text FROM harness_sessions WHERE id=$1 AND run_id IS NOT NULL`, sessionID).Scan(&found)
		if err == nil && found != sessionID {
			t.Fatal("fixture lost indexed sample")
		}
		return err
	})
}

func TestTierHistoryEventFenceFiresAcrossXIDEpochs(t *testing.T) {
	for _, nextEpoch := range []bool{false, true} {
		t.Run(map[bool]string{false: "current_epoch", true: "next_epoch"}[nextEpoch], func(t *testing.T) {
			f := fixture(t)
			path, _, _ := tierSession(t, f)
			sessionID := path[strings.LastIndex(path, "/")+1:]
			tierHistoryEventFence(t, f)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if nextEpoch {
					var definition string
					if err := tx.QueryRow(t.Context(), `SELECT pg_get_functiondef('tier_history_event_fence()'::regprocedure)`).Scan(&definition); err != nil {
						return err
					}
					// Simulate an xid8 one epoch ahead of the row's 32-bit xmin. Leave
					// the trigger's comparison untouched so the old text equality fails.
					definition = strings.ReplaceAll(definition, "pg_current_xact_id()", "((pg_current_xact_id()::text::bigint+4294967296)::text::xid8)")
					if _, err := tx.Exec(t.Context(), definition); err != nil {
						return err
					}
				}
				if _, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "harness.test_fence", After: map[string]any{"session_id": sessionID}}); err != nil {
					return err
				}
				// The event must carry the top-level xid. Start the recovery
				// savepoint afterwards; an event inside it would carry a subxid.
				if _, err := tx.Exec(t.Context(), `SAVEPOINT fence_negative_control`); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO harness_tier_history(tenant_id,session_id,action,from_tier,to_tier,actor_id) VALUES($1,$2,'switch_requested','default','fast',$3)`, f.person.TenantID, sessionID, f.person.ID)
				var pgerr *pgconn.PgError
				if !errors.As(err, &pgerr) || pgerr.Code != "P0001" || pgerr.Message != "tier history must precede the tenant event counter" {
					t.Fatalf("event-first write escaped fence: %v", err)
				}
				_, err = tx.Exec(t.Context(), `ROLLBACK TO SAVEPOINT fence_negative_control`)
				return err
			})
		})
	}
}

func TestTierUndoAlsoRecordsApprovalOfFreshAgentRequest(t *testing.T) {
	f := fixture(t)
	tierHistoryEventFence(t, f)
	path, lease, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	w := f.call(f.person, "POST", path+"/tier", tierChangeBody(t, f, path, "fast", identity), "")
	expect(t, w, 201)
	control := decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	requestID := uid()
	expect(t, f.call(f.agent, "POST", path+"/tier/ask", map[string]any{"request_id": requestID, "tier": "default", "reason": "Return to the usual tier"}, ""), 201)
	undo := tierChangeBody(t, f, path, "default", identity)
	undo["undo_of_control_id"] = control
	expect(t, f.call(f.person, "POST", path+"/tier", undo, ""), 201)
	expect(t, f.call(f.person, "POST", path+"/tier", undo, ""), 201)
	got := tierHistoryActions(t, f, path)
	want := []string{"undo_requested", "approved", "requested", "changed", "switch_requested"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("Undo discarded approval or duplicated replay", got)
	}
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	approved := decode(t, w)["history"].([]any)[1].(map[string]any)
	if approved["actor_id"] != f.person.ID || approved["asked_by_name"] == nil || approved["from_tier"] != "fast" || approved["to_tier"] != "default" {
		t.Fatal("Undo approval lost attribution", approved)
	}
	reverse := decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+reverse+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	if got := tierHistoryActions(t, f, path); got[0] != "undone" {
		t.Fatal("combined approval lost Undo outcome", got)
	}
}
