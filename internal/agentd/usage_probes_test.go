// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/usageprobe"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type usageFixtureAPI struct {
	*checkFixtureAPI
	reports int
}

func (a *usageFixtureAPI) ReportUsageProbe(_ context.Context, id string, revision int64, rs []capacity.Reading, b *capacity.Budget) error {
	if id != a.account.ID || revision != a.account.LinkRevision || len(rs) != 1 || rs[0].Source != "agentd" || b != nil {
		return ErrScope
	}
	a.reports++
	return nil
}

type usageFixtureProbe struct {
	calls  int
	result usageprobe.Result
}

func (p *usageFixtureProbe) Capture(context.Context, usageprobe.Target, time.Time) usageprobe.Result {
	p.calls++
	return p.result
}

// Risk: polling, manual checks or daemon restarts must not bypass 429 backoff,
// and opting in should fill quota without a run or an app-server launch.
func TestUsageProbeOptInAndCooldownSurviveRestart(t *testing.T) {
	s, checks, _, clock := checkFixture(t)
	api := &usageFixtureAPI{checkFixtureAPI: checks}
	s.api = api
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	s.adapters[Codex] = &CodexAdapter{Homes: map[string]string{"local": home}}
	p := &usageFixtureProbe{result: usageprobe.Result{Cause: "rate_limited"}}
	s.usageProbes = p
	locals := []EnrolledAccount{{ID: "account", Key: "local", Harness: Codex}}
	run := func() {
		s.captureUsageProbes(t.Context(), clock.Now(), map[string]CapacityCheckAccount{"account": api.account}, locals)
	}
	run()
	if p.calls != 0 {
		t.Fatal("default-off read login")
	}
	api.account.UsageProbeEnabled = true
	run()
	if p.calls != 1 || api.reports != 0 {
		t.Fatal("429 claimed a successful report")
	}
	clock.Add(usageprobe.Cooldown - time.Second)
	s.capacityChecks = map[string]capacityCheckState{}
	if err := s.loadCapacityChecks(); err != nil {
		t.Fatal(err)
	}
	run()
	if p.calls != 1 {
		t.Fatal("restart erased persisted cooldown")
	}
	clock.Add(time.Second)
	now := clock.Now()
	p.result = usageprobe.Result{Readings: []capacity.Reading{{Source: "agentd", ReadAt: now, WindowKind: "5h", WindowMinutes: 300, UsedPercent: 17, ResetsAt: now.Add(time.Hour)}}}
	run()
	if p.calls != 2 || api.reports != 1 || !s.capacityLast["account"].Equal(now) {
		t.Fatal("idle capture failed at cooldown boundary")
	}
	api.account.UsageProbeEnabled = false
	clock.Add(usageprobe.Cooldown)
	run()
	if p.calls != 2 {
		t.Fatal("opt-out continued probing")
	}
}
