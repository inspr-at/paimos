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

func TestFix3MeasurementHeartbeatAndGenerationFence(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix3-generation", now)
	path := "/api/agent-accounts/" + f.account.ID + "/probe"
	var health Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Failure: "auth_failed"}), 200, &health)
	callStatus(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g2", Available: true, MeasurementOnly: true}), 200, nil)
	if health.LastProbeAt == nil || scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_ok=false AND last_probe_failure='auth_failed' AND last_probe_at=$2 AND last_daemon_generation='g2'`, f.account.ID, health.LastProbeAt) != 1 {
		t.Fatal("generation heartbeat changed health")
	}
	for _, generation := range []string{"g1", "g2"} {
		want := 409
		if generation == "g2" {
			want = 200
		}
		callStatus(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probeWrite{DaemonID: "daemon-a", DaemonGeneration: generation, Available: true, MeasurementOnly: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success"}}), want, nil)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_ok=false AND last_probe_failure='auth_failed' AND last_daemon_generation='g2'`, f.account.ID) != 1 {
		t.Fatal("measurement report erased health or rebound an old generation")
	}
}

// A restart report carrying an old check must not bypass measurement-only
// health isolation through the ordinary restart heartbeat's partial commit.
func TestR122MeasurementRestartWithPendingCheckPreservesHealth(t *testing.T) {
	for _, failure := range []string{"auth_failed", "unavailable"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
			f := readinessWorld(t, "r122-measurement-restart-"+failure, now)
			path := "/api/agent-accounts/" + f.account.ID
			var health Account
			callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Failure: failure}), 200, &health)
			var check AccountCheck
			callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("before-restart", 0), 202, &check)
			if check.DaemonGeneration == nil || *check.DaemonGeneration != "g1" || health.LastProbeAt == nil {
				t.Fatal("fixture requires a pending g1 check and known failed health")
			}
			later := fixedClockModule{Module: New(appPool), at: now.Add(time.Minute)}
			probe := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g2", Available: true, MeasurementOnly: true, Readiness: &ReadinessReport{CheckID: check.ID, BindingRevision: ptrRevision(0), Result: "success"}}
			callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 409, nil)
			if scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_ok=false AND last_probe_failure=$2 AND last_probe_at=$3 AND last_daemon_generation='g1'`, f.account.ID, failure, *health.LastProbeAt) != 1 {
				t.Fatal("rejected measurement-only restart overwrote health or advanced generation")
			}
			if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='pending' AND result IS NULL`, check.ID) != 1 {
				t.Fatal("rejected measurement-only restart changed the pending check")
			}
			probe.Readiness = nil
			callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
			if scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_ok=false AND last_probe_failure=$2 AND last_probe_at=$3 AND last_daemon_generation='g2'`, f.account.ID, failure, *health.LastProbeAt) != 1 {
				t.Fatal("measurement-only generation heartbeat changed known failed health")
			}
		})
	}
}
