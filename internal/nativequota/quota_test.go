// SPDX-License-Identifier: AGPL-3.0-only

package nativequota

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type testClock struct{ at time.Time }

func (c *testClock) Now() time.Time { return c.at }

func quotaFixture(t *testing.T, harness string) (*Collector, Binding, *testClock, Qualification) {
	t.Helper()
	clock := &testClock{at: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)}
	b := Binding{Runtime: nativecontract.Binding{TenantID: "10000000-0000-0000-0000-000000000001", ProjectID: "10000000-0000-0000-0000-000000000002",
		OwnedSessionID: "10000000-0000-0000-0000-000000000003", HarnessSessionID: "10000000-0000-0000-0000-000000000004",
		RuntimeEpoch: "10000000-0000-0000-0000-000000000005", DaemonID: "daemon-one", DaemonGeneration: "generation-one", BindingRevision: 2},
		AccountID: "10000000-0000-0000-0000-000000000006", Harness: harness, AccountBindingRevision: 7, ProfileBindingRevision: 11,
		Binary: nativecontract.BinaryPin{Path: "/synthetic/bin/" + harness, SHA256: strings.Repeat("a", 64), Version: "fixture-version", Protocol: "fixture-protocol"}}
	b.Identity = nativecontract.NativeIdentity{Provider: harness, RuntimeEpoch: b.Runtime.RuntimeEpoch,
		NativeSessionRef: "session-one", NativeThreadRef: "thread-one", ProcessIdentityDigest: strings.Repeat("d", 64), PID: 42, StartedAt: clock.at}
	q := Qualification{Harness: harness, Binary: b.Binary, EvidenceDigest: strings.Repeat("b", 64), Schema: CodexSchema, CodexBuckets: []string{"model-one", "model-two"}}
	if harness == "claude" {
		b.SDK = nativecontract.BinaryPin{Path: "/synthetic/sdk", SHA256: strings.Repeat("c", 64), Version: "fixture-sdk-version", Protocol: "fixture-sdk-protocol"}
		q.SDK, q.Schema, q.CodexBuckets = b.SDK, ClaudeSchema, nil
	}
	c, err := New(b, &q, clock)
	if err != nil {
		t.Fatal(err)
	}
	ready := Fact{State: "ready", ObservedAt: clock.at}
	if err := c.ObserveReadiness(b, Readiness{SignIn: ready, BinaryPin: ready, HostSlots: Slots{Fact: ready, Used: 2, Limit: 4}, AccountSlots: Slots{Fact: ready, Used: 1, Limit: 2}}); err != nil {
		t.Fatal(err)
	}
	return c, b, clock, q
}

