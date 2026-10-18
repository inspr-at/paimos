// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"testing"
	"time"
)

func TestLeavingLevelSelection(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		budget, estimate time.Duration
		known, inbox     bool
		level            string
		start            time.Duration
	}{
		{"finish fits", 15 * time.Minute, 5 * time.Minute, true, true, "wrap_up", 0},
		{"ten minutes requires handover", 15 * time.Minute, 10 * time.Minute, true, true, "pause", 5 * time.Minute},
		{"finish misses deadline", 4 * time.Minute, 6 * time.Minute, true, true, "pause", 0},
		{"unknown estimate waits", 15 * time.Minute, 0, false, true, "pause", 5 * time.Minute},
		{"finish fits near deadline", 2 * time.Minute, time.Minute, true, true, "wrap_up", 0},
		{"quickly without finish", 2 * time.Minute, 0, false, true, "pause_quickly", 0},
		{"finish misses near deadline", 2 * time.Minute, 3 * time.Minute, true, true, "pause_quickly", 0},
		{"at deadline", 0, 0, false, true, "stop_now", 0},
		{"overdue", -time.Second, 0, false, true, "stop_now", 0},
		{"no inbox waits for deadline", 15 * time.Minute, time.Minute, true, false, "stop_now", 15 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var remaining *time.Duration
			if tc.known {
				remaining = &tc.estimate
			}
			level, start := leavingLevel(now, now.Add(tc.budget), leavingEstimate{finish: remaining}, tc.inbox)
			if level != tc.level || !start.Equal(now.Add(tc.start)) {
				t.Fatalf("got %s at %s", level, start)
			}
		})
	}
}

func TestPauseWakeHintsFollowDatabaseClockTransitions(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	start := now.Add(5 * time.Minute)
	pause := &Pause{State: "requested", Level: "pause", StartsAt: &start, DeadlineAt: now.Add(15 * time.Minute)}
	s := stampPause(Session{Pause: pause}, now)
	if s.Pause.Deliver || s.Pause.WakeInMS != (5*time.Minute).Milliseconds() {
		t.Fatal(s.Pause)
	}
	s = stampPause(Session{Pause: pause}, start)
	if !s.Pause.Deliver || s.Pause.WakeInMS != (8*time.Minute).Milliseconds() {
		t.Fatal(s.Pause)
	}
	pause.Level = "pause_quickly"
	s = stampPause(Session{Pause: pause}, now.Add(13*time.Minute))
	if s.Pause.WakeInMS != (2 * time.Minute).Milliseconds() {
		t.Fatal(s.Pause)
	}
	pause.Level, pause.State = "stop_now", "requested"
	pause.StartsAt = &pause.DeadlineAt
	s = stampPause(Session{Pause: pause}, now)
	if s.Pause.Deliver || s.Pause.WakeInMS != (15*time.Minute).Milliseconds() {
		t.Fatal(s.Pause)
	}
	pause.State = "cancelled"
	if stampPause(Session{Pause: pause}, now).Pause.WakeInMS != 0 {
		t.Fatal("cancelled deadline still schedules a wake")
	}
}

func TestLeavingLevelUsesHandoverAndCommandTiming(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	interruptible, blocked := true, false
	point, command := time.Minute, time.Minute
	longPoint := 5 * time.Minute
	for _, tc := range []struct {
		name     string
		estimate leavingEstimate
		budget   time.Duration
		level    string
	}{
		{"good point fits near deadline", leavingEstimate{nextPoint: &point, interrupt: &interruptible}, 2 * time.Minute, "pause"},
		{"good point misses", leavingEstimate{nextPoint: &longPoint, interrupt: &interruptible}, 3 * time.Minute, "pause_quickly"},
		{"command wait reduces handover budget", leavingEstimate{nextPoint: &point, commandLeft: &command}, 2 * time.Minute, "pause_quickly"},
		{"command can be interrupted", leavingEstimate{nextPoint: &point, commandLeft: &command, interrupt: &interruptible}, 2 * time.Minute, "pause"},
		{"blocked command keeps cooperative pause", leavingEstimate{nextPoint: &longPoint, commandLeft: &command, interrupt: &blocked}, 2 * time.Minute, "pause"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			level, _ := leavingLevel(now, now.Add(tc.budget), tc.estimate, true)
			if level != tc.level {
				t.Fatalf("got %s, want %s", level, tc.level)
			}
		})
	}
}

func TestPausePlanningFreshnessAndCountdown(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	finish, point, command := 5.0, 2.0, 3.0
	interrupt := false
	s := Session{PauseProgress: &PauseProgress{ReportedAt: now.Add(-time.Minute), FinishInMin: &finish, NextPointInMin: &point, CommandLeftMin: &command, Interrupt: &interrupt}}
	estimate := freshPauseEstimate(s, now, time.Minute)
	if estimate.finish == nil || *estimate.finish != 4*time.Minute || estimate.nextPoint == nil || *estimate.nextPoint != time.Minute || estimate.commandLeft == nil || *estimate.commandLeft != 2*time.Minute || estimate.interrupt == nil || *estimate.interrupt {
		t.Fatalf("report did not count down: %+v", estimate)
	}
	for _, reported := range []time.Time{now.Add(-2*time.Minute - time.Nanosecond), now.Add(time.Nanosecond)} {
		s.PauseProgress.ReportedAt = reported
		if e := freshPauseEstimate(s, now, time.Minute); e.finish != nil || e.nextPoint != nil || e.commandLeft != nil || e.interrupt != nil {
			t.Fatal("stale or future report supplied planning evidence")
		}
	}
}

func TestRunningWrapUpUsesItsRemainingLevelBudget(t *testing.T) {
	finish := 90 * time.Second
	estimate := leavingEstimate{finish: &finish}
	if !estimate.levelFits("wrap_up", 2*time.Minute) {
		t.Fatal("a running Wrap up with time to finish was escalated")
	}
	if estimate.finishFits(time.Minute) {
		t.Fatal("a finish beyond the remaining budget was accepted")
	}
}

func TestOwnedStopCapabilityKeepsUnmanagedControlFence(t *testing.T) {
	if _, err := normalizeCaps([]string{"status", "owned_stop_v1"}, "unmanaged"); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"stop", "interrupt", "managed_control_v1"} {
		if _, err := normalizeCaps([]string{cap}, "unmanaged"); err == nil {
			t.Fatalf("unmanaged session accepted %s", cap)
		}
	}
}
