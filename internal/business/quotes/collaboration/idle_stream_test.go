// SPDX-License-Identifier: AGPL-3.0-only
package collaboration

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Expire the real connection's deadline after each flush. This deterministically
// simulates the idle period passing without sleeping or shortening production
// timeouts. The sender must clear it before the next idle heartbeat.
type idleDeadlineWriter struct{ http.ResponseWriter }

func (w idleDeadlineWriter) SetWriteDeadline(t time.Time) error {
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(t)
}
func (w idleDeadlineWriter) FlushError() error {
	rc := http.NewResponseController(w.ResponseWriter)
	if err := rc.Flush(); err != nil {
		return err
	}
	return rc.SetWriteDeadline(time.Now().Add(-time.Second))
}

func TestAEON587IdleHeartbeatThenEventOnRealConnection(t *testing.T) {
	f := setup(t)
	ticks := make(chan time.Time)
	f.module.streamTicks = func() (<-chan time.Time, func()) { return ticks, func() {} }
	now := time.Now()
	f.module.now = func() time.Time { return now }
	actor := tenant.Principal{ID: f.admin, TenantID: f.tenant, Kind: tenant.Person, Roles: []string{"admin"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(idleDeadlineWriter{w}, r.WithContext(tenant.WithPrincipal(r.Context(), actor)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+fmt.Sprintf("/api/quotes/%s/collaboration/stream", f.quote), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	readFrame := func() string {
		t.Helper()
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("stream closed: %v; frame %s", err, frame.String())
			}
			frame.WriteString(line)
			if line == "\n" {
				return frame.String()
			}
		}
	}
	if frame := readFrame(); !strings.Contains(frame, "event: presence") {
		t.Fatal(frame)
	}
	select {
	case ticks <- now.Add(16 * time.Second):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if frame := readFrame(); frame != ": keepalive\n\n" {
		t.Fatalf("heartbeat %q", frame)
	}
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := events.Append(ctx, tx, actor, events.Change{NodeID: &f.quote, Type: "quote.draft_updated", After: map[string]any{"draft_revision": 2, "quote_revision": 2}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case ticks <- now.Add(17 * time.Second):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if frame := readFrame(); !strings.Contains(frame, "event: quote_change") || !strings.Contains(frame, `"draft_revision":2`) {
		t.Fatalf("notice %q", frame)
	}
}
