// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestFix2SuspendedTakeoverPreservesGenerationAndActualSettlement(t *testing.T) {
	f := newWorld(t, generous())
	id := f.hold(1, 100)
	claim := f.claim(id)
	if err := f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "suspend"); err != nil {
		t.Fatal(err)
	}
	before := f.success("journal", "cursor", "GET", nil, nil)
	// A fresh store must observe the durable pause, and retries must not consume
	// generations or change the journal cursor.
	replica, err := NewStore(f.database.App)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []*Store{f.store, replica} {
		_, err := store.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, f.claims.Generation)
		errorIs(t, err, 409, "suspended")
		state, err := store.Current(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID)
		if err != nil || !state.Suspended || state.Tombstone || state.Generation != f.claims.Generation || state.Epoch != f.claims.AuthEpoch {
			t.Fatalf("refused takeover changed suspended authority: %+v %v", state, err)
		}
	}
	_, err = replica.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, f.claims.Generation-1)
	errorIs(t, err, 409, "fenced_generation")
	f.store = replica
	after := f.success("journal", "cursor", "GET", nil, nil)
	if !bytes.Equal(marshal(before), marshal(after)) {
		t.Fatal("refused takeover allocated a journal sequence")
	}
	raw := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 27})
	settled := f.httpLedger(f.claims, "settle", raw)
	if settled.Code != 200 {
		t.Fatalf("suspend refused claim-owner settlement: %d", settled.Code)
	}
	doc, err := decode(settled.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	body := object(doc["body"])
	if body["closed_reason"] != "settled" || number(body["charged_micro"]) != 27 {
		t.Fatal("refused takeover fenced the claim and charged the maximum")
	}
	if err := replica.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "resume"); err != nil {
		t.Fatal(err)
	}
	state, err := replica.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, f.claims.Generation)
	if err != nil || state.Suspended || state.Generation != f.claims.Generation+1 {
		t.Fatalf("resume did not allow exactly one generation advance: %+v %v", state, err)
	}
	if retry := f.httpLedger(f.claims, "settle", raw); retry.Code != 200 || !bytes.Equal(settled.Body.Bytes(), retry.Body.Bytes()) {
		t.Fatal("resume and takeover changed the recorded actual settlement")
	}
}

func TestFix2OpResultKeysStayInSession(t *testing.T) {
	for _, action := range []string{"records", "op.result"} {
		t.Run(action, func(t *testing.T) {
			f := newWorld(t, generous())
			token, err := f.keys.MintDelegated(t.Context(), f.claims)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			(&Module{Store: f.store, Keys: f.keys}).Mount(mux)
			post := func(raw []byte) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest("POST", "/api/aithema/journal/sessions/"+f.claims.SessionID+"/"+action+"?format=stored", bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				return rec
			}
			doc := f.record("op-result")
			data := object(doc["data"])
			data["op_key"] = newID() + ":submit:3"
			// The foreign key passes the wire schema; session binding belongs to
			// the store and must reject it before any seq or client id is committed.
			if _, err := f.store.validator.Validate(marshal(doc), "aithema.journal.record"); err != nil {
				t.Fatal(err)
			}
			foreign := post(marshal(doc))
			if foreign.Code != 400 {
				t.Fatalf("foreign op_key accepted by %s: %d", action, foreign.Code)
			}
			errorDoc, err := decode(foreign.Body.Bytes())
			if err != nil || errorDoc["code"] != "invalid_request" {
				t.Fatalf("foreign op_key did not return invalid_request: %v", err)
			}
			cursor := f.success("journal", "cursor", "GET", nil, nil)
			if number(cursor["seq"]) != 0 || number(cursor["working_rev"]) != 0 || cursor["snapshot"] != nil {
				t.Fatal("rejected op.result changed the cursor")
			}
			// Correcting only the key must leave the rejected client id reusable.
			data["op_key"] = f.claims.SessionID + ":submit:3"
			raw := marshal(doc)
			accepted := post(raw)
			if accepted.Code != 200 {
				t.Fatalf("corrected op_key could not reuse client id: %d", accepted.Code)
			}
			var stored Record
			if err := json.Unmarshal(accepted.Body.Bytes(), &stored); err != nil {
				t.Fatal(err)
			}
			projection, err := decode(stored.Document)
			if err != nil || number(projection["seq"]) != 1 || stored.Bytes != string(raw) || !bytes.Equal(marshal(object(projection["data"])["host_ids"]), marshal(data["host_ids"])) {
				t.Fatal("op.result lost the host ids, original bytes or first sequence")
			}
			if retry := post(raw); retry.Code != 200 || !bytes.Equal(accepted.Body.Bytes(), retry.Body.Bytes()) {
				t.Fatal("op.result exact retry changed its stored result")
			}
			if changed := post(append(bytes.Clone(raw), ' ')); changed.Code != 409 {
				t.Fatal("op.result changed-byte retry did not conflict")
			}
			result, err := f.request("journal", "records", "GET", nil, url.Values{"ids": {"1"}, "format": {"stored"}})
			if err != nil {
				t.Fatal(err)
			}
			var records []Record
			if err := json.Unmarshal(result.Body, &records); err != nil || len(records) != 1 || !bytes.Equal(records[0].Document, stored.Document) || records[0].Bytes != stored.Bytes {
				t.Fatal("op.result hydration changed host ids or original bytes")
			}
		})
	}
}
