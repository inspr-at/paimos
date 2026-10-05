// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func testService() *Service {
	return New(Options{Enabled: true, SingleInstance: true, Origin: "https://aeon.test"})
}
func TestVolatileReservationCommitRollbackAndTuple(t *testing.T) {
	s := testService()
	b := Binding{TenantID: UUID(), SessionID: UUID(), Generation: UUID(), ServiceEpoch: s.epoch, OwnerID: UUID(), ProjectID: UUID(), DaemonEpoch: strings.Repeat("a", 64)}
	g, id := UUID(), UUID()
	scope := TakeTransaction{Context: t.Context(), Tx: livePayloadTx{}}
	if e := s.Reserve(id, b, g, "volatile-canary", "request", time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.Take(b, g, id, scope); ok {
		t.Fatal("uncommitted body released")
	}
	s.Publish(b.TenantID, id)
	if body, ok := s.Take(b, g, id); ok || body != "" {
		t.Fatal("take without a transaction released content")
	}
	changes := []func(*Binding){func(x *Binding) { x.TenantID = UUID() }, func(x *Binding) { x.ProjectID = UUID() }, func(x *Binding) { x.OwnerID = UUID() }, func(x *Binding) { x.SessionID = UUID() }, func(x *Binding) { x.Generation = UUID() }, func(x *Binding) { x.DaemonEpoch = "wrong" }, func(x *Binding) { x.HookConfigDigest = "wrong" }, func(x *Binding) { x.ServiceEpoch = UUID() }}
	for i, change := range changes {
		bad := b
		change(&bad)
		if _, ok := s.Take(bad, g, id, scope); ok {
			t.Fatalf("wrong tuple %d released", i)
		}
	}
	if _, ok := s.Take(b, UUID(), id, scope); ok {
		t.Fatal("wrong grant released")
	}
	body, ok := s.Take(b, g, id, scope)
	if !ok || body != "volatile-canary" {
		t.Fatal("committed body unavailable")
	}
	if _, ok = s.Take(b, g, id, scope); ok {
		t.Fatal("payload replayed")
	}
	id = UUID()
	if e := s.Reserve(id, b, g, "rollback-canary", "request", time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	s.Discard(b.TenantID, id)
	s.Publish(b.TenantID, id)
	if _, ok = s.Take(b, g, id, scope); ok {
		t.Fatal("rolled-back body published")
	}
	id = UUID()
	if e := s.Reserve(id, b, g, "expired", "request", time.Now().Add(-time.Second)); e != nil {
		t.Fatal(e)
	}
	s.Publish(b.TenantID, id)
	if _, ok = s.Take(b, g, id, scope); ok {
		t.Fatal("expired body released")
	}
}
func TestMemoryQuotasBoundPayloadAndMetadata(t *testing.T) {
	for _, tiny := range []bool{false, true} {
		s := testService()
		tid := UUID()
		body := strings.Repeat("x", 4096)
		max := 256
		if tiny {
			body = "x"
			max = 1024
		}
		for i := 0; i < max; i++ {
			b := Binding{TenantID: tid, SessionID: UUID()}
			if e := s.Reserve(UUID(), b, UUID(), body, "request", time.Now().Add(time.Minute)); e != nil {
				t.Fatalf("tenant quota premature %d: %v", i, e)
			}
		}
		if e := s.Reserve(UUID(), Binding{TenantID: tid, SessionID: UUID()}, UUID(), body, "request", time.Now().Add(time.Minute)); e == nil {
			t.Fatal("tenant ceiling exceeded")
		}
	}
	for _, tiny := range []bool{false, true} {
		s := testService()
		body := strings.Repeat("x", 4096)
		max := 4096
		if tiny {
			body = "x"
			max = 16384
		}
		for i := 0; i < max; i++ {
			b := Binding{TenantID: UUID(), SessionID: UUID()}
			if e := s.Reserve(UUID(), b, UUID(), body, "request", time.Now().Add(time.Minute)); e != nil {
				t.Fatalf("process quota premature %d: %v", i, e)
			}
		}
		if e := s.Reserve(UUID(), Binding{TenantID: UUID(), SessionID: UUID()}, UUID(), body, "request", time.Now().Add(time.Minute)); e == nil {
			t.Fatal("process ceiling exceeded")
		}
	}
}
func TestFrameLimitsAndEscaping(t *testing.T) {
	for _, body := range []string{"", strings.Repeat("x", 4097), "control\x1b[0m", "null\x00", "\xff", strings.Repeat("\t", 4096)} {
		if _, e := Frame("owner", "Owner", body); e == nil {
			t.Fatal("unsafe/oversized frame accepted")
		}
	}
	frame, e := Frame("owner", "Owner", "quoted \"line\"\n\t bidi\u202eend")
	if e != nil || strings.ContainsRune(frame, '\u202e') || !strings.Contains(frame, "u202e") {
		t.Fatal("frame escaping failed")
	}
}
func TestSwitchDefaultsAndEpochs(t *testing.T) {
	if New(Options{}).Enabled() || New(Options{Enabled: true}).Enabled() {
		t.Fatal("feature enabled without qualification")
	}
	a, b := testService(), testService()
	if a.epoch == b.epoch {
		t.Fatal("epochs reused")
	}
}

// Only the RAM tuple/replay tests stub SQL; the pairing tests exercise real rows.
type livePayloadTx struct{ pgx.Tx }

func (livePayloadTx) QueryRow(context.Context, string, ...any) pgx.Row { return livePayloadRow{} }

type livePayloadRow struct{}

func (livePayloadRow) Scan(dest ...any) error { *dest[0].(*bool) = true; return nil }

func TestNotificationMemoryBudgetReservesOwnerCapacity(t *testing.T) {
	s := testService()
	b := Binding{TenantID: UUID(), SessionID: UUID()}
	for i := 0; i < 5; i++ {
		if e := s.Reserve(UUID(), b, "", "", "notification", time.Now().Add(time.Minute)); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Reserve(UUID(), b, "", "", "notification", time.Now().Add(time.Minute)); e == nil {
		t.Fatal("notification budget unbounded")
	}
	for i := 0; i < 5; i++ {
		if e := s.Reserve(UUID(), b, UUID(), "owner note", "owner", time.Now().Add(time.Minute)); e != nil {
			t.Fatal("notification consumed owner capacity", e)
		}
	}
	if e := s.Reserve(UUID(), b, UUID(), "owner note", "owner", time.Now().Add(time.Minute)); e == nil {
		t.Fatal("owner budget unbounded")
	}
}
