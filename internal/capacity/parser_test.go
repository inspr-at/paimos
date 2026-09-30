// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexDurationsSparseAuthorityAndRollout(t *testing.T) {
	now := instant("2026-09-28T08:00:00Z")
	reset := now.Add(48 * time.Hour).Unix()
	p := Parser{}
	raw := fmt.Sprintf(`{"rateLimits":{"limitId":"codex","planType":"pro","primary":{"usedPercent":42,"windowDurationMins":10080,"resetsAt":%d}},"ordinaryUsageAllowed":false}`, reset)
	readings := p.Codex([]byte(raw), now)
	if len(readings) != 1 || readings[0].WindowKind != "weekly" || readings[0].Routable(now) {
		t.Fatal(readings)
	}
	readings = p.Codex([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":43}}}}`), now.Add(time.Minute))
	if len(readings) != 1 || readings[0].UsedPercent != 43 || readings[0].Plan != "pro" || *readings[0].OrdinaryUsageAllowed {
		t.Fatal(readings)
	}
	readings = p.Codex([]byte(`{"ordinaryUsageAllowed":true}`), now.Add(2*time.Minute))
	if len(readings) != 1 || !readings[0].Routable(now.Add(2*time.Minute)) {
		t.Fatal(readings)
	}
	raw = fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-09-28T08:00:00Z","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","plan_type":"pro","primary":{"used_percent":42,"window_minutes":10080,"resets_at":%d}}}}`, reset)
	p = Parser{}
	readings = p.Codex([]byte(raw), now.Add(time.Hour))
	if len(readings) != 1 || !readings[0].ReadAt.Equal(now) {
		t.Fatal(readings)
	}
	for _, bad := range []string{`{"rateLimits":{"primary":{"usedPercent":0}}}`, `{"type":"turn.completed","usage":{"input_tokens":200}}`, `{"context_window":{"used_percentage":40}}`} {
		p = Parser{}
		if got := p.Codex([]byte(bad), now); len(got) != 0 {
			t.Fatal(got)
		}
	}
}
func TestClaudeFractionWindows(t *testing.T) {
	now := instant("2026-09-28T08:00:00Z")
	raw := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"seven_day_opus","utilization":1.2,"resetsAt":%d,"unifiedWindows":{"five_hour":{"utilization":0.42,"resetsAt":%d},"seven_day":{"utilization":0.3,"resetsAt":%d}}}}`, now.Add(24*time.Hour).Unix(), now.Add(time.Hour).Unix(), now.Add(24*time.Hour).Unix())
	got := Claude([]byte(raw), now)
	if len(got) != 3 || got[0].Bucket != "five_hour" || got[0].UsedPercent != 42 || got[1].UsedPercent != 30 || got[2].Bucket != "seven_day_opus" || got[2].UsedPercent != 100 {
		t.Fatal(got)
	}
	for _, v := range got {
		denied := v.Bucket == "seven_day_opus"
		if v.Routable(now) == denied || (v.OrdinaryUsageAllowed == nil) == denied {
			t.Fatalf("bucket %s routable=%t allowed=%v", v.Bucket, v.Routable(now), v.OrdinaryUsageAllowed)
		}
	}
	if got := Claude([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":12}}}`), now); len(got) != 0 {
		t.Fatal(got)
	}
}
func TestCapacityFileTimestampAndSafety(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := instant("2026-09-28T14:00:00Z")
	at := now.Add(-2 * time.Hour)
	raw := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.1,"resetsAt":%d}}`, now.Add(time.Hour).Unix())
	path := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(path, []byte(raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path, "claude", now)
	if err != nil || len(got) != 1 || !got[0].ReadAt.Equal(at) || got[0].Freshness(now) != "stale" {
		t.Fatal(got, err)
	}
	// Appending ordinary output must not turn an old undated event fresh.
	if err := os.WriteFile(path, []byte(raw+"\n"+`{"type":"assistant"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	got, err = ReadFile(path, "claude", now)
	if err != nil || len(got) != 0 {
		t.Fatal("old undated quota refreshed", got, err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(link, "claude", now); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := ReadFile(filepath.Join(dir, "credentials.json"), "claude", now); err == nil {
		t.Fatal("credential path accepted")
	}
}

func TestSparseClaudeDenialDoesNotNeedUtilization(t *testing.T) {
	now := instant("2026-09-28T14:00:00Z")
	p := Parser{}
	raw := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.3,"resetsAt":%d}}`, now.Add(time.Hour).Unix())
	if len(p.Claude([]byte(raw), now)) != 1 {
		t.Fatal("missing baseline")
	}
	got := p.Claude([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}`), now.Add(time.Second))
	if len(got) != 1 || *got[0].OrdinaryUsageAllowed {
		t.Fatal("sparse rejection ignored")
	}
	if got := p.Claude([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`), now.Add(2*time.Second)); len(got) != 0 {
		t.Fatal("allowed-only event freshened quota")
	}
}

func TestFullCodexSnapshotDropsMissingBucket(t *testing.T) {
	now := instant("2026-09-28T14:00:00Z")
	p := Parser{}
	raw := fmt.Sprintf(`{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300,"resetsAt":%d},"secondary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":%d}}}`, now.Add(time.Hour).Unix(), now.Add(24*time.Hour).Unix())
	if got := p.CodexSnapshot([]byte(raw), now); len(got) != 2 {
		t.Fatal(got)
	}
	raw = fmt.Sprintf(`{"rateLimits":{"primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":%d}}}`, now.Add(time.Hour).Unix())
	if got := p.CodexSnapshot([]byte(raw), now.Add(time.Second)); len(got) != 1 || got[0].WindowKind != "5h" {
		t.Fatal(got)
	}
}
func TestClaudeSparseSnapshotPreservesTimesAndExpiresPeers(t *testing.T) {
	now := instant("2026-09-28T14:00:00Z")
	p := Parser{}
	weekly := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"seven_day_opus","utilization":1,"resetsAt":%d}}`, now.Add(time.Minute).Unix())
	p.Claude([]byte(weekly), now)
	short := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.1,"resetsAt":%d}}`, now.Add(time.Hour).Unix())
	got := p.Claude([]byte(short), now.Add(time.Second))
	if len(got) != 2 || !got[1].ReadAt.Equal(now) {
		t.Fatal(got)
	}
	got = p.Claude([]byte(short), now.Add(2*time.Minute))
	if len(got) != 1 || got[0].WindowKind != "5h" {
		t.Fatal(got)
	}
}

func TestClaudeRejectionDeniesOnlyTheNamedWindow(t *testing.T) {
	now := time.Now().UTC()
	p := Parser{}
	weeklyAt := now
	weekly := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"seven_day","utilization":0.2,"resetsAt":%d}}`, now.Add(48*time.Hour).Unix())
	five := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.4,"resetsAt":%d}}`, now.Add(2*time.Hour).Unix())
	if len(p.Claude([]byte(weekly), weeklyAt)) != 1 || len(p.Claude([]byte(five), now.Add(time.Second))) != 2 {
		t.Fatal("missing baseline")
	}
	named := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","utilization":0.9,"resetsAt":%d,"unifiedWindows":{"seven_day":{"utilization":0.25,"resetsAt":%d}}}}`, now.Add(2*time.Hour).Unix(), now.Add(48*time.Hour).Unix())
	got := p.Claude([]byte(named), now.Add(2*time.Second))
	if len(got) != 2 {
		t.Fatal(got)
	}
	for _, v := range got {
		switch v.Bucket {
		case "five_hour":
			if v.OrdinaryUsageAllowed == nil || *v.OrdinaryUsageAllowed {
				t.Fatal("named window stayed allowed", v)
			}
		case "seven_day":
			if v.OrdinaryUsageAllowed == nil || !*v.OrdinaryUsageAllowed || !v.ReadAt.Equal(weeklyAt) {
				t.Fatal("peer window changed", v)
			}
		default:
			t.Fatal(v.Bucket)
		}
	}
	sparse := p.Claude([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}`), now.Add(3*time.Second))
	if len(sparse) != 2 || sparse[1].Bucket != "seven_day" || sparse[1].OrdinaryUsageAllowed == nil || !*sparse[1].OrdinaryUsageAllowed || !sparse[1].ReadAt.Equal(weeklyAt) {
		t.Fatal("sparse named rejection spread", sparse)
	}
	unnamed := []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","unifiedWindows":{"seven_day":{"utilization":0.9,"resetsAt":` + fmt.Sprint(now.Add(48*time.Hour).Unix()) + `}}}}`)
	if !ClaudeUnnamedStop(unnamed) || ClaudeUnnamedStop([]byte(named)) || ClaudeUnnamedStop([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`)) {
		t.Fatal("unnamed stop detection")
	}
	if got := p.Claude(unnamed, now.Add(4*time.Second)); len(got) != 0 {
		t.Fatal("unnamed rejection rewrote windows", got)
	}
	later := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.1,"resetsAt":%d}}`, now.Add(2*time.Hour).Unix())
	got = p.Claude([]byte(later), now.Add(5*time.Second))
	if len(got) != 2 || !got[1].ReadAt.Equal(weeklyAt) || got[1].OrdinaryUsageAllowed == nil || !*got[1].OrdinaryUsageAllowed {
		t.Fatal("peer lost after unnamed stop", got)
	}
}
