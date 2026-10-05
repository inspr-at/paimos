// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"testing"
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
