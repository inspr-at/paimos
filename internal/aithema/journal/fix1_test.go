// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

// Admission is not dispatch: the worker must acknowledge this exact hold in
// the journal before it can claim it. Read the authoritative admission fields.
func (f *world) journalHold(id string) map[string]any {
	f.t.Helper()
	var attempt, lane, kind, currency string
	var maximum int64
	err := db.InTenant(f.t.Context(), f.database.App, f.claims.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT attempt_id,lane,lane_kind,max_micro,currency FROM aithema_budget_holds WHERE tenant_id=$1 AND sid=$2 AND hold_id=$3`, f.claims.TenantID, f.claims.SessionID, id).Scan(&attempt, &lane, &kind, &maximum, &currency)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	doc := f.record("budget-hold")
	data := map[string]any{"hold_id": id, "attempt_id": attempt, "lane": lane, "max_micro": maximum, "currency": currency}
	if kind != "remote" {
		data["lane_kind"] = kind
	}
	doc["data"] = data
	return doc
}

func (f *world) acknowledgeHold(id string) {
	f.t.Helper()
	f.success("journal", "records", "POST", marshal(f.journalHold(id)), nil)
}

func (f *world) httpLedger(c tokens.Claims, action string, raw []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	token, err := f.keys.MintDelegated(f.t.Context(), c)
	if err != nil {
		f.t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Module{Store: f.store, Keys: f.keys}).Mount(mux)
	req := httptest.NewRequest("POST", "/api/aithema/ledger/sessions/"+c.SessionID+"/"+action, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestFix1CommittedClaimOwnerCanSettleAfterFencing(t *testing.T) {
	for _, transition := range []string{"takeover", "epoch", "purge"} {
		t.Run(transition, func(t *testing.T) {
			f := newWorld(t, generous())
			id := f.hold(1, 100)
			f.acknowledgeHold(id)
			claim := f.claim(id)
			unclaimed := f.hold(2, 100)
			owner := f.claims
			switch transition {
			case "takeover":
				if _, err := f.store.Takeover(t.Context(), owner.TenantID, owner.ProjectID, owner.SessionID, owner.Generation); err != nil {
					t.Fatal(err)
				}
				f.claims.Generation++
			case "epoch":
				authz, _ := decode(f.config.Authorization)
				authz["epoch"] = owner.AuthEpoch + 1
				if err := db.InTenant(t.Context(), f.database.App, owner.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE aithema_sessions SET auth_epoch=$3,authorization_bytes=$4 WHERE tenant_id=$1 AND sid=$2`, owner.TenantID, owner.SessionID, owner.AuthEpoch+1, marshal(authz))
					return err
				}); err != nil {
					t.Fatal(err)
				}
				f.claims.AuthEpoch++
			case "purge":
				if err := f.store.Revoke(t.Context(), owner.TenantID, owner.ProjectID, owner.SessionID, "purge"); err != nil {
					t.Fatal(err)
				}
			}
			raw := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 1})
			got := f.httpLedger(owner, "settle", raw)
			if got.Code != 200 {
				t.Fatalf("committed claim owner fenced from settlement: %d", got.Code)
			}
			doc, _ := decode(got.Body.Bytes())
			body := object(doc["body"])
			if body["closed_reason"] != "unknown" || number(body["charged_micro"]) != 100 {
				t.Fatal("stale claim was not charged at maximum")
			}
			if retry := f.httpLedger(owner, "settle", raw); retry.Code != 200 || !bytes.Equal(got.Body.Bytes(), retry.Body.Bytes()) {
				t.Fatal("owner settlement lost exact retry after fencing")
			}
			if retry := f.httpLedger(owner, "settle", append(bytes.Clone(raw), ' ')); retry.Code != 409 {
				t.Fatal("changed settlement bytes did not conflict")
			}
			for _, field := range []string{"generation", "epoch", "capability", "project", "plugin", "actor"} {
				other := owner
				switch field {
				case "generation":
					other.Generation++
				case "epoch":
					other.AuthEpoch++
				case "capability":
					other.Capabilities = []string{"intake.read"}
				case "project":
					other.ProjectID = "other-project"
				case "plugin":
					other.Subject = "other-plugin"
				case "actor":
					other.Actor = &tokens.Actor{Subject: "other-person"}
				}
				if rec := f.httpLedger(other, "settle", raw); rec.Code != 403 {
					t.Fatalf("foreign %s settled committed claim: %d", field, rec.Code)
				}
			}
			for _, action := range []string{"claim", "recover"} {
				payload := f.claimBody(unclaimed)
				if action == "recover" {
					payload = f.recoverBody(unclaimed)
				}
				if rec := f.httpLedger(owner, action, payload); rec.Code != 409 {
					t.Fatalf("stale owner used %s: %d", action, rec.Code)
				}
			}
			if transition != "purge" {
				if got := f.recover(unclaimed); got["closed_reason"] != "void" {
					t.Fatal("current authority could not recover an old hold")
				}
			}
		})
	}
}

