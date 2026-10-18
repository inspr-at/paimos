// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/usageprobe"
)

type usageProbeAPI interface {
	ReportUsageProbe(context.Context, string, int64, []capacity.Reading, *capacity.Budget) error
}

func (r *Remote) ReportUsageProbe(ctx context.Context, id string, revision int64, readings []capacity.Reading, budget *capacity.Budget) error {
	if readings == nil {
		readings = []capacity.Reading{}
	}
	return r.capacityCheckJSON(ctx, http.MethodPost, "/api/agent-accounts/"+url.PathEscape(id)+"/readings", struct {
		Readings []capacity.Reading `json:"readings"`
		Budget   *capacity.Budget   `json:"budget,omitempty"`
		Probe    bool               `json:"usage_probe"`
		Revision int64              `json:"binding_revision"`
	}{readings, budget, true, revision}, nil)
}

func usageProbeTarget(adapter Adapter, local EnrolledAccount) (usageprobe.Target, bool) {
	t := usageprobe.Target{Harness: local.Harness}
	var homes map[string]string
	switch a := adapter.(type) {
	case *GrokAdapter:
		homes = a.Homes
	case *ClaudeAdapter:
		homes = a.Homes
	case *CodexAdapter:
		homes = a.Homes
	case *PiAdapter:
		homes = a.Homes
		t.Provider = a.Providers[local.Key]
	default:
		return t, false
	}
	home, err := localHome(homes, local.Key)
	if err != nil {
		return t, false
	}
	t.Home = home
	return t, local.Harness != "pi" || t.Provider == "openrouter"
}

// Called under capacityCheckMu inside the existing loop. No CLI is launched;
// active runs keep their stream capture while probes fill the gaps between runs.
func (s *Supervisor) captureUsageProbes(ctx context.Context, now time.Time, accounts map[string]CapacityCheckAccount, locals []EnrolledAccount) {
	api, ok := s.api.(usageProbeAPI)
	if !ok {
		return
	}
	for _, local := range locals {
		if ctx.Err() != nil {
			return
		}
		a, ok := accounts[local.ID]
		if !ok || !a.UsageProbeEnabled || a.LinkRevision < 0 || !a.OngoingUseApproved || a.State != "available" || a.DaemonID != s.daemonID || a.AccountKey != local.Key || a.Harness != local.Harness || local.DependencyBlocked || !s.dispatchAllowed(local.ID) {
			continue
		}
		s.mu.Lock()
		state := s.capacityChecks[local.ID]
		adapter := s.adapters[local.Harness]
		// Revision changes cannot erase a 429 cooldown. A first opt-in has no deadline.
		due := !now.Before(state.UsageNextAttempt)
		s.mu.Unlock()
		if !due {
			continue
		}
		target, ok := usageProbeTarget(adapter, local)
		if !ok {
			continue
		}
		// Persist the five-minute minimum before reading any login or doing IO.
		s.mu.Lock()
		state = s.capacityChecks[local.ID]
		state.UsageNextAttempt = now.Add(usageprobe.Cooldown)
		s.capacityChecks[local.ID] = state
		err := s.saveCapacityChecksLocked()
		s.mu.Unlock()
		if err != nil {
			logCapacityError("usage probe schedule persistence failed", local.ID, err)
			continue
		}
		op, cancel := context.WithTimeout(ctx, 10*time.Second)
		result := s.usageProbes.Capture(op, target, now)
		if result.Cause == "rate_limited" {
			s.mu.Lock()
			saved := s.capacityChecks[local.ID]
			saved.UsageNextAttempt = s.capacityNow().Add(usageprobe.Cooldown)
			s.capacityChecks[local.ID] = saved
			err := s.saveCapacityChecksLocked()
			s.mu.Unlock()
			if err != nil {
				logCapacityError("usage cooldown persistence failed", local.ID, err)
			}
		}
		if result.Cause == "" && (len(result.Readings) > 0 || result.Budget != nil) {
			if err := api.ReportUsageProbe(op, local.ID, a.LinkRevision, result.Readings, result.Budget); err != nil {
				logCapacityError("usage probe report failed", local.ID, err)
			} else {
				s.rememberCapacityCapture(local.ID, result.Readings)
			}
		}
		cancel()
	}
}