func event(harness string, percent float64, reset time.Time) []byte {
	if harness == "claude" {
		return []byte(fmt.Sprintf(`{"type":"rate_limit_event","session_id":"session-one","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":%g,"resetsAt":%d}}`, percent/100, reset.Unix()))
	}
	return []byte(fmt.Sprintf(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":%g,"windowDurationMins":300,"resetsAt":%d}},"ordinaryUsageAllowed":true}}`, percent, reset.Unix()))
}

func scope(harness string) string {
	if harness == "claude" {
		return "5h/five_hour"
	}
	return "5h/codex"
}

func snapshot(t *testing.T, c *Collector, b Binding, scope string) Snapshot {
	t.Helper()
	s, err := c.Snapshot(b, scope)
	if err != nil {
		t.Fatal(err)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s.Quota)
	if err != nil || v.ValidateJSON("NativeQuota", raw) != nil {
		t.Fatalf("quota projection violates C0: %v", err)
	}
	return s
}

// Named risk: consumed tokens, remaining allowance and unknown quota are
// different facts. Staleness/probe failure must never fabricate zero or logout.
func TestStreamQuotaFreshnessAndAccountIsolation(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			c, b, clock, q := quotaFixture(t, harness)
			key := scope(harness)
			missing := snapshot(t, c, b, key)
			if missing.Quota.State != "unknown" || missing.Quota.UsedPercent != nil || missing.Measurement != "missing" || missing.Readiness.SignIn.State != "ready" {
				t.Fatalf("missing reading changed identity or quota: %+v", missing)
			}
			reset := clock.at.Add(2 * time.Hour)
			if err := c.Observe(b, 1, event(harness, 25, reset)); err != nil {
				t.Fatal(err)
			}
			fresh := snapshot(t, c, b, key)
			if fresh.Quota.State != "known" || fresh.Measurement != "fresh" || fresh.Quota.UsedPercent == nil || *fresh.Quota.UsedPercent != 25 || fresh.Allowance.State != "allowed" {
				t.Fatalf("valid stream observation lost: %+v", fresh)
			}
			for _, mismatch := range []struct {
				name string
				edit func(*Binding)
				want error
			}{
				{"account", func(v *Binding) { v.AccountID = "10000000-0000-0000-0000-000000000099" }, ErrAccount},
				{"tenant", func(v *Binding) { v.Runtime.TenantID = "10000000-0000-0000-0000-000000000099" }, ErrAccount},
				{"profile", func(v *Binding) { v.ProfileBindingRevision++ }, ErrRevision},
				{"enrollment", func(v *Binding) { v.AccountBindingRevision++ }, ErrRevision},
				{"runtime", func(v *Binding) { v.Runtime.RuntimeEpoch = "10000000-0000-0000-0000-000000000099" }, ErrRuntime},
				{"daemon", func(v *Binding) { v.Runtime.DaemonGeneration = "old-generation" }, ErrRuntime},
				{"connection", func(v *Binding) { v.Identity.NativeThreadRef = "foreign-thread" }, ErrRuntime},
				{"pin", func(v *Binding) { v.Binary.Version = "foreign-version" }, ErrQualification},
			} {
				t.Run(mismatch.name, func(t *testing.T) {
					foreign := b
					mismatch.edit(&foreign)
					if err := c.Observe(foreign, 2, event(harness, 0, reset)); !errors.Is(err, mismatch.want) {
						t.Fatalf("wrong mismatch cause: %v", err)
					}
					if err := c.Failed(foreign, "timeout"); !errors.Is(err, mismatch.want) {
						t.Fatalf("foreign failure changed current account: %v", err)
					}
					if _, err := c.Readings(foreign); !errors.Is(err, mismatch.want) {
						t.Fatalf("foreign ingress cause: %v", err)
					}
				})
			}
			if got := snapshot(t, c, b, key); got.Quota.UsedPercent == nil || *got.Quota.UsedPercent != 25 || got.Readiness != fresh.Readiness {
				t.Fatal("rejected foreign updates polluted current facts")
			}
			clock.at = clock.at.Add(FreshFor)
			stale := snapshot(t, c, b, key)
			if stale.Quota.State != "unknown" || stale.Quota.UsedPercent != nil || stale.Measurement != "stale" || stale.Readiness != fresh.Readiness {
				t.Fatalf("stale reading became allowance or logout: %+v", stale)
			}
			if _, err := c.Readings(b); !errors.Is(err, ErrNoReadings) {
				t.Fatalf("stale ingress reported success: %v", err)
			}
			if err := c.Observe(b, 1, event(harness, 0, reset)); !errors.Is(err, ErrSequence) {
				t.Fatalf("replayed event freshened quota: %v", err)
			}
			if err := c.Observe(b, 2, event(harness, 0, reset)); err != nil {
				t.Fatal(err)
			}
			zero := snapshot(t, c, b, key)
			if zero.Quota.UsedPercent == nil || *zero.Quota.UsedPercent != 0 || zero.Measurement != "fresh" {
				t.Fatal("explicit vendor zero was confused with absence")
			}
			// Account quota outlives individual turns/cursors on this connection.
			nextTurn := b
			nextTurn.Identity.NativeTurnRef, nextTurn.Identity.ResumeCursor = "turn-two", "cursor-two"
			if err := c.Observe(nextTurn, 3, event(harness, 0, reset)); err != nil {
				t.Fatalf("persistent connection metadata retargeted account quota: %v", err)
			}
			if err := c.Failed(b, "timeout"); err != nil {
				t.Fatal(err)
			}
			failed := snapshot(t, c, b, key)
			if failed.Quota.UsedPercent != nil || failed.Quota.State != "unknown" || failed.Measurement != "failed" || failed.Failure != "timeout" || failed.Readiness != fresh.Readiness {
				t.Fatalf("failed probe changed independent facts: %+v", failed)
			}
			probe, err := c.ProbeFailure(b)
			if err != nil || !probe.Available || !probe.MeasurementOnly || probe.Readiness.Result != "timeout" || probe.Readiness.BindingRevision != b.AccountBindingRevision {
				t.Fatalf("quota failure became a sign-in failure: %+v %v", probe, err)
			}
			if c.IdleCapability() != "unsupported" {
				t.Fatal("idle probing was qualified by a stream fixture")
			}
			// A new profile revision starts unknown; old evidence cannot follow it.
			next := b
			next.ProfileBindingRevision++
			successor, err := New(next, &q, clock)
			if err != nil {
				t.Fatal(err)
			}
			if err := successor.Observe(b, 3, event(harness, 10, reset)); !errors.Is(err, ErrRevision) {
				t.Fatalf("predecessor followed successor: %v", err)
			}
			if s := snapshot(t, successor, next, key); s.Quota.UsedPercent != nil || s.Quota.State != "unknown" {
				t.Fatal("new profile inherited a predecessor reading")
			}
		})
	}
}

