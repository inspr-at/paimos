// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/hostcapacity"
)

type hostCapacityAPI struct {
	*fakeAPI
	reason string
	err    error
	reads  int
}

func (a *hostCapacityAPI) HostCapacity(context.Context) (hostcapacity.View, error) {
	a.reads++
	return hostcapacity.View{Reason: a.reason}, a.err
}
func TestManagedStartWaitsForHostCapacityBeforeRouteAndClaim(t *testing.T) {
	s, a, _ := testSupervisor(t)
	host := &hostCapacityAPI{fakeAPI: a, reason: "host_load"}
	s.api = host
	if err := s.StartRun(t.Context(), a.run); !errors.Is(err, ErrHostCapacity) {
		t.Fatal("wrong wait error", err)
	}
	if host.reads != 1 || a.claims != 0 || len(a.routeAccounts) != 0 || len(s.Status()) != 0 {
		t.Fatal("busy host routed, claimed or launched")
	}
	host.reason = ""
	if err := s.StartRun(t.Context(), a.run); err != nil {
		t.Fatal(err)
	}
	if host.reads != 2 || a.claims != 1 {
		t.Fatal("falling host load did not release queued start")
	}
	host.reason = "host_load" // A changed host does not kill its running process.
	if err := s.pollOnce(t.Context(), true); err != nil {
		t.Fatal("busy host poll failed", err)
	}
	if host.reads != 3 {
		t.Fatal("poll did not observe the changed host policy")
	}
	if len(s.Status()) != 1 || s.Status()[0].State != "running" {
		t.Fatal("host policy changed running work")
	}
}
func TestManagedStartHostReportFailureNeverClaims(t *testing.T) {
	s, a, _ := testSupervisor(t)
	host := &hostCapacityAPI{fakeAPI: a, err: errors.New("report unavailable")}
	s.api = host
	if err := s.StartRun(t.Context(), a.run); !errors.Is(err, host.err) {
		t.Fatal("report failure lost", err)
	}
	if a.claims != 0 || len(a.routeAccounts) != 0 {
		t.Fatal("report failure authorized a start")
	}
}

// capacityServer emulates the two server generations. A pre-change server
// serves the computer view without host_capacity and refuses the unknown
// capacity route at the pairing boundary.
type capacityServer struct {
	mu                    sync.Mutex
	supported             bool
	selfStatus, capStatus int
	selfReads, reports    int
}

func (c *capacityServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent-pairing/self", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.selfReads++
		w.Header().Set("Content-Type", "application/json")
		if c.selfStatus != 0 {
			w.WriteHeader(c.selfStatus)
			_, _ = w.Write([]byte(`{"error":"pairing not found","code":"not_found"}`))
			return
		}
		if c.supported {
			_, _ = w.Write([]byte(`{"state":"redeemed","host_capacity":{"policy":{"mode":"fixed"},"running":0,"queued":0,"reason":"","load_limit":0,"history":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"redeemed"}`))
	})
	mux.HandleFunc("POST /api/agent-pairing/self/capacity", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.reports++
		w.Header().Set("Content-Type", "application/json")
		if !c.supported {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"outside this computer's runtime authority","code":"forbidden"}`))
			return
		}
		if c.capStatus != 0 {
			w.WriteHeader(c.capStatus)
			_, _ = w.Write([]byte(`{"error":"pairing operation failed","code":"internal_error"}`))
			return
		}
		_, _ = w.Write([]byte(`{"policy":{"mode":"fixed"},"running":0,"queued":0,"reason":"","load_limit":0,"history":[]}`))
	})
	return mux
}

type remoteHostAPI struct {
	*fakeAPI
	remote *Remote
}

func (a *remoteHostAPI) HostCapacity(ctx context.Context) (hostcapacity.View, error) {
	return a.remote.HostCapacity(ctx)
}

// A new daemon against a server without host capacity keeps the unchanged
// protocol: polling, probes and starts work as before.
func TestManagedStartFollowsLegacyPathOnServerWithoutHostCapacity(t *testing.T) {
	old := &capacityServer{}
	server := httptest.NewServer(old.handler())
	defer server.Close()
	s, a, _ := testSupervisor(t)
	s.api = &remoteHostAPI{fakeAPI: a, remote: NewRemote(server.URL, "aeon_test_key")}
	if err := s.pollOnce(t.Context(), false); err != nil {
		t.Fatal("poll blocked on a server without host capacity", err)
	}
	if err := s.StartRun(t.Context(), a.run); err != nil {
		t.Fatal("start blocked on a server without host capacity", err)
	}
	if a.claims != 1 {
		t.Fatalf("legacy start did not claim: %d", a.claims)
	}
	if old.reports != 0 {
		t.Fatalf("daemon called the unnegotiated capacity route %d times", old.reports)
	}
}

func TestHostCapacityNegotiationRenegotiatesAndFailsClosed(t *testing.T) {
	srv := &capacityServer{}
	server := httptest.NewServer(srv.handler())
	defer server.Close()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	r := NewRemote(server.URL, "aeon_test_key")
	r.now = func() time.Time { return now }
	if _, err := r.HostCapacity(t.Context()); !errors.Is(err, ErrHostCapacityUnsupported) {
		t.Fatal("old server was not recognised", err)
	}
	if _, err := r.HostCapacity(t.Context()); !errors.Is(err, ErrHostCapacityUnsupported) || srv.selfReads != 1 {
		t.Fatalf("negotiation was not reused: reads=%d err=%v", srv.selfReads, err)
	}
	// The server is upgraded; the next negotiation picks it up.
	srv.supported = true
	now = now.Add(hostCapacityRenegotiate)
	if _, err := r.HostCapacity(t.Context()); err != nil || srv.selfReads != 2 || srv.reports != 1 {
		t.Fatalf("upgrade not negotiated: reads=%d reports=%d err=%v", srv.selfReads, srv.reports, err)
	}
	// A supported server that fails keeps starts closed and negotiates again.
	srv.capStatus = http.StatusInternalServerError
	_, err := r.HostCapacity(t.Context())
	if err == nil || errors.Is(err, ErrHostCapacityUnsupported) || srv.selfReads != 2 {
		t.Fatal("supported server failure must fail closed", srv.selfReads, err)
	}
	s, a, _ := testSupervisor(t)
	s.api = &remoteHostAPI{fakeAPI: a, remote: r}
	if err := s.StartRun(t.Context(), a.run); err == nil || a.claims != 0 {
		t.Fatal("failed capacity report on a supported server authorized a start", err)
	}
	if srv.selfReads != 3 {
		t.Fatalf("failure did not force renegotiation: reads=%d", srv.selfReads)
	}
	// An unreadable view is unknown, never unsupported.
	r.forgetHostCapacitySupport()
	srv.selfStatus = http.StatusInternalServerError
	if _, err := r.HostCapacity(t.Context()); err == nil || errors.Is(err, ErrHostCapacityUnsupported) {
		t.Fatal("unknown support must fail closed", err)
	}
	// No paired computer: nothing to throttle, legacy path.
	srv.selfStatus = http.StatusNotFound
	if _, err := r.HostCapacity(t.Context()); !errors.Is(err, ErrHostCapacityUnsupported) {
		t.Fatal("unpaired key must take the legacy path", err)
	}
}
