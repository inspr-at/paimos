// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"testing"
	"time"
)

func TestVoiceEstimateSeconds(t *testing.T) {
	cases := []struct {
		bytes int
		want  int64
	}{
		{0, 1}, {1, 1}, {4000, 1}, {4001, 2}, {8000, 2}, {12 << 20, 3146},
	}
	for _, c := range cases {
		if got := voiceEstimateSeconds(c.bytes); got != c.want {
			t.Errorf("voiceEstimateSeconds(%d) = %d, want %d", c.bytes, got, c.want)
		}
	}
}

func TestVoiceInflight(t *testing.T) {
	resetVoiceLimitsForTest()
	defer resetVoiceLimitsForTest()

	const user = int64(42)
	for i := range voiceMaxInflightPerUser {
		if !voiceAcquireInflight(user) {
			t.Fatalf("acquire %d rejected below the cap", i+1)
		}
	}
	if voiceAcquireInflight(user) {
		t.Fatal("acquire above the cap admitted")
	}
	// Another user is unaffected.
	if !voiceAcquireInflight(7) {
		t.Fatal("second user rejected by first user's slots")
	}
	voiceReleaseInflight(user)
	if !voiceAcquireInflight(user) {
		t.Fatal("acquire after release rejected")
	}
}

func TestVoiceRateWindow(t *testing.T) {
	resetVoiceLimitsForTest()
	defer resetVoiceLimitsForTest()

	t0 := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if d := voiceRates.checkAndRecord("stt:1", t0, 2); d != 0 {
		t.Fatalf("first attempt limited: %v", d)
	}
	if d := voiceRates.checkAndRecord("stt:1", t0.Add(time.Second), 2); d != 0 {
		t.Fatalf("second attempt limited: %v", d)
	}
	d := voiceRates.checkAndRecord("stt:1", t0.Add(2*time.Second), 2)
	if d != 58*time.Second {
		t.Fatalf("retry-after = %v, want 58s (oldest attempt leaves the window)", d)
	}
	// A different key has its own window.
	if d := voiceRates.checkAndRecord("tts:1", t0.Add(2*time.Second), 2); d != 0 {
		t.Fatalf("other key limited: %v", d)
	}
	// After the window slides past the old attempts, admits again.
	if d := voiceRates.checkAndRecord("stt:1", t0.Add(voiceLimitWindow+2*time.Second), 2); d != 0 {
		t.Fatalf("post-window attempt limited: %v", d)
	}
}

func TestVoiceActionClassesShareSurfacePools(t *testing.T) {
	tests := map[string]string{
		voiceActionIntakeSTT: voiceClassSTT, voiceActionAgentModeSTT: voiceClassSTT,
		voiceActionIntakeTTS: voiceClassTTS, voiceActionAgentModeTTS: voiceClassTTS,
	}
	for action, wantClass := range tests {
		if got := voiceClassForAction(action); got != wantClass {
			t.Fatalf("voiceClassForAction(%q)=%q want %q", action, got, wantClass)
		}
	}
	if got := voiceClassForAction("unclassified"); got != "" {
		t.Fatalf("unknown action class=%q", got)
	}
}

func TestTimeToUTCMidnight(t *testing.T) {
	d := timeToUTCMidnight()
	if d <= 0 || d > 24*time.Hour {
		t.Fatalf("timeToUTCMidnight = %v, want (0, 24h]", d)
	}
}

func TestVoiceDailyBudgetEnvFallback(t *testing.T) {
	t.Setenv(envVoiceSTTDailySeconds, "-5")
	if got := voiceDailyBudget("intake_stt"); got != voiceSTTDailySecondsDefault {
		t.Fatalf("negative env budget = %d, want default %d", got, voiceSTTDailySecondsDefault)
	}
	t.Setenv(envVoiceTTSDailyChars, "1234")
	if got := voiceDailyBudget("intake_tts"); got != 1234 {
		t.Fatalf("env budget = %d, want 1234", got)
	}
}
