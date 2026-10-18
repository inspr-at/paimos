// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Risk: middleware clones lose route/auth state; counters double count, leak
// capabilities, or lack the connection ID needed to correlate poisoned reuse.
func TestTransportCountersAndConnectionLog(t *testing.T) {
	var logs bytes.Buffer
	before := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(before)
	s := &Server{}
	handler := requestIDMiddlewareWithMetrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		MarkAuthentication(r.WithContext(context.WithValue(r.Context(), requestIDKey{}, "clone")))
		w.WriteHeader(201)
	}), &s.Metrics)
	ctx := s.ConnContext(context.Background(), nil)
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/api/nodes/private-id?credential=private", nil).WithContext(ctx)
		r.Pattern = "POST /api/nodes/{id}"
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	r := httptest.NewRequest("GET", "/api/capability/private", nil).WithContext(cancelled)
	handler.ServeHTTP(httptest.NewRecorder(), r)
	rows, arrivals := s.Metrics.Snapshot()
	if len(rows) != 2 || arrivals != 1 {
		t.Fatalf("rows=%v cancelled=%d", rows, arrivals)
	}
	for _, row := range rows {
		if row.Authenticated {
			if row.Route != "POST /api/nodes/{id}" || row.StatusClass != "2xx" || row.Total != 2 {
				t.Fatalf("authenticated row: %+v", row)
			}
		} else if row.Route != "unmatched" || row.StatusClass != "5xx" || row.Total != 1 {
			t.Fatalf("cancelled row: %+v", row)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "request" && record["conn_id"] != ConnectionID(ctx) {
			t.Fatalf("missing/wrong connection ID: %s", line)
		}
		if strings.Contains(line, "private") {
			t.Fatalf("counter/log leaked capability: %s", line)
		}
	}
	if ConnectionID(s.ConnContext(context.Background(), nil)) == ConnectionID(ctx) {
		t.Fatal("accepted connections share an ID")
	}
}

func TestTransportCountersConcurrentAndBounded(t *testing.T) {
	var metrics TransportMetrics
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			r := httptest.NewRequest("GET", "/api/private", nil)
			r.Pattern = "GET /api/nodes/{id}"
			for j := 0; j < 50; j++ {
				metrics.record(r, 500, true, true)
			}
		})
	}
	wg.Wait()
	rows, arrivals := metrics.Snapshot()
	if len(rows) != 1 || rows[0].Total != 1000 || arrivals != 1000 {
		t.Fatalf("lost counters: %v, %d", rows, arrivals)
	}
	metrics.requests = make(map[requestCounterKey]uint64)
	for i := 0; i < 8200; i++ {
		r := httptest.NewRequest("GET", "/", nil)
		r.Pattern = string(rune(i))
		metrics.record(r, 400, false, false)
	}
	if len(metrics.requests) > 8193 {
		t.Fatalf("unbounded labels: %d", len(metrics.requests))
	}
}
