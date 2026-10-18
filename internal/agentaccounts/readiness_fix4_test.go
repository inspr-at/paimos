// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fix4Clock struct {
	sync.Mutex
	at time.Time
}

func (c *fix4Clock) now() time.Time      { c.Lock(); defer c.Unlock(); return c.at }
func (c *fix4Clock) add(d time.Duration) { c.Lock(); c.at = c.at.Add(d); c.Unlock() }

// Only enrollment identity, an empty work queue and the vendor are synthetic.
// Polls, heartbeats and reports go through the production account HTTP handlers.
type fix4DaemonAPI struct {
	agentd.API
	remote  *agentd.Remote
	actor   tenant.Principal
	lost    bool
	stop    context.CancelFunc
	reports []agentd.CapacityCheckReport
	err     error
}

func (a *fix4DaemonAPI) Identity(context.Context) (string, string, error) {
	return a.actor.TenantID, a.actor.ID, nil
}
func (a *fix4DaemonAPI) Queued(context.Context) ([]agentd.Run, error) { return nil, nil }
func (a *fix4DaemonAPI) Probe(ctx context.Context, account, daemon, generation string, ok bool) error {
	return a.remote.Probe(ctx, account, daemon, generation, ok)
}
func (a *fix4DaemonAPI) PollCapacityChecks(ctx context.Context) ([]agentd.CapacityCheckAccount, error) {
	return a.remote.PollCapacityChecks(ctx)
}
func (a *fix4DaemonAPI) CapacityCheckHeartbeat(ctx context.Context, account, daemon, generation string) error {
	return a.remote.CapacityCheckHeartbeat(ctx, account, daemon, generation)
}
func (a *fix4DaemonAPI) ReportCapacityCheck(ctx context.Context, account, daemon, generation string, status agentd.ProbeStatus, report agentd.CapacityCheckReport) error {
	defer a.stop()
	a.reports = append(a.reports, report)
	a.err = errors.New("synthetic delivery outage")
	if !a.lost {
		a.err = a.remote.ReportCapacityCheck(ctx, account, daemon, generation, status, report)
	}
	return a.err
}

type fix4CaptureAdapter struct {
	clock  *fix4Clock
	reset  time.Time
	result string
	used   float64
	calls  int
}

func (*fix4CaptureAdapter) Name() string                       { return agentd.Codex }
func (*fix4CaptureAdapter) Probe(context.Context, string) bool { return true }
func (*fix4CaptureAdapter) Start(context.Context, agentd.StartRequest, func(agentd.AdapterEvent)) (agentd.Process, error) {
	return nil, errors.New("unexpected inference launch")
}
func (a *fix4CaptureAdapter) CaptureCapacityResult(context.Context, string) agentd.CapacityCapture {
	a.calls++
	c := agentd.CapacityCapture{Result: a.result}
	if a.result == "success" {
		c.Readings = []capacity.Reading{{ReadAt: a.clock.now(), ResetsAt: a.reset, WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: a.used, Source: "agentd"}}
	}
	return c
}

