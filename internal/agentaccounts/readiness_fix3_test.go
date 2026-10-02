// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestFix3CapacityReplayPreservesConcurrentHealthFailure(t *testing.T) {
	for _, failure := range []string{"auth_failed", "unavailable"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			f := readinessWorld(t, "fix3-health-"+failure, now)
			path := "/api/agent-accounts/" + f.account.ID + "/probe"
			callStatus(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true}), 200, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			mux := http.NewServeMux()
			f.mod.Mount(mux)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				// The fixture injects the same scoped agent context as callStatus.
				r = r.WithContext(tenant.WithPrincipal(r.Context(), f.runner))
				mux.ServeHTTP(w, r)
			}))
			defer server.Close()
			remote := agentd.NewRemote(server.URL, f.token)
			result := make(chan error, 1)
			go func() {
				result <- remote.ReportCapacityCheck(t.Context(), f.account.ID, "daemon-a", "g1", agentd.ProbeStatus{OK: true}, agentd.CapacityCheckReport{BindingRevision: 0, Result: "success"})
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("replay did not reach barrier")
			}
			later := fixedClockModule{Module: New(appPool), at: now.Add(time.Second)}
			var failed Account
			callStatus(t, later, &f.runner, f.token, "POST", path, encodedSimple(probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: false, Failure: failure}), 200, &failed)
			close(release)
			if err := <-result; err != nil {
				t.Fatal("measurement replay rejected", err)
			}
			if failed.LastProbeAt == nil || scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_ok=false AND last_probe_failure=$2 AND last_probe_at=$3`, f.account.ID, failure, failed.LastProbeAt) != 1 {
				t.Fatal("measurement replay overwrote concurrent health failure")
			}
		})
	}
}