func TestSparseQuotaDenialPreservesConsumptionAndScope(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			c, b, clock, _ := quotaFixture(t, harness)
			start := clock.at
			reset := start.Add(time.Hour)
			if err := c.Observe(b, 1, event(harness, 30, reset)); err != nil {
				t.Fatal(err)
			}
			clock.at = start.Add(9 * time.Minute)
			denial := []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}`)
			if harness == "codex" {
				denial = []byte(`{"method":"account/rateLimits/updated","params":{"ordinaryUsageAllowed":false}}`)
			}
			if err := c.Observe(b, 2, denial); err != nil {
				t.Fatal(err)
			}
			limited := snapshot(t, c, b, scope(harness))
			if limited.Quota.State != "limited" || limited.Allowance.State != "limited" || limited.Quota.UsedPercent == nil || *limited.Quota.UsedPercent != 30 || !limited.Quota.ObservedAt.Equal(start) {
				t.Fatalf("denial fabricated or freshened consumption: %+v", limited)
			}
			sibling := "weekly/seven_day_sonnet"
			if harness == "codex" {
				sibling = "weekly/model-one"
			}
			other := snapshot(t, c, b, sibling)
			if other.Quota.UsedPercent != nil || harness == "claude" && other.Allowance.State != "unknown" {
				t.Fatal("named denial was spread to a sibling quota scope")
			}
			clock.at = start.Add(FreshFor)
			stale := snapshot(t, c, b, scope(harness))
			if stale.Quota.UsedPercent != nil || stale.Allowance.State != "limited" || stale.Measurement != "stale" {
				t.Fatal("sparse denial freshened old utilization or recovered the stop")
			}
			if err := c.Failed(b, "protocol"); err != nil {
				t.Fatal(err)
			}
			if failed := snapshot(t, c, b, scope(harness)); failed.Allowance.State != "limited" || failed.Quota.UsedPercent != nil || failed.Measurement != "failed" {
				t.Fatal("probe failure cleared a vendor denial")
			}
			if err := c.Observe(b, 3, event(harness, 20, reset)); err != nil {
				t.Fatal(err)
			}
			if recovered := snapshot(t, c, b, scope(harness)); recovered.Quota.State != "known" || recovered.Allowance.State != "allowed" || recovered.Quota.UsedPercent == nil || *recovered.Quota.UsedPercent != 20 {
				t.Fatalf("complete explicit recovery lost: %+v", recovered)
			}
			if _, err := c.ProbeFailure(b); !errors.Is(err, ErrNoReadings) {
				t.Fatalf("successful measurement replayed an old check failure: %v", err)
			}
			clock.at = reset
			if expired := snapshot(t, c, b, scope(harness)); expired.Quota.UsedPercent != nil || expired.Quota.State != "unknown" || expired.Allowance.State != "unknown" {
				t.Fatal("reset guessed replenishment")
			}
		})
	}
	t.Run("denial-without-utilization", func(t *testing.T) {
		c, b, clock, _ := quotaFixture(t, "claude")
		reset := clock.at.Add(time.Hour)
		raw := []byte(fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":%d}}`, reset.Unix()))
		if err := c.Observe(b, 1, raw); err != nil {
			t.Fatal(err)
		}
		s := snapshot(t, c, b, "5h/five_hour")
		if s.Quota.UsedPercent != nil || s.Allowance.State != "limited" || s.Allowance.ResetsAt == nil || !s.Allowance.ResetsAt.Equal(reset) || s.Measurement != "missing" {
			t.Fatalf("sparse denial invented quota or lost its reset: %+v", s)
		}
		if _, err := c.Readings(b); !errors.Is(err, ErrNoReadings) {
			t.Fatalf("sparse denial fabricated a numeric ingress reading: %v", err)
		}
	})
	t.Run("claude-account-stop-recovery", func(t *testing.T) {
		c, b, clock, _ := quotaFixture(t, "claude")
		if err := c.Observe(b, 1, []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`)); err != nil {
			t.Fatal(err)
		}
		if s := snapshot(t, c, b, "5h/five_hour"); s.Quota.State != "limited" || s.Quota.UsedPercent != nil {
			t.Fatal("unnamed account stop invented utilization")
		}
		clock.at = clock.at.Add(time.Minute)
		if err := c.Observe(b, 2, event("claude", 10, clock.at.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
		if s := snapshot(t, c, b, "5h/five_hour"); s.Quota.State != "known" || s.Allowance.State != "allowed" {
			t.Fatal("explicit complete allowance did not recover an unnamed stop")
		}
	})
}

func TestQuotaIngressKeepsPrivateFieldsOutAndCopiesFacts(t *testing.T) {
	c, b, clock, _ := quotaFixture(t, "codex")
	reset := clock.at.Add(time.Hour)
	raw := []byte(fmt.Sprintf(`{"method":"account/rateLimits/updated","params":{"rateLimitsByLimitId":{"model-one":{"limitId":"model-one","primary":{"usedPercent":40,"windowDurationMins":300,"resetsAt":%d}},"model-two":{"limitId":"model-two","secondary":{"usedPercent":60,"windowDurationMins":10080,"resetsAt":%d}}},"ordinaryUsageAllowed":true,"planType":"private-plan","email":"private-person","headers":{"fixture":"private-header"},"home":"private-home","thinking":"private-reasoning"}}`, reset.Unix(), clock.at.Add(6*24*time.Hour).Unix()))
	if err := c.Observe(b, 1, raw); err != nil {
		t.Fatal(err)
	}
	body, err := c.Readings(b)
	if err != nil || len(body.Readings) != 2 || !body.UsageProbe || body.BindingRevision != 7 {
		t.Fatalf("existing ingress projection: %+v %v", body, err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "synthetic") || strings.Contains(string(encoded), "profile_binding_revision") {
		t.Fatal("raw metadata or local authority entered readings ingress")
	}
	for _, reading := range body.Readings {
		if reading.Validate(clock.at) != nil || reading.Source != "agentd" || reading.Plan != "" || reading.Phase != "" || reading.RunID != "" {
			t.Fatal("readings bypassed existing normalization")
		}
	}
	body.Readings[0].UsedPercent = 0
	*body.Readings[0].OrdinaryUsageAllowed = false
	s := snapshot(t, c, b, "5h/model-one")
	if s.Quota.UsedPercent == nil || *s.Quota.UsedPercent != 40 || s.Allowance.State != "allowed" {
		t.Fatal("caller mutated collector through ingress body")
	}
	*s.Quota.UsedPercent = 0
	if s := snapshot(t, c, b, "5h/model-one"); s.Quota.UsedPercent == nil || *s.Quota.UsedPercent != 40 {
		t.Fatal("caller mutated collector through public quota")
	}
	clock.at = clock.at.Add(9 * time.Minute)
	patch := []byte(`{"method":"account/rateLimits/updated","params":{"rateLimitsByLimitId":{"model-one":{"primary":{"windowDurationMins":300,"usedPercent":50}}}}}`)
	if err := c.Observe(b, 2, patch); err != nil {
		t.Fatal(err)
	}
	clock.at = clock.at.Add(time.Minute)
	if s := snapshot(t, c, b, "5h/model-one"); s.Quota.UsedPercent != nil || s.Measurement != "stale" {
		t.Fatal("incomplete Codex patch freshened a reconstructed measurement")
	}
	t.Run("equal-codex-alias", func(t *testing.T) {
		c, b, clock, _ := quotaFixture(t, "codex")
		w := fmt.Sprintf(`{"limitId":"model-one","primary":{"usedPercent":10,"windowDurationMins":300,"resetsAt":%d}}`, clock.at.Add(time.Hour).Unix())
		raw := []byte(fmt.Sprintf(`{"method":"account/rateLimits/updated","params":{"rateLimits":%s,"rateLimitsByLimitId":{"model-one":%s}}}`, w, w))
		if err := c.Observe(b, 1, raw); err != nil {
			t.Fatalf("valid singular/by-ID alias rejected: %v", err)
		}
		if s := snapshot(t, c, b, "5h/model-one"); s.Quota.UsedPercent == nil || *s.Quota.UsedPercent != 10 {
			t.Fatal("aliased vendor fact lost")
		}
	})
	t.Run("equal-claude-alias", func(t *testing.T) {
		c, b, clock, _ := quotaFixture(t, "claude")
		reset := clock.at.Add(time.Hour).Unix()
		raw := []byte(fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","utilization":0.1,"resetsAt":%d,"unifiedWindows":{"five_hour":{"utilization":0.1,"resetsAt":%d},"seven_day_opus":{"utilization":0.4,"resetsAt":%d},"seven_day_sonnet":{"utilization":0.5,"resetsAt":%d}}}}`, reset, reset, reset, reset))
		if err := c.Observe(b, 1, raw); err != nil {
			t.Fatalf("valid top-level/unified alias rejected: %v", err)
		}
		if s := snapshot(t, c, b, "weekly/seven_day_opus"); s.Quota.UsedPercent == nil || *s.Quota.UsedPercent != 40 {
			t.Fatal("model-specific Claude quota lost")
		}
		if s := snapshot(t, c, b, "weekly/seven_day_sonnet"); s.Quota.UsedPercent == nil || *s.Quota.UsedPercent != 50 {
			t.Fatal("model-specific Claude quotas conflated")
		}
	})
}