func TestFix4AutomaticReplayAfterDayOutageAllowsFreshCheckNow(t *testing.T) {
	clock := &fix4Clock{at: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	observed := clock.now()
	f := readinessWorld(t, "fix4-day-outage", observed)
	f.runner.Scopes = []string{"account.read", "account.probe", "account.manage"}
	f.token = issueKey(t, f.runner, f.runner.Scopes)
	mux := http.NewServeMux()
	New(appPool).Mount(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(tenant.WithPrincipal(r.Context(), f.runner), clockKey{}, clock.now())
		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	api := &fix4DaemonAPI{remote: agentd.NewRemote(server.URL, f.token), actor: f.runner, lost: true}
	adapter := &fix4CaptureAdapter{clock: clock, reset: observed.Add(7 * 24 * time.Hour), result: "success", used: 100}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := agentd.Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon-a", Accounts: []agentd.EnrolledAccount{{ID: f.account.ID, Key: f.account.AccountKey, Harness: agentd.Codex}}, Adapters: []agentd.Adapter{adapter}, Now: clock.now}
	s, err := agentd.NewSupervisor(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			_ = s.Close(context.Background())
		}
	})
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	tick := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		api.stop = cancel
		before := len(api.reports)
		s.RunCapacityCaptures(ctx)
		if ctx.Err() != context.Canceled || len(api.reports) != before+1 {
			t.Fatal("tick did not reach exactly one report; cancellation is only a hang guard")
		}
	}
	pending := func() *agentd.CapacityCheckReport {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, "aeon-agentd-daemon-a.checks.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state struct {
			Accounts map[string]struct {
				Pending *struct{ Report agentd.CapacityCheckReport }
			}
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if v := state.Accounts[f.account.ID].Pending; v != nil {
			return &v.Report
		}
		return nil
	}
	tick()
	want := api.reports[0]
	if api.err == nil || adapter.calls != 1 || want.CheckID != "" || len(want.Facts) != 1 || want.Facts[0].StopKind != "named_reset" || !reflect.DeepEqual(pending(), &want) {
		t.Fatal("automatic exhausted evidence was not durably retained after failed delivery")
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE window_key='weekly:'`) != 0 {
		t.Fatal("failed delivery unexpectedly wrote the fact")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock.add(25 * time.Hour)
	api.lost = false
	s, err = agentd.NewSupervisor(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pending(), &want) {
		t.Fatal("restart lost pending automatic evidence")
	}
	// Replay must work before a new local health probe, without another capture.
	tick()
	if api.err != nil || adapter.calls != 1 || !reflect.DeepEqual(api.reports[1], want) || pending() != nil {
		t.Fatalf("aged automatic replay stalled, changed evidence or recaptured: %v", api.err)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE window_key='weekly:' AND stop_kind='named_reset' AND observed_at=$1 AND reading_at=$1 AND resets_at=$2 AND used_percent=100`, observed, adapter.reset) != 1 {
		t.Fatal("replay erased the stop or renewed its evidence time")
	}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.mod = fixedClockModule{Module: New(appPool), at: clock.now()}
	var check AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("after-outage", 0), 202, &check)
	adapter.result = "protocol"
	tick()
	if api.err != nil || adapter.calls != 2 || api.reports[2].CheckID != check.ID || api.reports[2].Result != "protocol" || pending() != nil || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='completed' AND result='protocol'`, check.ID) != 1 {
		t.Fatal("Check now did not perform and acknowledge its fresh capture", api.err)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE window_key='weekly:' AND stop_kind='named_reset' AND observed_at=$1 AND reading_at=$1`, observed) != 1 {
		t.Fatal("failed fresh measurement cleared or renewed the unresolved stop")
	}
	clock.add(CheckGap)
	f.mod = fixedClockModule{Module: New(appPool), at: clock.now()}
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("fresh-room", 0), 202, &check)
	adapter.result, adapter.used = "success", 17
	tick()
	if api.err != nil || adapter.calls != 3 || api.reports[3].CheckID != check.ID || pending() != nil || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='completed' AND result='success'`, check.ID) != 1 {
		t.Fatal("fresh successful Check now remained blocked", api.err)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE window_key='weekly:' AND stop_kind='none' AND reading_at=$1 AND used_percent=17`, clock.now()) != 1 {
		t.Fatal("newer measured room did not clear the same-window quota stop")
	}
}

func TestFix4AgedAutomaticFactsKeepFreshnessAndOrdering(t *testing.T) {
	for _, used := range []float64{17, 100} {
		t.Run(encodedSimple(used), func(t *testing.T) {
			now := time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC)
			f := readinessWorld(t, "fix4-aged-facts", now)
			var resource string
			if err := adminPool.QueryRow(t.Context(), `SELECT resource_id::text FROM account_readiness_memberships WHERE account_id=$1 AND binding_revision=0`, f.account.ID).Scan(&resource); err != nil {
				t.Fatal(err)
			}
			observed, reset := now.Add(-25*time.Hour), now.Add(6*24*time.Hour)
			fact := ReadinessFactWrite{ResourceID: resource, WindowKey: "weekly:replay", Source: "agentd", ObservedAt: observed, ReadingAt: &observed, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown", StopKind: "none"}
			probe := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", MeasurementOnly: true, Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}}
			path := "/api/agent-accounts/" + f.account.ID + "/probe"
			callStatus(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probe), 200, nil)
			var rows struct {
				Items []AccountReadiness `json:"items"`
			}
			callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &rows)
			if len(rows.Items) != 1 || len(rows.Items[0].MeasuredUsage) != 0 || rows.Items[0].CanTry != (used < 100) || slices.Contains(rows.Items[0].ReasonCodes, "quota_exhausted") != (used == 100) {
				t.Fatalf("old replay became fresh room or erased a stop: %+v", rows.Items)
			}
			// A new person request does not turn the old observation into a manual capture.
			var check AccountCheck
			callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("old-manual", 0), 202, &check)
			probe.Readiness.CheckID = check.ID
			status, raw := call(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probe))
			var rejected struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &rejected); err != nil || status != 400 || rejected.Error != "invalid readiness observation time" {
				t.Fatalf("aged manual fact rejected for wrong reason: %d %s (%v)", status, raw, err)
			}
			if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='pending'`, check.ID) != 1 {
				t.Fatal("invalid manual completion partially committed")
			}
			// Fresh evidence wins even if the durable automatic receipt is replayed again.
			probe.Readiness.CheckID = ""
			freshUsed := 23.0
			fresh := fact
			fresh.ObservedAt, fresh.ReadingAt, fresh.UsedPercent = now, &now, &freshUsed
			probe.Readiness.Facts = []ReadinessFactWrite{fresh}
			callStatus(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probe), 200, nil)
			probe.Readiness.Facts = []ReadinessFactWrite{fact}
			callStatus(t, f.mod, &f.runner, f.token, "POST", path, encodedSimple(probe), 200, nil)
			if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key=$2 AND observed_at=$3 AND reading_at=$3 AND used_percent=23 AND stop_kind='none'`, resource, fact.WindowKey, now) != 1 {
				t.Fatal("older replay replaced newer room evidence")
			}
		})
	}
}
