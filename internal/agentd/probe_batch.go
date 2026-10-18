// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/client"
)

type AccountProbeObservation struct {
	AccountID  string
	Status     ProbeStatus
	ObservedAt time.Time
}

type probeBatchReporter interface {
	ProbeBatch(context.Context, string, string, []AccountProbeObservation) []error
}

func (s *Supervisor) reportAccountProbes(ctx context.Context, probes []AccountProbeObservation) []error {
	if batch, ok := s.api.(probeBatchReporter); ok {
		out := batch.ProbeBatch(ctx, s.daemonID, s.generation, probes)
		if len(out) == len(probes) {
			return out
		}
		out = make([]error, len(probes))
		for i := range out {
			out[i] = errors.New("incomplete account probe results")
		}
		return out
	}
	results := make([]error, len(probes))
	for i, probe := range probes {
		if reporter, ok := s.api.(ProbeStatusReporter); ok {
			results[i] = reporter.ProbeStatus(ctx, probe.AccountID, s.daemonID, s.generation, probe.Status)
		} else {
			results[i] = s.api.Probe(ctx, probe.AccountID, s.daemonID, s.generation, probe.Status.OK)
		}
	}
	return results
}

// ProbeBatch batches transport only. Every result remains independently fenced
// and acknowledged; a partial response cannot establish readiness for a sibling.
func (r *Remote) ProbeBatch(ctx context.Context, daemon, generation string, probes []AccountProbeObservation) []error {
	results := make([]error, len(probes))
	r.mu.RLock()
	legacy := r.clock().Before(r.probeBatchRetryAt)
	r.mu.RUnlock()
	for offset := 0; offset < len(probes); offset += 32 {
		part := probes[offset:min(offset+32, len(probes))]
		if legacy {
			for i, p := range part {
				results[offset+i] = r.ProbeStatus(ctx, p.AccountID, daemon, generation, p.Status)
			}
			continue
		}
		items := make([]map[string]any, 0, len(part))
		for _, p := range part {
			body := map[string]any{"daemon_id": daemon, "daemon_generation": generation, "available": p.Status.OK}
			if !p.Status.OK {
				failure := ProbeUnavailable
				if p.Status.Failure == ProbeAuthFailed {
					failure = ProbeAuthFailed
				}
				body["failure"] = failure
			}
			if p.Status.OpenRouterCredits != nil {
				body["openrouter_credits"] = p.Status.OpenRouterCredits
			}
			if host, err := os.Hostname(); err == nil && host != "" && len(host) <= 128 {
				body["host_label"] = host
			}
			items = append(items, map[string]any{"account_id": p.AccountID, "observed_at": p.ObservedAt, "probe": body})
		}
		var out struct {
			Items []struct {
				AccountID string `json:"account_id"`
				Status    int    `json:"status"`
			} `json:"items"`
		}
		err := r.Client.Do(ctx, "POST", "/api/agent-accounts/probes", map[string]any{"items": items}, &out)
		if optionalDaemonRouteUnavailable(err) {
			// Older servers deny undeclared routes with 403. Each legacy
			// single-probe write still checks its own current authorization.
			// Do not add one failed batch request to every idle health tick.
			legacy = true
			r.mu.Lock()
			r.probeBatchRetryAt = r.clock().Add(5 * time.Minute)
			r.mu.Unlock()
			for i, p := range part {
				results[offset+i] = r.ProbeStatus(ctx, p.AccountID, daemon, generation, p.Status)
			}
			continue
		}
		for i, p := range part {
			results[offset+i] = err
			if err != nil {
				continue
			}
			if len(out.Items) != len(part) || out.Items[i].AccountID != p.AccountID {
				results[offset+i] = errors.New("incomplete account probe results")
				continue
			}
			if out.Items[i].Status != http.StatusOK {
				results[offset+i] = &client.StatusError{Status: out.Items[i].Status}
			}
		}
	}
	return results
}

// Only whole-request refusals negotiate the optional transport. Per-account
// batch results never reach this check and retain their authorization failures.
func optionalDaemonRouteUnavailable(err error) bool {
	var status *client.StatusError
	return errors.As(err, &status) && (status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden ||
		status.Status == http.StatusNotFound || status.Status == http.StatusMethodNotAllowed)
}
