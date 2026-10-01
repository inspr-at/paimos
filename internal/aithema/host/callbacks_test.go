// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallbackReplicaLeasePreventsConcurrentDelivery(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	f.session()
	done := make(chan error, 1)
	go func() { _, err := f.m.DeliverOne(context.Background(), f.p.TenantID); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("delivery did not acquire lease")
	}
	if ok, err := f.m.DeliverOne(t.Context(), f.p.TenantID); err != nil || ok {
		close(release)
		t.Fatal("replica delivered an already leased callback")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not complete")
	}
	if calls.Load() != 1 {
		t.Fatal("replica sent duplicate callback")
	}
}

func TestPurgeAcknowledgementIsBoundedAndSessionOwned(t *testing.T) {
	var invalid atomic.Bool
	invalid.Store(true)
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/purge") {
			w.Write([]byte(`{}`))
			return
		}
		var request struct {
			Session string `json:"sid"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("purge callback body")
		}
		artifacts := []string{request.Session + ":preview:test"}
		if invalid.Load() {
			artifacts = []string{newID() + ":preview:foreign"}
		}
		json.NewEncoder(w).Encode(map[string]any{"sid": request.Session, "purged": true, "host_artifacts": artifacts})
	})
	s := f.session()
	f.deliver()
	w := f.call("POST", "/api/projects/"+f.project+"/aithema/sessions/"+s.Session+"/control", map[string]string{"action": "purge", "idempotency_key": newID()})
	if w.Code != 202 {
		t.Fatal("purge control refused")
	}
	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	f.deliver()
	w = f.call("GET", "/api/aithema/callbacks/"+result["callback_id"], nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"pending"`) || strings.Contains(w.Body.String(), "host_artifacts") {
		t.Fatal("foreign acknowledgement retained")
	}
	invalid.Store(false)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE aithema_callbacks SET next_attempt_at=now() WHERE tenant_id=$1`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	f.deliver()
	w = f.call("GET", "/api/aithema/callbacks/"+result["callback_id"], nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"delivered"`) || !strings.Contains(w.Body.String(), s.Session+":preview:test") {
		t.Fatal("valid purge acknowledgement missing")
	}
}

func TestCallbackAuthenticationFailureDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(401)
		w.Write([]byte(mockCredential))
	})
	s := f.session()
	f.deliver()
	if ok, err := f.m.DeliverOne(t.Context(), f.p.TenantID); err != nil || ok {
		t.Fatal("authentication failure retried")
	}
	w := f.call("GET", "/api/aithema/callbacks/"+s.Callback, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"failed"`) || strings.Contains(w.Body.String(), mockCredential) || calls.Load() != 1 {
		t.Fatal("permanent failure contract")
	}
}
