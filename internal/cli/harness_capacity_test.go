// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

func TestHeartbeatCapacityProjection(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "quota.jsonl")
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	raw := fmt.Sprintf(`{"timestamp":%q,"type":"rate_limit_event","session_id":"not-forwarded","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.23,"resetsAt":%d},"unrelated":"not-forwarded"}`, at.Format(time.RFC3339), at.Add(time.Hour).Unix())
	if err := os.WriteFile(path, []byte(raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Method != "POST" || r.URL.Path != "/api/agent-accounts/11111111-1111-4111-8111-111111111111/readings" {
			t.Error("incorrect endpoint")
		}
		var body struct {
			Readings []capacity.Reading `json:"readings"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Readings) != 1 || body.Readings[0].UsedPercent != 23 || !body.Readings[0].ReadAt.Equal(at) {
			t.Errorf("bad projection %+v", body)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	opts := heartbeatCapacity{Account: "11111111-1111-4111-8111-111111111111", Source: "claude", File: path}
	if err := rt.reportHeartbeatCapacity(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal(count)
	}
	for _, bad := range []heartbeatCapacity{{Account: opts.Account}, {Source: "claude", File: path}, {Account: opts.Account, Source: "grok", File: path}} {
		if bad.validate() == nil {
			t.Fatal("accepted incomplete flags")
		}
	}
}

func TestRunHeartbeatCapacityLifecycle(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "quota.jsonl")
	at := time.Now().UTC().Add(-time.Second)
	raw := fmt.Sprintf(`{"timestamp":%q,"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.23,"resetsAt":%d}}`, at.Format(time.RFC3339Nano), at.Add(time.Hour).Unix())
	if err := os.WriteFile(path, []byte(raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	base := heartbeatFixture(t, &calls, "", "")
	defer base.Close()
	var phases []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent-accounts/11111111-1111-4111-8111-111111111111/readings" {
			var body struct {
				Readings []capacity.Reading `json:"readings"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for _, v := range body.Readings {
				phases = append(phases, v.Phase)
			}
			w.WriteHeader(204)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(dir)
	o.Capacity = heartbeatCapacity{Account: "11111111-1111-4111-8111-111111111111", Source: "claude", File: path}
	err = rt.runHeartbeat(t.Context(), o, heartbeatDeps{alive: func(int) bool { return true }, wait: func(context.Context, int, time.Duration) error { return errors.New("owner exited") }})
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	if len(phases) != 2 || phases[0] != "start" || phases[1] != "end" {
		t.Fatal(phases, stderr.String())
	}
}