func TestFix1SettleAfterRecoverReturnsRecordedResult(t *testing.T) {
	f := newWorld(t, generous())
	id := f.hold(1, 100)
	f.acknowledgeHold(id)
	claim := f.claim(id)
	recovered := f.recover(id)
	raw := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 1})
	got := object(f.success("ledger", "settle", "POST", raw, nil)["body"])
	if !bytes.Equal(marshal(recovered), marshal(got)) {
		t.Fatal("late settlement changed recovered result")
	}
	// Recovery owns the close, so no settlement bytes are invented by a late
	// reply. Match the reference's closed-hold result without discounting cost.
	got = object(f.success("ledger", "settle", "POST", budget("settle_request", map[string]any{"claim_id": claim, "outcome": "unknown"}), nil)["body"])
	if !bytes.Equal(marshal(recovered), marshal(got)) {
		t.Fatal("repeated late settlement changed recorded result")
	}
	_, err := f.request("ledger", "claim", "POST", f.claimBody(id), nil)
	errorIs(t, err, 409, "hold_closed")
}

func TestFix1ClaimRequiresMatchingAcknowledgedHold(t *testing.T) {
	for _, mismatch := range []string{"absent", "session", "generation", "hold_id", "attempt_id", "lane", "max_micro", "currency", "lane_kind"} {
		t.Run(mismatch, func(t *testing.T) {
			f := newWorld(t, generous())
			id := text(object(f.success("ledger", "admit", "POST", f.admitBody(1, 100), nil)["body"])["hold_id"])
			doc := f.journalHold(id)
			data := object(doc["data"])
			switch mismatch {
			case "session":
				other, _ := decode(f.config.Authorization)
				other["sid"] = newID()
				config := f.config
				config.Authorization = marshal(other)
				if err := f.store.CreateSession(t.Context(), config); err != nil {
					t.Fatal(err)
				}
				c := f.claims
				c.SessionID = text(other["sid"])
				doc["sid"] = c.SessionID
				data["attempt_id"] = fmt.Sprintf("%s:2:spec:1", c.SessionID)
				if _, err := f.store.Request(t.Context(), c, "journal", "records", "POST", marshal(doc), nil); err != nil {
					t.Fatal(err)
				}
			case "generation":
				f.acknowledgeHold(id)
				if _, err := f.store.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, 2); err != nil {
					t.Fatal(err)
				}
				f.claims.Generation = 3
				// Isolate writer matching: move only the hold's admission generation
				// to current while leaving the same data and old journal writer.
				if err := db.InTenant(t.Context(), f.database.App, f.claims.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE aithema_budget_holds SET worker_generation=3 WHERE hold_id=$1`, id)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "hold_id":
				data[mismatch] = newID()
			case "attempt_id":
				data[mismatch] = fmt.Sprintf("%s:2:spec:9", f.claims.SessionID)
			case "lane":
				data[mismatch] = "design"
				data["attempt_id"] = fmt.Sprintf("%s:2:design:1", f.claims.SessionID)
			case "max_micro":
				data[mismatch] = 99
			case "currency":
				data[mismatch] = "USD"
			case "lane_kind":
				data[mismatch] = "operator_local"
			}
			if mismatch != "absent" && mismatch != "session" && mismatch != "generation" {
				f.success("journal", "records", "POST", marshal(doc), nil)
			}
			_, err := f.request("ledger", "claim", "POST", f.claimBody(id), nil)
			errorIs(t, err, 409, "hold_not_journaled")
			if mismatch != "generation" {
				f.acknowledgeHold(id)
				f.claim(id)
			}
		})
	}
}

func TestFix1SuspendPausesWritesAndResumeRestoresAuthority(t *testing.T) {
	f := newWorld(t, generous())
	id, unclaimed := f.hold(1, 100), f.hold(2, 100)
	f.acknowledgeHold(id)
	f.acknowledgeHold(unclaimed)
	claim := f.claim(id)
	if err := f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "suspend"); err != nil {
		t.Fatal(err)
	}
	state := f.success("journal", "authority", "GET", nil, nil)
	if state["tombstone"] != false || state["suspended"] != true || number(state["auth_epoch"]) != f.claims.AuthEpoch || object(state["authorization"])["withdrawn_at"] != nil {
		t.Fatal("suspend changed terminal authority instead of pausing writes")
	}
	for _, action := range []string{"admit", "claim"} {
		raw := f.admitBody(3, 100)
		if action == "claim" {
			raw = f.claimBody(unclaimed)
		}
		_, err := f.request("ledger", action, "POST", raw, nil)
		errorIs(t, err, 409, "suspended")
	}
	_, err := f.request("journal", "records", "POST", marshal(f.record("turn")), nil)
	errorIs(t, err, 409, "suspended")
	f.success("ledger", "holds", "GET", nil, url.Values{"state": {"open"}})
	f.success("journal", "cursor", "GET", nil, nil)
	settled := object(f.success("ledger", "settle", "POST", budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 27}), nil)["body"])
	if settled["closed_reason"] != "settled" || number(settled["charged_micro"]) != 27 {
		t.Fatal("suspend discounted or fenced committed claim")
	}
	if got := f.recover(unclaimed); got["closed_reason"] != "void" {
		t.Fatal("suspend blocked recovery")
	}
	for range 2 {
		if err := f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "resume"); err != nil {
			t.Fatal(err)
		}
	}
	state = f.success("journal", "authority", "GET", nil, nil)
	if state["suspended"] != false || number(state["worker_generation"]) != f.claims.Generation {
		t.Fatal("resume changed generation or retained pause")
	}
	f.success("journal", "records", "POST", marshal(f.record("turn")), nil)
	f.claim(f.hold(4, 100)) // test helper will acknowledge the hold before claiming
	if err := f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "purge"); err != nil {
		t.Fatal(err)
	}
	err = f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "resume")
	errorIs(t, err, 409, "revoked")
}

func TestFix1SnapshotCursorNeverDecreasesAndPendingOpsStayInSession(t *testing.T) {
	f := newWorld(t, generous())
	f.success("journal", "records", "POST", marshal(f.record("turn")), nil)
	f.success("journal", "snapshots", "POST", marshal(f.snapshot(0, 1)), nil)
	replica, err := NewStore(f.database.App)
	if err != nil {
		t.Fatal(err)
	}
	f.store = replica
	_, err = f.request("journal", "snapshots", "POST", marshal(f.snapshot(1, 0)), nil)
	errorIs(t, err, 400, "invalid_request")
	var consumed int64
	if err := f.database.Admin.QueryRow(context.Background(), `SELECT consumed_seq FROM aithema_sessions WHERE sid=$1`, f.claims.SessionID).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("snapshot cursor not persisted: %d %v", consumed, err)
	}
	snap := f.snapshot(1, 1)
	op := object(array(snap["pending_ops"])[0])
	op["op_key"] = newID() + ":submit:1"
	_, err = f.request("journal", "snapshots", "POST", marshal(snap), nil)
	errorIs(t, err, 400, "invalid_request")
	// A rejected revision must leave both the cursor and CAS revision intact.
	f.success("journal", "snapshots", "POST", marshal(f.snapshot(1, 1)), nil)
}

func TestFix1ConcurrentSettlementAndRecoveryPreserveOneClose(t *testing.T) {
	f := newWorld(t, generous())
	id := f.hold(1, 100)
	claim := f.claim(id)
	settle := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 27})
	start := make(chan struct{})
	const workers = 8
	results, errs := make([]Result, workers), make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			action, raw := "settle", settle
			if i%2 == 0 {
				action, raw = "recover", f.recoverBody(id)
			}
			results[i], errs[i] = f.request("ledger", action, "POST", raw, nil)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil || !bytes.Equal(results[0].Body, results[i].Body) {
			t.Fatalf("settle/recover race changed recorded close: %v", err)
		}
	}
	doc, _ := decode(results[0].Body)
	body := object(doc["body"])
	if !(body["closed_reason"] == "settled" && number(body["charged_micro"]) == 27) &&
		!(body["closed_reason"] == "unknown" && number(body["charged_micro"]) == 100) {
		t.Fatal("settlement/recovery charge was not one atomic close")
	}
}

func TestFix1ConcurrentTakeoverAndSettlementRespectCommitOrder(t *testing.T) {
	f := newWorld(t, generous())
	id := f.hold(1, 100)
	claim := f.claim(id)
	raw := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 27})
	start := make(chan struct{})
	var wg sync.WaitGroup
	var settled Result
	var settleErr, takeoverErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		settled, settleErr = f.request("ledger", "settle", "POST", raw, nil)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, takeoverErr = f.store.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, f.claims.Generation)
	}()
	close(start)
	wg.Wait()
	if settleErr != nil || takeoverErr != nil {
		t.Fatalf("committed owner could not finish: settle=%v takeover=%v", settleErr, takeoverErr)
	}
	doc, _ := decode(settled.Body)
	body := object(doc["body"])
	if !(body["closed_reason"] == "settled" && number(body["charged_micro"]) == 27) &&
		!(body["closed_reason"] == "unknown" && number(body["charged_micro"]) == 100) {
		t.Fatal("takeover/settlement charge was not one atomic close")
	}
	// A lost response remains replayable by the original claim owner.
	retry, err := f.request("ledger", "settle", "POST", raw, nil)
	if err != nil || !bytes.Equal(settled.Body, retry.Body) {
		t.Fatal("takeover changed a committed settlement retry")
	}
	f.claims.Generation++
	if got := f.recover(id); !bytes.Equal(marshal(got), marshal(body)) {
		t.Fatal("successor recovery changed recorded charge")
	}
}

func TestFix1HostControlWriteAheadRollbackAndIdempotency(t *testing.T) {
	f := newWorld(t, generous())
	for _, action := range []string{"suspend", "suspend", "resume", "resume"} {
		if err := f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, action); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.request("journal", "records", "GET", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if err := json.Unmarshal(result.Body, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || object(records[0]["data"])["action"] != "suspend" || object(records[1]["data"])["action"] != "resume" {
		t.Fatal("control retry appended duplicate records")
	}
	if _, err := f.database.Admin.Exec(t.Context(), `UPDATE aithema_sessions SET seq=$2 WHERE sid=$1`, f.claims.SessionID, tokens.MaxSafeInteger); err != nil {
		t.Fatal(err)
	}
	err = f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "suspend")
	errorIs(t, err, 409, "sequence_exhausted")
	state, err := f.store.Current(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID)
	if err != nil || state.Suspended || state.Tombstone || state.Epoch != f.claims.AuthEpoch {
		t.Fatal("failed write-ahead projected a control effect")
	}
}