func TestQuotaRejectsUnqualifiedAndMalformedFrames(t *testing.T) {
	c, b, clock, q := quotaFixture(t, "codex")
	bad := q
	bad.Binary.SHA256 = strings.Repeat("f", 64)
	if _, err := New(b, &bad, clock); !errors.Is(err, ErrQualification) {
		t.Fatalf("unqualified binary accepted: %v", err)
	}
	unqualified, err := New(b, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := unqualified.Observe(b, 1, event("codex", 0, clock.at.Add(time.Hour))); !errors.Is(err, ErrQualification) {
		t.Fatalf("missing stream qualification accepted: %v", err)
	}
	if s := snapshot(t, unqualified, b, "5h/codex"); s.Quota.State != "unsupported" || s.Quota.UsedPercent != nil || unqualified.IdleCapability() != "unsupported" {
		t.Fatal("unsupported capability produced a numeric allowance")
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		want error
	}{
		{"consumption-only", []byte(`{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"total":{"inputTokens":200}}}}`), ErrProtocol},
		{"transcript", []byte(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":0}}}}`), ErrProtocol},
		{"missing", []byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":0}}}}`), ErrNoReadings},
		{"foreign-thread", []byte(`{"method":"account/rateLimits/updated","params":{"threadId":"foreign-thread","ordinaryUsageAllowed":true}}`), ErrRuntime},
		{"private-bucket", []byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"private-account-label"}}}`), ErrScope},
		{"negative", []byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"windowDurationMins":300,"usedPercent":-1}}}}`), ErrProtocol},
		{"duplicate", []byte(`{"method":"account/rateLimits/updated","method":"rateLimits/updated","params":{"ordinaryUsageAllowed":true}}`), ErrProtocol},
		{"case-alias", []byte(`{"method":"account/rateLimits/updated","params":{"OrdinaryUsageAllowed":true}}`), ErrProtocol},
		{"deep", []byte(`{"method":"account/rateLimits/updated","ignored":` + strings.Repeat("[", 18) + "0" + strings.Repeat("]", 18) + `}`), ErrBounds},
		{"oversize", []byte(strings.Repeat(" ", nativecontract.NativeFrameBeforeDecodeBytes+1)), ErrBounds},
		{"trailing", []byte(`{"method":"account/rateLimits/updated","params":{"ordinaryUsageAllowed":true}} {}`), ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Observe(b, 1, tc.raw); !errors.Is(err, tc.want) {
				t.Fatalf("wrong rejection cause: %v, want %v", err, tc.want)
			}
			if s := snapshot(t, c, b, "5h/codex"); s.Quota.UsedPercent != nil || s.Readiness.SignIn.State != "ready" {
				t.Fatal("bad frame became a measurement or changed sign-in")
			}
		})
	}
	t.Run("whole-batch-window-bound", func(t *testing.T) {
		buckets := make(map[string]any)
		q.CodexBuckets = nil
		for i := 0; i < 16; i++ {
			bucket := fmt.Sprintf("model-%d", i)
			q.CodexBuckets = append(q.CodexBuckets, bucket)
			buckets[bucket] = map[string]any{"primary": map[string]any{"usedPercent": 10, "windowDurationMins": 300, "resetsAt": clock.at.Add(time.Hour).Unix()},
				"secondary": map[string]any{"usedPercent": 20, "windowDurationMins": 10080, "resetsAt": clock.at.Add(time.Hour).Unix()}}
		}
		c, err := New(b, &q, clock)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"method": "account/rateLimits/updated", "params": map[string]any{"rateLimitsByLimitId": buckets, "ordinaryUsageAllowed": false}})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Observe(b, 1, raw); !errors.Is(err, ErrBounds) {
			t.Fatalf("batch above scope bound admitted: %v", err)
		}
		if s := snapshot(t, c, b, "5h/model-0"); s.Quota.UsedPercent != nil || s.Allowance.State != "unknown" {
			t.Fatal("oversize batch partially mutated quota")
		}
	})
}
